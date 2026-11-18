package main

import (
	"os"
	"testing"
)

func TestDefaultConfigPath(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	home := func() (string, error) { return "/home/u", nil }
	for _, c := range []struct {
		goos string
		env  map[string]string
		want string
	}{
		{"windows", map[string]string{"LOCALAPPDATA": `C:\Users\u\AppData\Local`, "APPDATA": `C:\Users\u\AppData\Roaming`}, `C:\Users\u\AppData\Local`},
		{"linux", nil, "/home/u/.config"},
		{"darwin", map[string]string{"XDG_CONFIG_HOME": "/x"}, "/x"},
		{"linux", map[string]string{"XDG_CONFIG_HOME": "rel"}, "/home/u/.config"},
	} {
		if got, err := userConfigDir(c.goos, env(c.env), home); err != nil || got != c.want {
			t.Errorf("%s %v: %q %v", c.goos, c.env, got, err)
		}
	}
	if _, err := userConfigDir("windows", env(nil), home); err == nil {
		t.Error("Windows without LOCALAPPDATA")
	}
	// iwato.json in the folder started in is kept.
	t.Chdir(t.TempDir())
	if p, _ := defaultConfigPath(); p == "iwato.json" {
		t.Fatalf("no iwato.json here: %q", p)
	}
	if err := os.WriteFile("iwato.json", []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if p, err := defaultConfigPath(); err != nil || p != "iwato.json" {
		t.Fatalf("%q %v", p, err)
	}
}
