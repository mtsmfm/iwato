// Package protect keeps a secret in a file only this user can read back:
// on Windows encrypted with DPAPI (CurrentUser scope), on macOS encrypted
// with a key the login Keychain keeps, elsewhere (Linux, tests) as plain
// text in a 0600 file.
package protect

import (
	"errors"
	"os"
	"path/filepath"
)

// Save writes secret to path (atomically, mode 0600).
func Save(path string, secret []byte) error {
	data, err := protect(secret)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".secret-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err = tmp.Chmod(0o600); err == nil {
		_, err = tmp.Write(data)
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Load reads a secret Save wrote; os.ErrNotExist when there is none.
func Load(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, errors.New("empty secret file")
	}
	return unprotect(data)
}

// Remove deletes the secret (none is not an error).
func Remove(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
