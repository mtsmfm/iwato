//go:build !windows && !darwin

package protect

// Scheme tells how the file is protected: here only by its mode (0600),
// the secret is plain text.
const Scheme = "file"

func protect(b []byte) ([]byte, error)   { return append([]byte(nil), b...), nil }
func unprotect(b []byte) ([]byte, error) { return append([]byte(nil), b...), nil }

// DeleteKey does nothing: there is no key.
func DeleteKey() error { return nil }
