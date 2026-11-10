package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// imageFiles is the sandbox image's sources (sandboximage/), which the
// tests read from the repository: the exe does not carry them (Iwato pulls
// the image). Folders starting with "_" (the daemon's binaries a build of
// the image adds) are not sources.
var imageFiles = sourceFS{os.DirFS(".").(fs.ReadFileFS)}

type sourceFS struct{ fs.ReadFileFS }

func (s sourceFS) ReadDir(name string) ([]fs.DirEntry, error) {
	list, err := fs.ReadDir(s.ReadFileFS, name)
	out := list[:0]
	for _, e := range list {
		if !(e.IsDir() && strings.HasPrefix(e.Name(), "_")) {
			out = append(out, e)
		}
	}
	return out, err
}

// writeImageContext copies the image's sources into dir/sandboximage.
func writeImageContext(dir string) error {
	return fs.WalkDir(imageFiles, "sandboximage", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := imageFiles.ReadFile(p)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		target := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode().Perm()|0o600)
	})
}
