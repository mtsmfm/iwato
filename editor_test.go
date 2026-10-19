package main

import (
	"errors"
	"testing"

	"iwato/internal/dav"
)

func TestEditorPath(t *testing.T) {
	for _, p := range []string{"/etc/passwd", "..", "../x", "a/../../x", "a/..", "~/x", "a\x00b", ""} {
		if _, err := editorPath(p, false); err == nil {
			t.Errorf("editorPath(%q) was taken", p)
		}
	}
	for p, want := range map[string]string{"a/b.go": "a/b.go", "./a//b": "a/b", "work/iwato/": "work/iwato"} {
		if got, err := editorPath(p, false); err != nil || got != want {
			t.Errorf("editorPath(%q) = %q, %v; want %q", p, got, err, want)
		}
	}
	if got, err := editorPath("", true); err != nil || got != "." {
		t.Errorf("the root folder: %q, %v", got, err)
	}
}

// A save names the version it was edited from: it is refused once the file
// changed (or went away), unless forced, and a binary file is not opened.
func TestEditorSaveConflict(t *testing.T) {
	m := dav.NewMemServer()
	if err := m.WriteFile("a.txt", []byte("one\n")); err != nil {
		t.Fatal(err)
	}
	_ = m.WriteFile("bin", []byte("x\x00y"))
	c, err := dav.NewClient(m.Transport())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	data, err := readEditorFile(c, "a.txt")
	if err != nil || string(data) != "one\n" {
		t.Fatalf("read: %q, %v", data, err)
	}
	base := editorVersion(data)
	v, err := saveEditorFile(c, "a.txt", []byte("two\n"), base, false)
	if err != nil || v != editorVersion([]byte("two\n")) {
		t.Fatalf("save: %q, %v", v, err)
	}
	// Saving again from the first version: someone else's save came between.
	if _, err := saveEditorFile(c, "a.txt", []byte("three\n"), base, false); !errors.Is(err, errEditorChanged) {
		t.Fatalf("a stale save: %v", err)
	}
	if _, err := saveEditorFile(c, "a.txt", []byte("three\n"), base, true); err != nil {
		t.Fatalf("an overwrite: %v", err)
	}
	if data, _ := readEditorFile(c, "a.txt"); string(data) != "three\n" {
		t.Fatalf("after the overwrite: %q", data)
	}
	if _, err := saveEditorFile(c, "gone.txt", []byte("x"), base, false); !errors.Is(err, errEditorChanged) {
		t.Fatalf("a removed file: %v", err)
	}
	if _, err := readEditorFile(c, "bin"); !errors.Is(err, errEditorBinary) {
		t.Fatalf("a binary file: %v", err)
	}
	// No temporary file is left next to it.
	entries, _ := c.ReadDir(".")
	if len(entries) != 2 {
		t.Fatalf("entries after the saves: %d", len(entries))
	}
}
