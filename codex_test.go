package main

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The terminals write Codex's auth.json with the shell's values, again
// when they change, never over the owner's own sign-in, and send Codex's
// own refresh nowhere.
func TestCodexAuthLines(t *testing.T) {
	home := t.TempDir()
	run := func(access string) string {
		t.Helper()
		cmd := exec.Command("/bin/sh", "-c", codexAuthLines+`printf %s "${CODEX_REFRESH_TOKEN_URL_OVERRIDE-}"`)
		cmd.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), codexOAuthCredentialKey + "=" + access, codexAccountKey + "=acct-ph"}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatal(err, string(out))
		}
		return string(out)
	}
	f := filepath.Join(home, ".codex", "auth.json")
	read := func() map[string]any {
		t.Helper()
		b, err := os.ReadFile(f)
		var m map[string]any
		if err != nil || json.Unmarshal(b, &m) != nil {
			t.Fatal(err, string(b))
		}
		return m
	}
	if out := run("ph-1"); out != "http://127.0.0.1:9/" {
		t.Fatalf("override %q", out)
	}
	m := read()
	tokens, _ := m["tokens"].(map[string]any)
	if m["auth_mode"] != "chatgpt" || m["OPENAI_API_KEY"] != nil || tokens["access_token"] != "ph-1" || tokens["account_id"] != "acct-ph" || tokens["refresh_token"] != codexAuthMarker {
		t.Fatalf("%v", m)
	}
	parts := strings.Split(tokens["id_token"].(string), ".")
	if _, err := base64.RawURLEncoding.DecodeString(parts[1]); len(parts) != 3 || parts[2] == "" || err != nil {
		t.Fatal("id token", parts)
	}
	if st, _ := os.Stat(f); st.Mode().Perm() != 0o600 {
		t.Fatal(st.Mode())
	}
	run("ph-2")
	if read()["tokens"].(map[string]any)["access_token"] != "ph-2" {
		t.Fatal("not written again")
	}
	own := []byte(`{"tokens":{"refresh_token":"mine"}}`)
	os.WriteFile(f, own, 0o600)
	if out := run("ph-3"); out != "" {
		t.Fatalf("override over the owner's sign-in: %q", out)
	}
	if b, _ := os.ReadFile(f); string(b) != string(own) {
		t.Fatal("the owner's auth.json was replaced")
	}
}
