package main

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"iwato/internal/openshell/client"
	"iwato/internal/openshell/gateway"
)

// TestDockerE2E runs Iwato on a Docker engine as on macOS (docs/adr/0002):
// the gateway container with upstream's Docker driver, a sandbox whose home
// is a labelled volume with a 1 GiB /dev/shm, and the home outliving a stop
// and a delete. It needs a Docker engine (.github/workflows/e2e.yml) and
// is skipped unless IWATO_E2E_DOCKER=1. It uses the gateway's port and
// removes the gateway it made.
func TestDockerE2E(t *testing.T) {
	if os.Getenv("IWATO_E2E_DOCKER") == "" {
		t.Skip("set IWATO_E2E_DOCKER=1 with a Docker engine")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()
	dir := t.TempDir()
	c := defaultConfig()
	c.Namespace = "e2e"
	e, err := NewEngine(c)
	if err != nil || !e.docker {
		t.Fatal(err, e)
	}
	// A small image of the convention: a non-root user whose home is
	// /home/dev, and WORKDIR outside it.
	img := filepath.Join(dir, "image")
	if err := os.MkdirAll(img, 0o755); err != nil {
		t.Fatal(err)
	}
	dockerfile := "FROM ubuntu:24.04\nRUN useradd -m -d /home/dev -u 1001 -s /bin/bash dev && install -d -o dev -g dev /sandbox\nUSER dev\nWORKDIR /sandbox\n"
	if err := os.WriteFile(filepath.Join(img, "Dockerfile"), []byte(dockerfile), 0o644); err != nil {
		t.Fatal(err)
	}
	const image = "iwato-e2e-docker:1"
	if out, err := exec.CommandContext(ctx, e.exe, "image", "build", "--tag", image, img).CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", image, err, out)
	}

	o := NewOpenShell(e, dir)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if t.Failed() {
			out, _ := exec.CommandContext(ctx, e.exe, "container", "logs", "--tail", "200", c.prefix()+"-gateway").CombinedOutput()
			t.Logf("gateway output:\n%s", out)
		}
		removed, err := gateway.Remove(ctx, o.gatewayCLI(), o.gatewayOptions(ctx))
		t.Log("removed", removed, err)
	})
	o.Prepare(ctx)
	if o.err != nil {
		log, _ := os.ReadFile(o.logPath())
		t.Fatalf("prepare: %v\n%s", o.err, tail(string(log), 4000))
	}
	w := newSandbox("e2e")
	w.Image = image
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		t.Log("delete:", o.Delete(ctx, w, true))
	})
	run := func(script string) string {
		t.Helper()
		_, conn, err := o.clients(ctx)
		if err != nil {
			t.Fatal(err)
		}
		p, err := client.Start(ctx, conn, openshellWorkspace, w.Name, []string{"/bin/sh", "-c", script}, client.Options{})
		if err != nil {
			t.Fatal(err)
		}
		defer p.Close()
		p.CloseWrite()
		out, _ := io.ReadAll(p)
		if code, err := p.Wait(); code != 0 || err != nil {
			t.Fatalf("%s: exit %d %v\n%s", script, code, err, out)
		}
		return string(out)
	}
	start := func() {
		t.Helper()
		if err := o.Start(ctx, w); err != nil {
			t.Fatal("start:", err, o.Status(ctx, w))
		}
	}

	start()
	out := run(`id -un; echo kept >"$HOME/kept"; df -k /dev/shm | tail -1`)
	if !strings.HasPrefix(out, "dev\n") || !strings.Contains(out, "1048576") {
		t.Fatalf("user and /dev/shm:\n%s", out)
	}
	if err := o.Stop(ctx, w); err != nil {
		t.Fatal("stop:", err)
	}
	start()
	if out := run(`cat "$HOME/kept"`); out != "kept\n" {
		t.Fatalf("after a restart: %q", out)
	}
	// A recreate keeps the home's volume.
	if err := o.Delete(ctx, w, false); err != nil {
		t.Fatal("delete:", err)
	}
	start()
	if out := run(`cat "$HOME/kept"`); out != "kept\n" {
		t.Fatalf("after a delete: %q", out)
	}
}

func tail(s string, n int) string { return s[max(0, len(s)-n):] }
