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
	srv, bins := fakeReleases(t, map[string]string{"edge": commit}, tamper)
	return srv, bins["edge"]
}

// fakeReleases serves the releases tag -> commit (tags pr-N are pull
// requests' builds) and the list of GitHub's API.
func fakeReleases(t *testing.T, commits map[string]string, tamper bool) (*httptest.Server, map[string][]byte) {
	asset := assetName(runtime.GOOS, runtime.GOARCH)
	files, bins := map[string][]byte{}, map[string][]byte{}
	type release struct {
		Tag        string           `json:"tag_name"`
		Prerelease bool             `json:"prerelease"`
		Assets     []map[string]any `json:"assets"`
	}
	list := []release{{Tag: "v0.0.9", Assets: []map[string]any{{"name": updateMetaName}}}}
	for tag, commit := range commits {
		bin := []byte("#!/bin/sh\necho iwato 0.1.0 " + commit[:7] + " 2026-10-20T00:00:00Z\n")
		sum := sha256.Sum256(bin)
		m := buildMeta{Version: "0.1.0", Commit: commit, Channel: tag, PR: prOfTag(tag),
			Images: map[string]string{"iwato-gateway": "sha256:" + strings.Repeat("2", 64)},
			Assets: map[string]string{asset: hex.EncodeToString(sum[:])}}
		if m.PR > 0 {
			m.Channel, m.Title = "pr", "PR "+tag
		}
		meta, _ := json.Marshal(m)
		files["/releases/download/"+tag+"/"+updateMetaName] = meta
		if tamper {
			files["/releases/download/"+tag+"/"+asset] = append(bin, '#')
		} else {
			files["/releases/download/"+tag+"/"+asset] = bin
		}
		bins[tag] = bin
		list = append(list, release{Tag: tag, Prerelease: true, Assets: []map[string]any{{"name": asset}, {"name": updateMetaName}}})
	}
	// Not listed: one without metadata.
	list = append(list, release{Tag: "pr-99", Prerelease: true, Assets: []map[string]any{{"name": asset}}})
	files["/releases/list"], _ = json.Marshal(list)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if b, ok := files[r.URL.Path]; ok {
			w.Write(b)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, bins
}

func testUpdate(t *testing.T, srv *httptest.Server) *selfUpdate {
	return testUpdateOn(t, srv, "edge")
}

func testUpdateOn(t *testing.T, srv *httptest.Server, channel string) *selfUpdate {
	base, was, repo := updateBaseOverride, buildChannel, buildRepo
	t.Cleanup(func() { updateBaseOverride, buildChannel, buildRepo = base, was, repo })
	updateBaseOverride, buildChannel, buildRepo = srv.URL+"/releases/", channel, "o/r"
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

func TestBuildMetaPR(t *testing.T) {
	m := buildMeta{Version: "0.1.0", Commit: strings.Repeat("a", 40), Channel: "pr", PR: 7, Title: "x"}
	if m.check("pr-7") != nil || m.check("pr-8") == nil || m.check("edge") == nil {
		t.Error("a pull request's build matches its own tag only")
	}
	m.Channel = "edge"
	if m.check("edge") == nil {
		t.Error("edge with a pull request")
	}
	if prOfTag("pr-0") != 0 || prOfTag("pr-07") != 0 || prOfTag("pr-12") != 12 || prOfTag("edge") != 0 {
		t.Error("prOfTag")
	}
}

// A pull request's build offers its own newer pushes only, and goes back
// to edge on request even though edge is not newer.
func TestUpdatePRChannel(t *testing.T) {
	if runtime.GOOS == "windows" || assetName(runtime.GOOS, runtime.GOARCH) == "" {
		t.Skip("the fake binary is a shell script")
	}
	edge, pr7, pr3 := "2222222"+strings.Repeat("0", 33), "7777777"+strings.Repeat("0", 33), "3333333"+strings.Repeat("0", 33)
	srv, bins := fakeReleases(t, map[string]string{"edge": edge, "pr-7": pr7, "pr-3": pr3}, false)
	u := testUpdateOn(t, srv, "pr-7")
	if u.channel != "pr" || u.pr != 7 {
		t.Fatalf("channel %q %d", u.channel, u.pr)
	}
	prs, err := u.list(context.Background())
	if err != nil || len(prs) != 2 || prs[0].PR != 7 || prs[1].PR != 3 || prs[1].Commit != pr3 {
		t.Fatalf("%v %+v", err, prs)
	}
	if err := u.check(context.Background()); err != nil {
		t.Fatal(err)
	}
	v := u.view()
	if v.Latest == nil || v.Latest.Commit != pr7 || v.Edge == nil || v.Edge.Commit != edge || !v.Switch || v.Current.PR != 7 {
		t.Fatalf("%+v", v)
	}
	u.current.Commit = pr7[:7]
	_ = u.check(context.Background())
	if u.view().Latest != nil {
		t.Fatal("edge offered as an update of a pull request's build")
	}
	restarted := make(chan struct{})
	u.restart = func() { close(restarted) }
	if err := u.switchTo(context.Background(), "edge", pr3); err != nil {
		t.Fatal(err)
	}
	if v := waitPhase(t, u); v.Code != "update_changed" {
		t.Fatalf("installed another commit than the one confirmed: %+v", v)
	}
	if err := u.switchTo(context.Background(), "edge", edge); err != nil {
		t.Fatal(err)
	}
	if v := waitPhase(t, u); v.Phase != "restarting" {
		t.Fatalf("%+v", v)
	}
	<-restarted
	if got, _ := os.ReadFile(u.exe); string(got) != string(bins["edge"]) {
		t.Fatalf("exe %q", got)
	}
}

func TestUpdatePRGone(t *testing.T) {
	srv, _ := fakeReleases(t, map[string]string{"edge": strings.Repeat("2", 40)}, false)
	u := testUpdateOn(t, srv, "pr-5")
	if err := u.check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if v := u.view(); !v.Gone || v.Edge == nil || v.CheckFailed != "" {
		t.Fatalf("%+v", v)
	}
	r := testUpdateOn(t, srv, "release")
	if err := r.switchTo(context.Background(), "edge", strings.Repeat("2", 40)); err == nil {
		t.Error("a release build switched")
	}
}
