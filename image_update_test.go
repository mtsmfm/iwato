package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeImageEngine is WSLc with one standard image: inspect answers with
// the ID in *id ("" for none), and each `image pull` (a script, as stream
// runs the exe itself) is logged and makes the image next.
func fakeImageEngine(t *testing.T, id *string, next string) (e *Engine, pulls func() int) {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "pulls")
	script := filepath.Join(dir, "wslc")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho \"$*\" >> "+log+"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	pulls = func() int {
		data, _ := os.ReadFile(log)
		return strings.Count(string(data), "image pull")
	}
	seen := 0
	e = &Engine{config: testConfig(), exe: script}
	e.run = func(_ context.Context, a ...string) ([]byte, error) {
		if n := pulls(); n > seen {
			seen, *id = n, next
		}
		if len(a) > 1 && a[0] == "image" && a[1] == "inspect" && *id != "" {
			return []byte(`[{"Id":"` + *id + `","Created":"2026-10-15T03:04:05.123456789Z"}]`), nil
		}
		return []byte("[]"), errors.New("not found")
	}
	return e, pulls
}

func pullEdge(t *testing.T) {
	r, tag := imageRegistry, imageTag
	imageRegistry, imageTag = "ghcr.io/mtsmfm", "edge"
	t.Cleanup(func() { imageRegistry, imageTag = r, tag })
}

// A pulled image (edge) here is used as it is at startup: only a missing
// one is pulled.
func TestEnsureImagePullsOnlyAMissingImage(t *testing.T) {
	pullEdge(t)
	idA, idB := "sha256:"+strings.Repeat("a", 64), "sha256:"+strings.Repeat("b", 64)
	id := idA
	e, pulls := fakeImageEngine(t, &id, idB)
	dir := t.TempDir()
	if err := e.EnsureImage(context.Background(), dir, func(ImageState) {}); err != nil || pulls() != 0 {
		t.Fatal("pulled at startup with a local image:", err, pulls())
	}
	id = ""
	if err := e.EnsureImage(context.Background(), dir, func(ImageState) {}); err != nil || pulls() != 1 {
		t.Fatal("missing image not pulled:", err, pulls())
	}
	if info := e.ImageInfo(context.Background(), dir); info.At == "" || !info.Present || info.Created == "" {
		t.Fatalf("pull time not recorded: %+v", info)
	}
}
