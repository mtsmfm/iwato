//go:build linux

package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"iwato/internal/agentsmd"
)

// writeAgents writes the generated /AGENTS.md (agentsmd) when /AGENTS.md
// links to it, keeping the section Iwato wrote last. It never goes through
// a link in the home.
func writeAgents(link, staticPath, generated string) error {
	if t, err := os.Readlink(link); err != nil || t != generated {
		return nil
	}
	static, err := os.ReadFile(staticPath)
	if err != nil {
		return err
	}
	dir := filepath.Dir(generated)
	for i, d := range []string{filepath.Dir(filepath.Dir(dir)), filepath.Dir(dir), dir} {
		if i > 0 {
			if err := os.Mkdir(d, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
				return err
			}
		}
		fi, err := os.Lstat(d)
		if err != nil {
			return err
		}
		if st, ok := fi.Sys().(*syscall.Stat_t); !fi.IsDir() || !ok || int(st.Uid) != os.Getuid() {
			return fmt.Errorf("%s is not a directory of the user's own", d)
		}
	}
	var old []byte
	if fi, err := os.Lstat(generated); err == nil && fi.Mode().IsRegular() {
		old, _ = os.ReadFile(generated)
	}
	next := agentsmd.Compose(static, old)
	if old != nil && bytes.Equal(next, old) {
		return nil
	}
	tmp := fmt.Sprintf("%s/.AGENTS.md.%d", dir, os.Getpid())
	_ = os.Remove(tmp)
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(next)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, generated)
	}
	if err != nil {
		_ = os.Remove(tmp)
	}
	return err
}
