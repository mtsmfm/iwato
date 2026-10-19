package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReplayReadsARecordingAndATruncatedOne(t *testing.T) {
	dir := t.TempDir()
	good := `{"t":0,"k":"h","v":1,"at":"2026-10-10T00:00:00Z","sandbox":"dev","term":"main","cols":20,"rows":4}
{"t":5,"k":"o","d":"aGVsbG8NCg=="}
{"t":9,"k":"r","cols":30,"rows":5}
{"t":12,"k":"e","why":"EOF"}
`
	p := filepath.Join(dir, "good.jsonl")
	if err := os.WriteFile(p, []byte(good), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run(p, -1, true); err != nil {
		t.Fatal(err)
	}
	// A crash leaves a partial last line: replay what is there.
	cut := filepath.Join(dir, "cut.jsonl")
	if err := os.WriteFile(cut, []byte(good[:len(good)-30]), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run(cut, -1, false); err != nil {
		t.Fatal(err)
	}
	if err := run(filepath.Join(dir, "missing.jsonl"), -1, false); err == nil {
		t.Fatal("a missing file was replayed")
	}
}
