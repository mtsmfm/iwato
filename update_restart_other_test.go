//go:build !windows

package main

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// startFailing runs serve with an unreadable configuration, as the swapped
// exe started by relaunch, and returns the env of the exe it execs (nil: none).
func startFailing(t *testing.T, restored bool) (exe string, env []string) {
	dir := t.TempDir()
	exe = filepath.Join(dir, "iwato")
	cfg := filepath.Join(dir, "iwato.json")
	for f, data := range map[string]string{exe: "new", exe + ".old": "old", cfg: "{"} {
		if err := os.WriteFile(f, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	oldSelf, oldExec := selfExe, execExe
	t.Cleanup(func() { selfExe, execExe = oldSelf, oldExec })
	selfExe = func() (string, error) { return exe, nil }
	execExe = func(_ string, _ []string, e []string) error {
		env = e
		return errors.New("exec stub")
	}
	t.Setenv(restartListenEnv, "127.0.0.1:17699")
	if restored {
		t.Setenv(restartRestoredEnv, "1")
	}
	if err := run([]string{"serve", "-config", cfg}); err == nil {
		t.Fatal("served with a broken configuration")
	}
	return exe, env
}

func TestUpdateRestoresTheOldExeWhenTheNewOneFailsToStart(t *testing.T) {
	exe, env := startFailing(t, false)
	if got, _ := os.ReadFile(exe); string(got) != "old" {
		t.Fatalf("exe %q", got)
	}
	if !slices.Contains(env, restartListenEnv+"=127.0.0.1:17699") || !slices.Contains(env, restartRestoredEnv+"=1") {
		t.Fatalf("exec env %v", env)
	}
}

func TestUpdateRestoredExeDoesNotRollBackAgain(t *testing.T) {
	exe, env := startFailing(t, true)
	got, _ := os.ReadFile(exe)
	old, _ := os.ReadFile(exe + ".old")
	if env != nil || string(got) != "new" || string(old) != "old" {
		t.Fatalf("exec env %v, exe %q, old %q", env, got, old)
	}
}
