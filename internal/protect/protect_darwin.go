//go:build darwin

package protect

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Scheme tells how the file is protected: encrypted (AES-256-GCM) with a
// key that the login Keychain keeps for Iwato.
const Scheme = "keychain"

// The Keychain item that holds the key (a generic password, hex), made on
// first use. The security tool made it, so it reads it back without a
// prompt. Any program of this user can run the tool: like DPAPI's
// CurrentUser, this protects against other users and a copied disk.
const (
	service = "iwato"
	account = "secret-file-key"
)

// magic starts every protected file (a format version).
var magic = []byte("iwato-keychain-v1\n")

var (
	keyMu sync.Mutex
	key   []byte
)

func protect(b []byte) ([]byte, error) {
	aead, err := cipherFor()
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	out := append(append(append([]byte(nil), magic...), nonce...), aead.Seal(nil, nonce, b, magic)...)
	return out, nil
}

func unprotect(b []byte) ([]byte, error) {
	if !bytes.HasPrefix(b, magic) {
		return nil, errors.New("not a Keychain-protected secret file")
	}
	aead, err := cipherFor()
	if err != nil {
		return nil, err
	}
	b = b[len(magic):]
	if len(b) < aead.NonceSize() {
		return nil, errors.New("truncated secret file")
	}
	return aead.Open(nil, b[:aead.NonceSize()], b[aead.NonceSize():], magic)
}

func cipherFor() (cipher.AEAD, error) {
	k, err := keychainKey()
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(k)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// keychainKey returns the key, making it in the Keychain the first time.
func keychainKey() ([]byte, error) {
	keyMu.Lock()
	defer keyMu.Unlock()
	if key != nil {
		return key, nil
	}
	k, err := findKey()
	if errors.Is(err, errNoItem) {
		fresh := make([]byte, 32)
		if _, err := rand.Read(fresh); err != nil {
			return nil, err
		}
		// Through security's own command line on stdin: an argument would
		// show the key to every process (ps).
		cmd := fmt.Sprintf("add-generic-password -s %s -a %s -l %s -w %s\n", service, account, "iwato-secrets", hex.EncodeToString(fresh))
		if _, err := security(cmd, "-i"); err != nil {
			// Another process may have made the item since findKey.
			if k, ferr := findKey(); ferr == nil {
				key = k
				return k, nil
			}
			return nil, fmt.Errorf("store Iwato's key in the Keychain: %w", err)
		}
		k, err = findKey()
	}
	if err != nil {
		return nil, err
	}
	key = k
	return k, nil
}

var errNoItem = errors.New("no Keychain item")

func findKey() ([]byte, error) {
	out, err := security("", "find-generic-password", "-s", service, "-a", account, "-w")
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 44 { // errSecItemNotFound
		return nil, errNoItem
	}
	if err != nil {
		return nil, fmt.Errorf("read Iwato's key from the Keychain: %w", err)
	}
	k, err := hex.DecodeString(strings.TrimSpace(string(out)))
	if err != nil || len(k) != 32 {
		return nil, errors.New("the Keychain item for Iwato's key is not a key")
	}
	return k, nil
}

// security runs /usr/bin/security with stdin; a locked Keychain that would
// ask the owner gives up after a while instead of hanging.
func security(stdin string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	c := exec.CommandContext(ctx, "/usr/bin/security", args...)
	c.Stdin = strings.NewReader(stdin)
	var stderr bytes.Buffer
	c.Stderr = &stderr
	out, err := c.Output()
	if err != nil {
		if _, ok := err.(*exec.ExitError); ok {
			return out, &securityError{err: err, msg: strings.TrimSpace(stderr.String())}
		}
		return out, err
	}
	// `security -i` reports a failed command on its output, not its status.
	if len(args) > 0 && args[0] == "-i" && bytes.Contains(bytes.ToLower(append(out, stderr.Bytes()...)), []byte("error")) {
		return out, fmt.Errorf("security: %s", strings.TrimSpace(stderr.String()+" "+string(out)))
	}
	return out, nil
}

type securityError struct {
	err error
	msg string
}

func (e *securityError) Error() string { return "security: " + e.err.Error() + ": " + e.msg }
func (e *securityError) Unwrap() error { return e.err }

// DeleteKey removes the key from the Keychain (none is not an error): the
// files it protected cannot be read any more. The next Save makes a new one.
func DeleteKey() error {
	keyMu.Lock()
	defer keyMu.Unlock()
	key = nil
	_, err := security("", "delete-generic-password", "-s", service, "-a", account)
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 44 { // errSecItemNotFound
		return nil
	}
	if err != nil {
		return fmt.Errorf("delete Iwato's key from the Keychain: %w", err)
	}
	return nil
}
