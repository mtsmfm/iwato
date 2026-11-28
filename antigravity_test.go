package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// The terminals write agy's token file with the shell's placeholder, again
// when it changes, never over the owner's own sign-in or through a link.
func TestAntigravityTokenLines(t *testing.T) {
	home := t.TempDir()
	run := func(access string) {
		t.Helper()
		cmd := exec.Command("/bin/sh", "-c", antigravityTokenLines)
		cmd.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), antigravityCredentialKey + "=" + access}
		if out, err := cmd.CombinedOutput(); err != nil || len(out) != 0 {
			t.Fatal(err, string(out))
		}
	}
	f := filepath.Join(home, ".gemini", "antigravity-cli", "antigravity-oauth-token")
	token := func() string {
		t.Helper()
		b, err := os.ReadFile(f)
		var m struct {
			Token struct {
				AccessToken string `json:"access_token"`
				TokenType   string `json:"token_type"`
				Expiry      string `json:"expiry"`
			} `json:"token"`
			AuthMethod string `json:"auth_method"`
		}
		if err != nil || json.Unmarshal(b, &m) != nil || m.Token.TokenType != "Bearer" || m.Token.Expiry != "2099-01-01T00:00:00Z" || m.AuthMethod != "consumer" {
			t.Fatal(err, string(b))
		}
		return m.Token.AccessToken
	}
	run("ph-1")
	if token() != "ph-1" {
		t.Fatal("not written")
	}
	if st, _ := os.Stat(f); st.Mode().Perm() != 0o600 {
		t.Fatal(st.Mode())
	}
	run("ph-2")
	if token() != "ph-2" {
		t.Fatal("not written again")
	}
	own := []byte(`{"token":{"access_token":"mine","refresh_token":"mine"}}`)
	os.WriteFile(f, own, 0o600)
	run("ph-3")
	if b, _ := os.ReadFile(f); string(b) != string(own) {
		t.Fatal("the owner's sign-in was replaced")
	}
	os.Remove(f)
	target := filepath.Join(t.TempDir(), "elsewhere")
	os.Symlink(target, f)
	run("ph-4")
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Fatal("wrote through a link")
	}
}
