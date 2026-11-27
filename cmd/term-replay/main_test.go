package main

import (
	"encoding/base64"
	"io"
	"os"
	"path/filepath"
	"strings"
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
	if err := run(io.Discard, p, options{at: -1, hist: true}); err != nil {
		t.Fatal(err)
	}
	// A crash leaves a partial last line: replay what is there.
	cut := filepath.Join(dir, "cut.jsonl")
	if err := os.WriteFile(cut, []byte(good[:len(good)-30]), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run(io.Discard, cut, options{at: -1}); err != nil {
		t.Fatal(err)
	}
	if err := run(io.Discard, filepath.Join(dir, "missing.jsonl"), options{at: -1}); err == nil {
		t.Fatal("a missing file was replayed")
	}
}

// -find with -mask shows how a line came to be twice on the screen, and
// nothing of what the terminal showed but the text looked for: here the
// program scrolled the screen while it had a row less, so the screen stayed
// up when the row came back, and the program then drew where it thought
// the line was.
func TestFindShowsHowALineCameTwiceWithoutTheText(t *testing.T) {
	enc := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
	rec := `{"t":0,"k":"h","v":1,"at":"2026-11-27T02:48:00Z","sandbox":"dev","term":"main","cols":20,"rows":4}
{"t":10,"k":"o","d":"` + enc("\x1b]0;secret title\x07\x1b[1;1Hhello 世界\x1b[4;1H\x1b[31mmode on\x1b[m 4") + `"}
{"t":600,"k":"r","cols":20,"rows":3}
{"t":601,"k":"o","d":"` + enc("\x1b[3;1H\n") + `"}
{"t":602,"k":"r","cols":20,"rows":4}
{"t":700,"k":"o","d":"` + enc("\x1b[") + `"}
{"t":1300,"k":"o","d":"` + enc("4;1Hmode on 5") + `"}
{"t":3000,"k":"o","d":"` + enc("\x1b[1;1Hhi") + `"}
`
	p := filepath.Join(t.TempDir(), "rec.jsonl")
	if err := os.WriteFile(p, []byte(rec), 0o600); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if err := run(&out, p, options{at: -1, mask: true, find: "mode on", min: 500}); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{
		"the screen is not as before these resizes (1 output records between them)",
		"1300 ms 02:48:01.300Z  for 1700 ms (still at the end), rows [2 4]",
		"[4]  20x4  for 590 ms  after output with the text",
		"[2]  20x3  for 699 ms  after output without the text",
		"=== how it came to rows [2]",
		"--- the screen before the resizes\n20x4, cursor at row 4 column 10, alternate screen false\n  1|xxxxx ＊＊\n  2|\n  3|\n  4|mode on 0\n",
		// The title by its length, the colours left out, the text masked
		// to the same width.
		`\e]0(14 B)\e[1;1Hxxxxx ＊＊\e[4;1Hmode on 0`,
		// A sequence cut between two records.
		`\e[4;1Hmode on 0`,
		"  2|mode on 0\n  3|\n  4|\n",
		"  2|mode on 0\n  3|\n  4|mode on 0\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("no %q in:\n%s", want, got)
		}
	}
	for _, secret := range []string{"secret", "hello", "世界"} {
		if strings.Contains(got, secret) {
			t.Errorf("%q shows in:\n%s", secret, got)
		}
	}
}
