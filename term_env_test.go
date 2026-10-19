package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The file Iwato writes, read by the image's env.sh: grok's command exactly
// when XAI_OAUTH_TOKEN is set and the command is not; rewritten only when
// it differs; never written through or read from a link.
func TestTermEnvFile(t *testing.T) {
	script := strings.TrimPrefix(termEnvScript, exportHome) // not the real /home/dev
	envSh, err := imageFiles.ReadFile("sandboximage/etc/env.sh")
	if err != nil {
		t.Fatal(err)
	}
	run := func(home, script string, env []string, args ...string) (string, error) {
		cmd := exec.Command("/bin/sh", append([]string{"-c", script, "test"}, args...)...)
		cmd.Env = append([]string{"HOME=" + home, "PATH=" + os.Getenv("PATH")}, env...)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	home := t.TempDir()
	if out, err := run(home, script, nil, termEnvContent); err != nil || out != "" {
		t.Fatal(out, err)
	}
	f := filepath.Join(home, termEnvPath)
	fi, err := os.Stat(f)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatal(fi, err)
	}
	show := string(envSh) + `printf %s "${GROK_AUTH_PROVIDER_COMMAND-}"`
	for _, c := range []struct {
		env  []string
		want string
	}{
		{nil, ""},
		{[]string{"XAI_OAUTH_TOKEN=tok"}, grokAuthCommand},
		{[]string{"XAI_OAUTH_TOKEN=tok", "GROK_AUTH_PROVIDER_COMMAND=mine"}, "mine"},
	} {
		if out, err := run(home, show, c.env); err != nil || out != c.want {
			t.Errorf("%v: %q %v", c.env, out, err)
		}
	}
	// The same content is not written again.
	before, _ := os.Stat(f)
	if out, err := run(home, script, nil, termEnvContent); err != nil || out != "" {
		t.Fatal(out, err)
	}
	if after, _ := os.Stat(f); !os.SameFile(before, after) {
		t.Error("rewrote the same content")
	}
	if _, err := run(home, script, nil, termEnvContent+"# more\n"); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.Stat(f); os.SameFile(before, after) {
		t.Error("kept a different content")
	}
	// A link in the way: nothing is written through it, nothing is read.
	other, linked := t.TempDir(), t.TempDir()
	os.MkdirAll(filepath.Join(linked, ".config"), 0o700)
	os.Symlink(other, filepath.Join(linked, ".config", "iwato"))
	if _, err := run(linked, script, nil, termEnvContent); err == nil {
		t.Fatal("wrote through a link")
	}
	if entries, _ := os.ReadDir(other); len(entries) != 0 {
		t.Fatal("wrote through a link:", entries)
	}
	target := filepath.Join(other, "env")
	os.WriteFile(target, []byte("export A=b\n"), 0o600)
	os.Remove(filepath.Join(linked, ".config", "iwato"))
	os.MkdirAll(filepath.Join(linked, ".config", "iwato"), 0o700)
	os.Symlink(target, filepath.Join(linked, termEnvPath))
	if out, _ := run(linked, string(envSh)+`printf %s "${A-}"`, nil); out != "" {
		t.Fatal("read a linked file")
	}
}
