package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// runAgentFiles runs agentFilesScript with HOME=home and /AGENTS.md at agents.
func runAgentFiles(t *testing.T, home, agents string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("needs /bin/sh")
	}
	// exportHome would replace HOME with the real home volume inside a sandbox.
	cmd := exec.Command("/bin/sh", "-c", strings.TrimPrefix(agentFilesScript, exportHome), "iwato-agent-files", agents)
	cmd.Env = []string{"HOME=" + home, "PATH=/usr/bin:/bin"}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("script: %v %s", err, out)
	}
	return string(out)
}

func agentsFile(t *testing.T) string {
	t.Helper()
	a := filepath.Join(t.TempDir(), "AGENTS.md")
	if err := os.WriteFile(a, []byte("# env\n"), 0o444); err != nil {
		t.Fatal(err)
	}
	return a
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestAgentFilesPlacedWhenAbsent(t *testing.T) {
	home, a := t.TempDir(), agentsFile(t)
	out := runAgentFiles(t, home, a)
	if got := readFile(t, filepath.Join(home, ".claude", "CLAUDE.md")); got != "@"+a+"\n" {
		t.Fatalf("CLAUDE.md = %q", got)
	}
	if l, err := os.Readlink(filepath.Join(home, ".codex", "AGENTS.md")); err != nil || l != a {
		t.Fatalf("codex link = %q %v", l, err)
	}
	if fi, err := os.Stat(filepath.Join(home, ".claude")); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf(".claude: %v %v", fi.Mode(), err)
	}
	if out != "placed ~/.claude/CLAUDE.md\nplaced ~/.codex/AGENTS.md\n" {
		t.Fatalf("output %q", out)
	}
	// A second run (the next start) changes nothing and says nothing.
	if out := runAgentFiles(t, home, a); out != "" {
		t.Fatalf("second run: %q", out)
	}
}

func TestAgentFilesKeepTheUsersOwn(t *testing.T) {
	home, a := t.TempDir(), agentsFile(t)
	// The user's own files, in directories kept on a volume.
	for _, d := range []string{".claude", ".codex"} {
		if err := os.Mkdir(filepath.Join(home, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	mine := filepath.Join(home, ".claude", "CLAUDE.md")
	os.WriteFile(mine, []byte("mine\n"), 0o600)
	// A dangling link counts as a file.
	if err := os.Symlink(filepath.Join(home, "elsewhere"), filepath.Join(home, ".codex", "AGENTS.md")); err != nil {
		t.Fatal(err)
	}
	if out := runAgentFiles(t, home, a); out != "" {
		t.Fatalf("output %q", out)
	}
	if got := readFile(t, mine); got != "mine\n" {
		t.Fatalf("CLAUDE.md = %q", got)
	}
	if _, err := os.Lstat(filepath.Join(home, "elsewhere")); !os.IsNotExist(err) {
		t.Fatal("wrote through the link:", err)
	}
	if fi, _ := os.Stat(filepath.Join(home, ".claude")); fi.Mode().Perm() != 0o755 {
		t.Fatalf("changed .claude's mode: %v", fi.Mode())
	}
}

func TestAgentFilesDoNotFollowLinkedDirectories(t *testing.T) {
	home, a, other := t.TempDir(), agentsFile(t), t.TempDir()
	os.Symlink(other, filepath.Join(home, ".claude"))
	os.Symlink(other, filepath.Join(home, ".codex"))
	if out := runAgentFiles(t, home, a); out != "" {
		t.Fatalf("output %q", out)
	}
	if es, _ := os.ReadDir(other); len(es) != 0 {
		t.Fatalf("wrote through a linked directory: %v", es)
	}
}

func TestAgentFilesSkipWhatDoesNotFit(t *testing.T) {
	a := agentsFile(t)
	// No /AGENTS.md: nothing to point at.
	home := t.TempDir()
	if out := runAgentFiles(t, home, filepath.Join(home, "missing")); out != "" {
		t.Fatalf("output %q", out)
	}
	if es, _ := os.ReadDir(home); len(es) != 0 {
		t.Fatalf("placed files without /AGENTS.md: %v", es)
	}
	// A home that is missing, relative or a link.
	for _, h := range []string{filepath.Join(home, "none"), "rel", ""} {
		if out := runAgentFiles(t, h, a); out != "" {
			t.Fatalf("home %q: %q", h, out)
		}
	}
	link := filepath.Join(t.TempDir(), "home")
	os.Symlink(home, link)
	if out := runAgentFiles(t, link, a); out != "" {
		t.Fatalf("linked home: %q", out)
	}
	// A file in the way of a directory.
	os.WriteFile(filepath.Join(home, ".claude"), nil, 0o600)
	if out := runAgentFiles(t, home, a); out != "placed ~/.codex/AGENTS.md\n" {
		t.Fatalf("output %q", out)
	}
}
