//go:build linux

package main

import (
	"os"
	"path/filepath"
	"testing"

	"iwato/internal/agentsmd"
)

func TestWriteAgents(t *testing.T) {
	d := t.TempDir()
	home, link, static := filepath.Join(d, "home"), filepath.Join(d, "AGENTS.md"), filepath.Join(d, "static.md")
	gen := filepath.Join(home, agentsmd.GeneratedRel)
	os.Mkdir(home, 0o700)
	os.WriteFile(static, []byte("v1\n"), 0o644)
	os.Symlink(static, link)
	if err := writeAgents(link, static, gen); err != nil || exists(gen) {
		t.Fatal("wrote for a /AGENTS.md that does not link to it", err)
	}
	os.Remove(link)
	os.Symlink(gen, link)
	if err := writeAgents(link, static, gen); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(link); string(b) != "v1\n" {
		t.Fatalf("%q", b)
	}
	section := "\n" + agentsmd.Marker + "\n## Your URLs\n"
	os.WriteFile(gen, []byte("v1\n"+section), 0o600)
	os.WriteFile(static, []byte("v2\n"), 0o644)
	if err := writeAgents(link, static, gen); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(link); string(b) != "v2\n"+section {
		t.Fatalf("%q", b)
	}
	os.RemoveAll(filepath.Join(home, ".config"))
	os.Mkdir(filepath.Join(d, "other"), 0o700)
	os.Symlink(filepath.Join(d, "other"), filepath.Join(home, ".config"))
	if err := writeAgents(link, static, gen); err == nil {
		t.Fatal("went through a link")
	}
	if exists(filepath.Join(d, "other", "iwato")) {
		t.Fatal("made a directory through a link")
	}
}

func exists(p string) bool { _, err := os.Lstat(p); return err == nil }
