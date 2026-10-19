package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// run runs this package's main by re-executing the test binary.
func run(t *testing.T, state string, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(os.Args[0], append([]string{"-test.run=TestMain"}, args...)...)
	cmd.Env = append(os.Environ(), "FAKEWSLC_AS_MAIN=1", "FAKE_WSLC_STATE="+state)
	var out, errb strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	code := 0
	if e, ok := err.(*exec.ExitError); ok {
		code = e.ExitCode()
	}
	return out.String(), errb.String(), code
}

func TestMain(m *testing.M) {
	if os.Getenv("FAKEWSLC_AS_MAIN") == "1" {
		args := []string{os.Args[0]}
		for _, a := range os.Args[1:] {
			if !strings.HasPrefix(a, "-test.") {
				args = append(args, a)
			}
		}
		os.Args = args
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestRefusesWhatWSLcRefuses(t *testing.T) {
	state := t.TempDir()
	for _, args := range [][]string{
		{"container", "create", "--cap-drop", "ALL", "img"},
		{"container", "create", "--security-opt", "no-new-privileges", "img"},
		{"container", "create", "--privileged", "img"},
		{"container", "create", "--read-only", "img"},
		{"container", "create", "--pids-limit", "100", "img"},
		{"container", "create", "--network", "host", "img"},
		{"container", "create", "--pull", "never", "missing:1"},
	} {
		if _, _, code := run(t, state, args...); code == 0 {
			t.Errorf("accepted %v", args)
		}
	}
}

func TestImagesExistOncePulled(t *testing.T) {
	state := t.TempDir()
	if out, _, code := run(t, state, "image", "inspect", "--format", "json", "a:1"); code != 1 || strings.TrimSpace(out) != "[]" {
		t.Fatalf("%q %d", out, code)
	}
	run(t, state, "image", "pull", "a:1")
	if _, _, code := run(t, state, "container", "create", "--pull", "never", "--network", "none", "a:1"); code != 0 {
		t.Fatal("a pulled image is missing")
	}
}

func TestMissingContainers(t *testing.T) {
	state := t.TempDir()
	if out, _, code := run(t, state, "container", "inspect", "--format", "json", "some-name"); code != 1 || strings.TrimSpace(out) != "[]" {
		t.Fatalf("by name: %q %d", out, code)
	}
	if _, errOut, code := run(t, state, "container", "inspect", "--format", "json", strings.Repeat("ab", 32)); code != 1 || !strings.Contains(errOut, "No such container") {
		t.Fatalf("by id: %q %d", errOut, code)
	}
	if _, _, code := run(t, state, "container", "remove", "--force", "gone"); code != 0 {
		t.Fatal("remove --force of a missing container failed")
	}
	if _, err := os.Stat(filepath.Join(state, "calls.log")); err != nil {
		t.Fatal(err)
	}
}

func TestVolumeInspectSeveral(t *testing.T) {
	state := t.TempDir()
	run(t, state, "volume", "create", "--label", "a=/x/y", "v1")
	run(t, state, "volume", "create", "v2")
	if out, _, _ := run(t, state, "volume", "list", "--quiet", "--filter", "label=a=/x/y"); strings.TrimSpace(out) != "v1" {
		t.Fatalf("list by a label with slashes: %q", out)
	}
	out, _, code := run(t, state, "volume", "inspect", "--format", "json", "v1", "v2")
	if code != 0 || !strings.Contains(out, `"a":"/x/y"`) || !strings.Contains(out, `"Name":"v2"`) || !strings.Contains(out, `"CreatedAt":"20`) {
		t.Fatalf("inspect: %d %s", code, out)
	}
	if out, _, code = run(t, state, "volume", "inspect", "--format", "json", "v1", "gone"); code != 1 || !strings.Contains(out, `"Name":"v1"`) {
		t.Fatalf("inspect with a missing one: %d %s", code, out)
	}
}
