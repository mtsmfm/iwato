package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// imageRunner answers wslc image inspect from ids (ref → ID; absent: none).
type imageRunner map[string]string

func (r imageRunner) Run(_ context.Context, _ []byte, args ...string) ([]byte, error) {
	ref := args[len(args)-1]
	if id, ok := r[ref]; ok {
		return []byte(fmt.Sprintf(`[{"Id":%q}]`, id)), nil
	}
	return []byte("[]"), nil
}

func TestRecreateToUpdate(t *testing.T) {
	refs := runtimeImages{"sup:new", "sbx:new"}
	for _, c := range []struct {
		name    string
		docker  bool
		created map[string]string // state file → ID
		local   imageRunner
		want    bool
	}{
		{"same", false, map[string]string{"supervisor-image": "sha256:a", "sandbox-image": "sha256:b"}, imageRunner{"sup:new": "sha256:a", "sbx:new": "sha256:b"}, false},
		{"supervisor changed", false, map[string]string{"supervisor-image": "sha256:old", "sandbox-image": "sha256:b"}, imageRunner{"sup:new": "sha256:a", "sbx:new": "sha256:b"}, true},
		{"sandbox runtime changed", false, map[string]string{"supervisor-image": "sha256:a", "sandbox-image": "sha256:old"}, imageRunner{"sup:new": "sha256:a", "sbx:new": "sha256:b"}, true},
		// Made before the sandbox runtime was recorded.
		{"supervisor only", false, map[string]string{"supervisor-image": "sha256:old"}, imageRunner{"sup:new": "sha256:a"}, true},
		{"nothing recorded", false, nil, imageRunner{"sup:new": "sha256:a", "sbx:new": "sha256:b"}, false},
		{"current not pulled", false, map[string]string{"supervisor-image": "sha256:old"}, imageRunner{}, false},
		{"docker", true, map[string]string{"supervisor-image": "sha256:old"}, imageRunner{"sup:new": "sha256:a"}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			state := filepath.Join(dir, "sandboxes", "personal", "sb-1")
			if err := os.MkdirAll(state, 0o700); err != nil {
				t.Fatal(err)
			}
			for name, id := range c.created {
				if err := os.WriteFile(filepath.Join(state, name), []byte(id), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			o := &OpenShell{e: &Engine{config: Config{Namespace: "personal"}, docker: c.docker}, dir: dir, runner: c.local}
			o.runtime.setRefs(refs)
			if got := o.recreateToUpdate(context.Background(), "sb-1"); got != c.want {
				t.Fatalf("recreateToUpdate = %v, want %v", got, c.want)
			}
		})
	}
}
