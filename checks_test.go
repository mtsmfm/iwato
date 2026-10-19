package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// checksText is the checks as "level name detail hint params" lines, and
// fails the test when a key has no Japanese or English string.
func checksText(t *testing.T, checks []check) string {
	t.Helper()
	page, err := webFiles.ReadFile("web/i18n.js")
	if err != nil {
		t.Fatal(err)
	}
	ja, en, _ := strings.Cut(string(page), "const en = {")
	var b strings.Builder
	for _, c := range checks {
		keys := []string{"checks.name." + c.Name, "checks.detail." + c.Detail}
		if c.Hint != "" {
			keys = append(keys, "checks.hint."+c.Hint)
		}
		for _, k := range keys {
			if !strings.Contains(ja, "'"+k+"':") || !strings.Contains(en, "'"+k+"':") {
				t.Errorf("%s has no string in web/i18n.js (ja and en)", k)
			}
		}
		fmt.Fprintf(&b, "%s %s %s %s %v\n", c.Level, c.Name, c.Detail, c.Hint, c.Params)
	}
	return b.String()
}

// The checks read WSLc and the configuration, and say what to do about
// each problem.
func TestDiagnosis(t *testing.T) {
	e := &Engine{config: testConfig()}
	e.run = func(_ context.Context, a ...string) ([]byte, error) {
		switch strings.Join(a, " ") {
		case "--version":
			return []byte("wslc 3.0.1\n"), nil
		}
		return []byte("[]"), errors.New("not found")
	}
	got := checksText(t, diagnose(context.Background(), e, t.TempDir()+"/iwato.json", 1))
	for _, want := range []string{"ok wslc version  map[version:wslc 3.0.1]", "ok config config  map[count:1 path:", "ok openshell version"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in\n%s", want, got)
		}
	}
	e.run = func(context.Context, ...string) ([]byte, error) { return nil, errors.New("exec: wslc.exe: not found") }
	checks := diagnose(context.Background(), e, "iwato.json", 1)
	if down := engineDown(checks); down == nil || len(checks) != 1 || !strings.Contains(checksText(t, checks), "fail wslc wslc_missing wslc_install map[error:exec: wslc.exe: not found]") {
		t.Fatal(checksText(t, checks))
	}
}

// On a Docker engine the first check is a running engine (serve waits for
// one); then its VM and the settings the Docker driver needs.
func TestDiagnosisDocker(t *testing.T) {
	e := &Engine{config: testConfig(), docker: true}
	server := ""
	e.run = func(_ context.Context, a ...string) ([]byte, error) {
		switch strings.Join(a, " ") {
		case "version --format {{json .}}":
			if server == "" {
				return []byte(`{"Client":{"Version":"28.3.0","Context":"colima"},"Server":null}`), errors.New("Cannot connect to the Docker daemon")
			}
			return []byte(`{"Client":{"Version":"28.3.0","Context":"colima"},"Server":{"Version":"` + server + `","APIVersion":"1.51"}}`), nil
		case "info --format {{json .}}":
			return []byte(`{"OperatingSystem":"Docker Desktop","Architecture":"riscv64","NCPU":4,"MemTotal":8589934592,"DefaultRuntime":"sysbox-runc"}`), nil
		}
		return []byte("[]"), errors.New("not found")
	}
	checks := diagnose(context.Background(), e, t.TempDir()+"/iwato.json", 1)
	if got := checksText(t, checks); engineDown(checks) == nil || !strings.Contains(got, "fail docker docker_down_error docker_start map[error:Cannot connect to the Docker daemon]") {
		t.Fatal(got)
	}
	server = "27.5.1"
	got := checksText(t, diagnose(context.Background(), e, t.TempDir()+"/iwato.json", 1))
	for _, want := range []string{"warn docker docker_version_context docker_old map[api:1.51 context:colima min:28 version:27.5.1]", "warn engine engine_vm engine_arch map[arch:riscv64 cpus:4 memory:8.0 os:Docker Desktop", "fail engine_config eci eci map[runtime:sysbox-runc]", "warn engine_config docker_desktop host_networking", "ok config config"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in\n%s", want, got)
		}
	}
}
