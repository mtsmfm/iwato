// Command term-replay replays a terminal recording (Iwato's termTrace; in a
// sandbox, ~/.local/state/iwato/term-trace/*.jsonl) through the same
// emulator Iwato uses (internal/screen) and prints the screen, to find out
// whether a broken screen comes from the emulator. It lists when the
// program entered or left the alternate screen and when the size changed;
// -at stops at a time (milliseconds since the recording began), -hist adds
// the scrollback.
//
//	go run ./cmd/term-replay [-at MS] [-hist] FILE
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"iwato/internal/screen"
)

type line struct {
	T       int64  `json:"t"`
	K       string `json:"k"`
	At      string `json:"at"`
	Sandbox string `json:"sandbox"`
	Term    string `json:"term"`
	Cols    int    `json:"cols"`
	Rows    int    `json:"rows"`
	D       []byte `json:"d"`
	Why     string `json:"why"`
}

func main() {
	at := flag.Int64("at", -1, "stop at this time, milliseconds since the recording began (default: the end)")
	hist := flag.Bool("hist", false, "print the scrollback too")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: term-replay [-at MS] [-hist] FILE")
		os.Exit(2)
	}
	if err := run(flag.Arg(0), *at, *hist); err != nil {
		fmt.Fprintln(os.Stderr, "term-replay:", err)
		os.Exit(1)
	}
}

func run(path string, stopAt int64, hist bool) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<26)
	var term *screen.Term
	var bytes, chunks int
	var last int64
	alt := false
	for n := 1; sc.Scan(); n++ {
		var l line
		if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
			// A recording cut off by a crash ends in a partial line.
			fmt.Printf("line %d: unreadable (%v); stopping there\n", n, err)
			break
		}
		if stopAt >= 0 && l.T > stopAt {
			break
		}
		last = l.T
		switch l.K {
		case "h":
			fmt.Printf("recording of %s/%s from %s, %dx%d\n", l.Sandbox, l.Term, l.At, l.Cols, l.Rows)
			term = screen.New(l.Cols, l.Rows)
		case "o":
			if term == nil {
				return fmt.Errorf("line %d: output before the header", n)
			}
			term.Write(l.D)
			bytes += len(l.D)
			chunks++
			if term.Alt() != alt {
				alt = term.Alt()
				fmt.Printf("%8d ms  alternate screen %s (after %d bytes)\n", l.T, map[bool]string{true: "on", false: "off"}[alt], bytes)
			}
		case "r":
			if term == nil {
				return fmt.Errorf("line %d: resize before the header", n)
			}
			term.Resize(l.Cols, l.Rows)
			fmt.Printf("%8d ms  resized to %dx%d\n", l.T, l.Cols, l.Rows)
		case "e":
			fmt.Printf("%8d ms  end: %s\n", l.T, l.Why)
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	if term == nil {
		return fmt.Errorf("no header in %s", path)
	}
	cols, rows := term.Size()
	cx, cy := term.Cursor()
	fmt.Printf("\nreplayed %d output chunks, %d bytes, up to %d ms; screen %dx%d, cursor (%d,%d), alternate screen %v, title %q\n", chunks, bytes, last, cols, rows, cx, cy, term.Alt(), term.Title())
	if hist {
		first, next := term.History()
		fmt.Printf("\n--- scrollback (%d lines)\n", next-first)
		for id := first; id < next; id++ {
			if h, ok := term.HistLine(id); ok {
				fmt.Println(strings.TrimRight(screen.Text(h.Data), " "))
			}
		}
	}
	fmt.Printf("\n--- screen\n")
	for y := 0; y < rows; y++ {
		fmt.Println(strings.TrimRight(screen.Text(screen.AppendRow(nil, term.Row(y).C, false)), " "))
	}
	return nil
}
