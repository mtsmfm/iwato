//go:build darwin

package protect

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// A secret goes through the Keychain's key and comes back; the file does
// not hold it in the clear.
func TestKeychainRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "secret")
	secret := []byte("tskey" + "-auth-roundtrip")
	if err := Save(p, secret); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(p)
	if bytes.Contains(raw, secret) {
		t.Fatal("stored in the clear")
	}
	got, err := Load(p)
	if err != nil || !bytes.Equal(got, secret) {
		t.Fatal(string(got), err)
	}
}
