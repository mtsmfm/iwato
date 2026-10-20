package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestNewerBuild(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"0.2.0", "0.1.9", true}, {"0.10.0", "0.9.0", true}, {"1.0.0", "1.0.0", false}, {"0.1.0", "0.2.0", false},
		{"1.0.0", "1.0.0-rc.1", true}, {"1.0.0-rc.1", "1.0.0", false}, {"1.0.0-rc.2", "1.0.0-rc.1", true}, {"bad", "0.1.0", false},
	} {
		if got := newerVersion(c.a, c.b); got != c.want {
			t.Errorf("newerVersion(%s, %s) = %v", c.a, c.b, got)
		}
	}
	m := buildMeta{Version: "0.1.0", Commit: "abcdef0" + strings.Repeat("1", 33)}
	if newerBuild("edge", m, BuildInfo{Version: "0.1.0", Commit: "abcdef0"}) || !newerBuild("edge", m, BuildInfo{Version: "0.1.0", Commit: "1234567"}) {
		t.Error("edge compares the commit")
	}
	if newerBuild("release", m, BuildInfo{Version: "0.1.0", Commit: "1234567"}) {
		t.Error("release compares the version")
	}
	if assetName("windows", "arm64") != "iwato-windows-arm64.exe" || assetName("darwin", "amd64") != "" {
		t.Error("assetName")
	}
}

// fakeRelease serves iwato-build.json and this platform's binary, a script
// that answers `version` as an Iwato of commit.
func fakeRelease(t *testing.T, commit string, tamper bool) (*httptest.Server, []byte) {
	asset := assetName(runtime.GOOS, runtime.GOARCH)
	bin := []byte("#!/bin/sh\necho iwato 0.1.0 " + commit[:7] + " 2026-10-20T00:00:00Z\n")
	sum := sha256.Sum256(bin)
	meta, _ := json.Marshal(buildMeta{Version: "0.1.0", Commit: commit, Channel: "edge",
		Images: map[string]string{"iwato-gateway": "sha256:" + strings.Repeat("2", 64)},
		Assets: map[string]string{asset: hex.EncodeToString(sum[:])}})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/releases/download/edge/iwato-build.json":
			w.Write(meta)
		case "/releases/download/edge/" + asset:
			if tamper {
				w.Write(append(bin, '#'))
				return
			}
			w.Write(bin)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, bin
}

func testUpdate(t *testing.T, srv *httptest.Server) *selfUpdate {
	base, channel, repo := updateBaseOverride, buildChannel, buildRepo
	t.Cleanup(func() { updateBaseOverride, buildChannel, buildRepo = base, channel, repo })
	updateBaseOverride, buildChannel, buildRepo = srv.URL+"/releases/", "edge", "o/r"
	u := newSelfUpdate()
	u.current = BuildInfo{Version: "0.1.0", Commit: "1111111"}
	u.exe = filepath.Join(t.TempDir(), "iwato")
	if err := os.WriteFile(u.exe, []byte("old"), 0o750); err != nil {
		t.Fatal(err)
	}
	return u
}

func waitPhase(t *testing.T, u *selfUpdate) UpdateView {
	for range 200 {
		if v := u.view(); v.Phase != "downloading" {
			return v
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("still downloading")
	return UpdateView{}
}

// Download, hash, the version the new binary reports, and the swap that
// keeps the old one as .old.
func TestUpdateApply(t *testing.T) {
	if runtime.GOOS == "windows" || assetName(runtime.GOOS, runtime.GOARCH) == "" {
		t.Skip("the fake binary is a shell script")
	}
	commit := "2222222" + strings.Repeat("0", 33)
	srv, bin := fakeRelease(t, commit, false)
	u := testUpdate(t, srv)
	restarted := make(chan struct{})
	u.restart = func() { close(restarted) }
	if err := u.check(context.Background()); err != nil || u.view().Latest == nil {
		t.Fatal(err, u.view())
	}
	if err := u.start(context.Background(), "3"+commit[1:]); err == nil {
		t.Fatal("applied another commit than the one confirmed")
	}
	if err := u.start(context.Background(), commit); err != nil {
		t.Fatal(err)
	}
	if v := waitPhase(t, u); v.Phase != "restarting" || u.swappedExe() != u.exe {
		t.Fatalf("%+v", v)
	}
	<-restarted
	got, _ := os.ReadFile(u.exe)
	old, _ := os.ReadFile(u.exe + ".old")
	st, _ := os.Stat(u.exe)
	if string(got) != string(bin) || string(old) != "old" || st.Mode().Perm() != 0o750 {
		t.Fatalf("exe %q, old %q, mode %v", got, old, st.Mode())
	}
	if err := restoreExe(u.exe); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(u.exe); string(got) != "old" {
		t.Fatal("not restored")
	}
}

func TestUpdateRefusesAWrongHash(t *testing.T) {
	if assetName(runtime.GOOS, runtime.GOARCH) == "" {
		t.Skip()
	}
	commit := "2222222" + strings.Repeat("0", 33)
	srv, _ := fakeRelease(t, commit, true)
	u := testUpdate(t, srv)
	u.restart = func() { t.Error("restarted") }
	if err := u.check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := u.start(context.Background(), commit); err != nil {
		t.Fatal(err)
	}
	if v := waitPhase(t, u); v.Phase != "failed" || v.Code != "update_hash" {
		t.Fatalf("%+v", v)
	}
	entries, _ := os.ReadDir(filepath.Dir(u.exe))
	if got, _ := os.ReadFile(u.exe); string(got) != "old" || len(entries) != 1 {
		t.Fatalf("exe %q, files %v", got, entries)
	}
}
