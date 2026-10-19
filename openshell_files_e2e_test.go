package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	v1 "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1"

	"iwato/internal/dav"
	"iwato/internal/hostterm"
	"iwato/internal/openshell/client"
)

// TestOpenShellFilesE2E reaches a sandbox's files through Iwato's WebDAV
// handler and the daemon's SFTP server (iwato-entrypoint sftp, rooted at
// the home, in a TTY-less exec), against the gateway that
// internal/openshell/wslc/e2e.sh runs (MODE=tls). (OpenShell's SFTP, for a
// sandbox without the daemon, is TestOpenShellTerminalsSurviveRestartE2E's.)
// It also tells whether the policy's filesystem.read_only holds for SFTP
// (the line starting "read_only:").
func TestOpenShellFilesE2E(t *testing.T) {
	addr := os.Getenv("OPENSHELL_E2E_ADDR")
	if addr == "" {
		t.Skip("run by internal/openshell/wslc/e2e.sh")
	}
	tlsDir := os.Getenv("OPENSHELL_E2E_TLS")
	ca, cert, key := filepath.Join(tlsDir, "ca.crt"), filepath.Join(tlsDir, "client", "tls.crt"), filepath.Join(tlsDir, "client", "tls.key")
	c := defaultConfig()
	c.WSLC = os.Getenv("OPENSHELL_E2E_WSLC")
	b, err := NewEngine(c)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := client.Dial(addr, ca, cert, key)
	if err != nil {
		t.Fatal(err)
	}
	sdk, err := v1.NewClient(v1.Config{Address: addr, TLS: &v1.TLSConfig{CAFile: ca, CertFile: cert, KeyFile: key}})
	if err != nil {
		t.Fatal(err)
	}
	o := &OpenShell{e: b, ready: make(chan struct{}), sdk: sdk, conn: conn, env: termEnv, terms: map[string]map[string]*hostterm.Session{}, imageDaemon: withDaemon}
	close(o.ready)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	if err := enablePolicyAdvisor(ctx, sdk); err != nil {
		t.Fatal("policy advisor:", err)
	}
	sandboxes := map[string]Sandbox{}
	pool := dav.NewPool(func(ctx context.Context, name string) (dav.Transport, error) {
		return o.DialFiles(ctx, sandboxes[name])
	})
	defer pool.CloseAll()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name, _, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/dav/"), "/")
		pool.ServeSandbox(w, r, name, dav.Options{Prefix: "/dav/" + name})
	}))
	defer srv.Close()
	do := func(method, path, body string, header ...string) (int, string) {
		t.Helper()
		req, _ := http.NewRequestWithContext(ctx, method, srv.URL+path, strings.NewReader(body))
		for i := 0; i+1 < len(header); i += 2 {
			req.Header.Set(header[i], header[i+1])
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		got, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(got)
	}
	expect := func(want int, method, path, body string, header ...string) string {
		t.Helper()
		code, got := do(method, path, body, header...)
		if code != want {
			t.Fatalf("%s %s: %d, want %d: %s", method, path, code, want, got)
		}
		return got
	}
	run := func(name, script string) string {
		t.Helper()
		res, err := sdk.Exec().Run(ctx, openshellWorkspace, name, []string{"sh", "-c", script})
		if err != nil {
			t.Fatalf("exec in %s: %v", name, err)
		}
		return strings.TrimSpace(string(res.Stdout) + string(res.Stderr))
	}

	// SFTP's root is the home (the daemon's server; the workspace, the
	// image's WORKDIR, is /sandbox). The fake wslc's sandboxes run on this machine, whose
	// homeDir stands for the home volume (e2e.sh makes it when missing):
	// the test keeps to a directory of its own there.
	const sub = "iwato-files-e2e"
	dir := homeDir + "/" + sub
	if _, err := os.Lstat(dir); err == nil {
		t.Fatalf("%s is there (from an earlier run?): this test would change it", dir)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	dav := "/dav/iwato-files/" + sub

	// A sandbox as Iwato makes it.
	w := newSandbox("iwato-files")
	sandboxes[w.Name] = w
	if err := o.Start(ctx, w); err != nil {
		t.Fatal("start:", err)
	}
	defer o.Delete(context.Background(), w, true)
	// What the workload makes in its home is what WebDAV shows at its root.
	if got := run(w.Name, `cd "$HOME" && mkdir -p `+sub+`/docs && echo from-sandbox > `+sub+`/docs/in.txt && echo "$HOME"`); got != homeDir {
		t.Fatalf("the workload's home: %q", got)
	}
	body := expect(207, "PROPFIND", "/dav/iwato-files/", "", "Depth", "1")
	if !strings.Contains(body, dav+"/") {
		t.Fatalf("the root does not list the home's %s: %s", sub, body)
	}
	body = expect(207, "PROPFIND", dav+"/docs/", "", "Depth", "1")
	if !strings.Contains(body, dav+"/docs/in.txt") {
		t.Fatalf("listing: %s", body)
	}
	if got := expect(200, "GET", dav+"/docs/in.txt", ""); got != "from-sandbox\n" {
		t.Fatalf("GET: %q", got)
	}
	big := strings.Repeat("0123456789", 100000)
	expect(201, "PUT", dav+"/big.txt", big)
	if got := expect(206, "GET", dav+"/big.txt", "", "Range", "bytes=500000-500009"); got != big[500000:500010] {
		t.Fatalf("Range: %q", got)
	}
	// Replacing a file: SFTP's rename does not overwrite.
	expect(201, "PUT", dav+"/docs/in.txt", "from-webdav")
	if got := run(w.Name, "cat "+dir+"/docs/in.txt; ls -A "+dir+"/docs | grep -c iwato- || true"); got != "from-webdav0" {
		t.Fatalf("the sandbox sees %q", got)
	}
	expect(201, "MKCOL", dav+"/new", "")
	expect(201, "MOVE", dav+"/docs/in.txt", "", "Destination", srv.URL+dav+"/new/moved.txt")
	expect(201, "COPY", dav+"/new/moved.txt", "", "Destination", srv.URL+dav+"/new/copy.txt")
	expect(204, "DELETE", dav+"/docs", "")
	got := run(w.Name, "ls "+dir+"/new; test -e "+dir+"/docs && echo DOCS-LEFT; stat -c %U "+dir+"/new/copy.txt")
	if !strings.HasPrefix(got, "copy.txt\nmoved.txt\n") || strings.Contains(got, "DOCS-LEFT") {
		t.Fatalf("after MOVE/COPY/DELETE: %q", got)
	}
	t.Logf("files written through SFTP are owned by %q", got[strings.LastIndex(got, "\n")+1:])
	// Pasting an image into the terminal (handlePasteImage): the path
	// answered is a file the workload reads, with the same bytes.
	img := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte("iwato-paste"), 30000)...)
	ext, err := pasteImageType(img)
	if err != nil {
		t.Fatal(err)
	}
	pasted, err := pasteImage(ctx, pool, o, w, ext, img, time.Now())
	if err != nil {
		t.Fatal("paste:", err)
	}
	t.Cleanup(func() {
		os.Remove(pasted)
		os.Remove(filepath.Dir(pasted)) // when empty (made by this test)
	})
	if !strings.HasPrefix(pasted, homeDir+"/"+pasteDir+"/") || !strings.HasSuffix(pasted, ".png") {
		t.Fatalf("pasted at %q", pasted)
	}
	sum := sha256.Sum256(img)
	if got := run(w.Name, "test -r "+pasted+" && sha256sum < "+pasted+"; stat -c '%U %a' "+pasted); !strings.HasPrefix(got, hex.EncodeToString(sum[:])+"  -\n") {
		t.Fatalf("the workload reads %s as %q", pasted, got)
	} else {
		t.Logf("pasted %s (%d bytes), owner and mode %q", pasted, len(img), got[strings.LastIndex(got, "\n")+1:])
	}
	lock := `<?xml version="1.0" encoding="utf-8"?><D:lockinfo xmlns:D="DAV:"><D:lockscope><D:exclusive/></D:lockscope><D:locktype><D:write/></D:locktype></D:lockinfo>`
	expect(200, "LOCK", dav+"/new/copy.txt", lock)

	// Stopped: an empty folder with the note, and no changes.
	pool.Close(w.Name)
	if err := o.Stop(ctx, w); err != nil {
		t.Fatal("stop:", err)
	}
	pool.Close(w.Name)
	body = expect(207, "PROPFIND", "/dav/iwato-files/", "", "Depth", "1")
	if strings.Contains(body, sub) || !strings.Contains(body, ".txt") {
		t.Fatalf("stopped listing: %s", body)
	}
	expect(503, "PUT", dav+"/x.txt", "x")
	if err := o.Start(ctx, w); err != nil {
		t.Fatal("start again:", err)
	}
	pool.Close(w.Name)
	expect(200, "GET", dav+"/new/copy.txt", "")

	// The fake wslc's sandboxes share the host's files and network: one at
	// a time (each binds the DNS relay's port 53).
	pool.Close(w.Name)
	if err := o.Delete(ctx, w, true); err != nil {
		t.Fatal("delete:", err)
	}
	// Until the deleted sandbox's relay no longer listens there (a closed
	// connection in TIME_WAIT does not keep the next one from binding).
	for i := 0; dnsRelayBound() && i < 150; i++ {
		time.Sleep(200 * time.Millisecond)
	}

	// read_only: the workspace (the home) read-only in the policy (not the
	// workdir's read-write default, nor the home volume's). The workload
	// must not write it; does SFTP?
	ro := newSandbox("iwato-files-ro")
	sandboxes[ro.Name] = ro
	api, err := o.sandboxes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// The volumes, as Start makes them before it creates a sandbox.
	for _, p := range ro.volumePaths() {
		if err := o.e.volume(ctx, persistVolume(o.e.config, ro, p), map[string]string{sandboxKey: ro.Name, pathKey: p}); err != nil {
			t.Fatal("volume:", err)
		}
	}
	policy := sandboxPolicy(ro)
	policy.Filesystem.IncludeWorkdir = false
	policy.Filesystem.ReadWrite = slices.DeleteFunc(policy.Filesystem.ReadWrite, func(p string) bool { return p == homeDir })
	policy.Filesystem.ReadOnly = append(policy.Filesystem.ReadOnly, homeDir)
	if _, err := api.Create(ctx, openshellWorkspace, ro.Name, &v1.SandboxSpec{
		Template:    &v1.SandboxTemplate{Image: sandboxImage, DriverConfig: map[string]any{"wslc": map[string]any{"mounts": o.sandboxMounts(ro)}}},
		Environment: sandboxEnv(ro),
		Policy:      policy,
		TTY:         true,
	}, map[string]string{"iwato.sandbox": ro.Name}); err != nil {
		t.Fatal("create read-only:", err)
	}
	defer o.Delete(context.Background(), ro, true)
	wait, cancelWait := context.WithTimeout(ctx, 5*time.Minute)
	err = o.waitStarted(wait, api, ro.Name)
	cancelWait()
	if err != nil {
		// Why: the fake wslc keeps the sandbox's output.
		wslc := os.Getenv("OPENSHELL_E2E_WSLC")
		ids, _ := exec.Command(wslc, "container", "list", "--all", "--quiet", "--filter", "label=openshell.ai/sandbox-name="+ro.Name).Output()
		for _, id := range strings.Fields(string(ids)) {
			logs, _ := exec.Command(wslc, "container", "logs", id).CombinedOutput()
			lines := strings.Split(strings.TrimSpace(string(logs)), "\n")
			t.Logf("read_only: sandbox %s output:\n%s", id, strings.Join(lines[max(0, len(lines)-25):], "\n"))
		}
		t.Fatal("start read-only:", err)
	}
	workload := run(ro.Name, "(echo x > "+dir+"/w.txt) 2>/dev/null && echo WROTE || echo DENIED")
	code, _ := do("PUT", "/dav/iwato-files-ro/"+sub+"/s.txt", "via sftp")
	sftpWrote := code == 201 && run(ro.Name, "cat "+dir+"/s.txt 2>/dev/null") == "via sftp"
	t.Logf("read_only: workload write to the home: %s; SFTP (WebDAV PUT %d) write: %v", workload, code, sftpWrote)
	switch {
	case workload == "WROTE":
		t.Log("read_only: inconclusive: Landlock did not stop the workload here")
	case sftpWrote:
		t.Log("read_only: NOT enforced for SFTP (the workload is denied, SFTP writes)")
	default:
		t.Log("read_only: enforced for SFTP too")
	}
}

// dnsRelayBound reports whether a process (a sandbox's DNS relay) still
// listens on TCP port 53 or has UDP port 53 bound.
func dnsRelayBound() bool {
	for _, f := range []string{"/proc/net/tcp", "/proc/net/tcp6", "/proc/net/udp", "/proc/net/udp6"} {
		b, _ := os.ReadFile(f)
		for _, line := range strings.Split(string(b), "\n")[1:] {
			fields := strings.Fields(line)
			if len(fields) < 4 || !strings.HasSuffix(fields[1], ":0035") {
				continue
			}
			if strings.HasPrefix(f, "/proc/net/udp") || fields[3] == "0A" {
				return true
			}
		}
	}
	return false
}
