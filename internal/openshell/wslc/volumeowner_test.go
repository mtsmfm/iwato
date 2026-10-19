package wslc

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func ptr[T any](v T) *T { return &v }

func TestWritableVolumeTargets(t *testing.T) {
	cfg := driverConfig{Mounts: []driverMount{
		{Type: "volume", Source: "a", Target: "/sandbox/work", ReadOnly: ptr(false)},
		{Type: "volume", Source: "b", Target: "/data/", ReadOnly: ptr(false)},
		{Type: "volume", Source: "c", Target: "/ro", ReadOnly: ptr(true)},
		{Type: "volume", Source: "d", Target: "/default"}, // read-only unless said otherwise
		{Type: "tmpfs", Target: "/scratch", ReadOnly: ptr(false)},
	}}
	if got, want := writableVolumeTargets(cfg), []string{"/sandbox/work", "/data"}; !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if got := writableVolumeTargets(driverConfig{}); len(got) != 0 {
		t.Fatal(got)
	}
}

// The script runs under a real sh with stat and chown replaced: stat reports
// root (0) for a path containing "root" and 1000 otherwise.
func TestOwnVolumesScript(t *testing.T) {
	bin, dir := t.TempDir(), t.TempDir()
	log := filepath.Join(bin, "chown.log")
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write("stat", `case "$3" in *root*) echo 0;; *) echo 1000;; esac`)
	write("chown", `echo "$@" >> `+log)

	mk := func(p string) string {
		p = filepath.Join(dir, p)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	rootDir := mk("root-volume")
	mine := mk("mine")
	rootFile := filepath.Join(dir, "root-file")
	if err := os.WriteFile(rootFile, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "root-link")
	if err := os.Symlink(rootDir, link); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "root-missing")

	args := ownVolumesCommand("1000", "1001", []string{rootDir, mine, rootFile, link, missing, filepath.Join(dir, "has space root")})
	mk("has space root")
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Env = []string{"PATH=" + bin + ":/usr/bin:/bin"}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	b, _ := os.ReadFile(log)
	got := strings.Split(strings.TrimSpace(string(b)), "\n")
	want := []string{"1000:1001 " + rootDir, "1000:1001 " + filepath.Join(dir, "has space root")}
	if !slices.Equal(got, want) {
		t.Fatalf("chown calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestOwnVolumesScriptIgnoresFailure(t *testing.T) {
	// No stat on the PATH at all (an odd image): the exec still succeeds.
	cmd := exec.Command("/bin/sh", ownVolumesCommand("1000", "1000", []string{"/nonexistent"})[1:]...)
	cmd.Env = []string{"PATH=/nonexistent"}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
}
