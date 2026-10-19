package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultAndStrictConfig(t *testing.T) {
	c := testConfig()
	if e := c.Validate(); e != nil {
		t.Fatal(e)
	}
	for _, id := range []string{"../x", "-option", "a b", "a;cmd", "日本語", "dev/../../x", "a\x00b"} {
		x := c
		x.Sandboxes = append([]Sandbox(nil), c.Sandboxes...)
		x.Sandboxes[0].Name = id
		if x.Validate() == nil {
			t.Errorf("accepted ID %q", id)
		}
	}
	d := t.TempDir()
	p := filepath.Join(d, "c.json")
	b, _ := json.Marshal(c)
	for _, suffix := range []string{"{}", "oops"} {
		os.WriteFile(p, append(b, []byte(suffix)...), 0600)
		if _, e := readConfig(p); e == nil {
			t.Errorf("accepted trailing %q", suffix)
		}
	}
	os.WriteFile(p, []byte(strings.Replace(string(b), `"version":1`, `"version":1,"unknown":true`, 1)), 0600)
	if _, e := readConfig(p); e == nil {
		t.Fatal("unknown field accepted")
	}
}

func TestNamesAreIwatosAndPerSandbox(t *testing.T) {
	c := testConfig()
	a, b := c.Sandboxes[0], c.Sandboxes[0]
	b.Name = "other"
	if c.resourceName(a, "persist-1") == c.resourceName(b, "persist-1") || !strings.HasPrefix(c.resourceName(a, "x"), "iwato-personal-dev-") {
		t.Fatal(c.resourceName(a, "persist-1"), c.resourceName(b, "persist-1"))
	}
}

func TestEnsureConfigNeverOverwrites(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "iwato.json")
	created, e := ensureConfig(p)
	if e != nil || !created {
		t.Fatal(created, e)
	}
	before, _ := os.ReadFile(p)
	c, e := readConfig(p)
	if e != nil || len(c.Sandboxes) != 0 {
		t.Fatal("new config must be valid and have no sandboxes", e)
	}
	if created, e = ensureConfig(p); e != nil || created {
		t.Fatal("existing config replaced", created, e)
	}
	after, _ := os.ReadFile(p)
	if string(after) != string(before) {
		t.Fatal("changed existing config")
	}
}

func TestImageContextMatchesEmbeddedSources(t *testing.T) {
	d := t.TempDir()
	if e := writeImageContext(d); e != nil {
		t.Fatal(e)
	}
	for _, p := range []string{"Dockerfile", "install-tools.sh", "AGENTS.md", "etc/shell/rc", "etc/gitconfig", "etc/iwato-browser"} {
		if _, e := os.Stat(filepath.Join(d, "sandboximage", filepath.FromSlash(p))); e != nil {
			t.Errorf("missing %s", p)
		}
	}
}

func TestPersistPathsAreMountable(t *testing.T) {
	for _, ok := range [][]string{nil, {"/work"}, {"/work", "/sandbox/data"}} {
		if err := validPersist(ok); err != nil {
			t.Errorf("%v: %v", ok, err)
		}
	}
	for _, bad := range [][]string{{"work"}, {"/"}, {"/sandbox"}, {"/work/"}, {"/a/../b"}, {"/etc/openshell/x"}, {"/a,b"}, {"/work", "/work/sub"}, {"/x", "/x"}, {"/home/dev"}, {"/home/dev/.claude"}, {"/home"}} {
		if validPersist(bad) == nil {
			t.Errorf("%v was accepted", bad)
		}
	}
}

// Only the home persists: a new sandbox has the home alone. A configuration
// from before the image convention still loads its persisted paths
// (persist), which the sandbox keeps (Start's automatic re-creation of a
// failed one mounts them too) until the owner recreates it.
func TestOnlyTheHomePersists(t *testing.T) {
	if got := newSandbox("new").volumePaths(); strings.Join(got, ",") != "/home/dev" {
		t.Errorf("a new sandbox's volumePaths = %v", got)
	}
	if env := sandboxEnv(newSandbox("new")); env["HOME"] != "/home/dev" || env["IWATO_PERSIST"] != "/home/dev" {
		t.Errorf("a new sandbox's env = %v", env)
	}
	var w Sandbox
	if err := json.Unmarshal([]byte(`{"name":"dev","persist":["/work"]}`), &w); err != nil {
		t.Fatal(err)
	}
	if got := w.volumePaths(); strings.Join(got, ",") != "/home/dev,/work" {
		t.Errorf("an older sandbox's volumePaths = %v", got)
	}
	c := testConfig()
	c.Sandboxes = []Sandbox{w}
	if err := c.Validate(); err != nil {
		t.Errorf("a configuration with persisted paths: %v", err)
	}
}

// An extra terminal gets the home volume when the sandbox has one, and keeps
// the sandbox's own home when it does not (one created before the home was a
// volume).
func TestExportHomeLooksAtTheSandbox(t *testing.T) {
	run := func(script string) string {
		cmd := exec.Command("/bin/sh", "-c", script+`printf %s "$HOME"`)
		cmd.Env = []string{"HOME=/old"}
		out, err := cmd.Output()
		if err != nil {
			t.Fatal(err)
		}
		return string(out)
	}
	if got := run(strings.Replace(exportHome, homeDir, "/no/such/dir", 1)); got != "/old" {
		t.Errorf("without a home volume: HOME = %q", got)
	}
	dir := t.TempDir()
	if got := run(strings.Replace(exportHome, homeDir, dir, 1)); got != dir {
		t.Errorf("with a home volume: HOME = %q, want %q", got, dir)
	}
}
