package main

import (
	"bytes"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// placeholderPrefix is how OpenShell's credential placeholders start. It is
// assembled so that no file in the repository contains it verbatim: once the
// sequence reaches an agent's conversation, the API refuses every later request.
const placeholderPrefix = "openshell:" + "resolve:env"

// TestNoPlaceholderLiterals keeps the placeholder's form out of the repository.
func TestNoPlaceholderLiterals(t *testing.T) {
	needle := []byte(placeholderPrefix)
	var files []string
	if out, err := exec.Command("git", "ls-files", "-z").Output(); err == nil {
		for _, f := range strings.Split(string(out), "\x00") {
			if f != "" {
				files = append(files, f)
			}
		}
	} else {
		filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				switch filepath.ToSlash(path) {
				case ".git", "dist", "web/vendor/fonts":
					return filepath.SkipDir
				}
				return nil
			}
			if d.Type().IsRegular() {
				files = append(files, path)
			}
			return nil
		})
	}
	if len(files) == 0 {
		t.Fatal("no files to check")
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue // deleted in the working tree, or a symlink to a directory
		}
		if bytes.Contains(b, needle) {
			t.Errorf("%s contains an OpenShell placeholder's prefix verbatim; assemble it (e.g. placeholderPrefix, or \"openshell:\"\"resolve:env\" in shell)", f)
		}
	}
}
