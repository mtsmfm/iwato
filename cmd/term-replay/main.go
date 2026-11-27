// Command term-replay replays a terminal recording (Iwato's termTrace; in a
// sandbox, ~/.local/state/iwato/term-trace/*.jsonl) through the same
// emulator Iwato uses (internal/screen) and prints the screen, to find out
// whether a broken screen comes from the emulator. It lists when the
// program entered or left the alternate screen and when the size changed
// (and, after resizes that came back to the size they started from,
// whether the screen came back too); -at stops at a time (milliseconds
// since the recording began), -hist adds the scrollback.
//
// -find TEXT follows the rows TEXT is on. It lists when it was on more than
// one row (a frame drawn over an earlier one) and the rows it was on in
// the 40 minutes before the first time, each with the record after which
// it got there; then it shows how it came to the row it was on just before
// and that first time: the screen before and after, and, with -before,
// what the program wrote up to then. -mask replaces letters and digits
// (but TEXT) with x and 0: the output keeps the layout without what the
// terminal showed, so that it can leave the sandbox.
//
//	go run ./cmd/term-replay [-at MS] [-hist] [-mask] [-find TEXT [-before MS]] FILE
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

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

type options struct {
	at, before, min int64
	hist, mask      bool
	find            string
}

func main() {
	var o options
	flag.Int64Var(&o.at, "at", -1, "stop at this time, milliseconds since the recording began (default: the end)")
	flag.BoolVar(&o.hist, "hist", false, "print the scrollback too")
	flag.BoolVar(&o.mask, "mask", false, "replace letters and digits with x and 0 (but the -find text), to share the output without what the terminal showed")
	flag.StringVar(&o.find, "find", "", "follow the rows this text is on, and show how it came to be on more than one")
	flag.Int64Var(&o.before, "before", 0, "with -find, also print what the program wrote in these milliseconds before")
	flag.Int64Var(&o.min, "min", 500, "with -find, the milliseconds a state must last to count (shorter: a frame being drawn)")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: term-replay [-at MS] [-hist] [-mask] [-find TEXT [-before MS]] FILE")
		os.Exit(2)
	}
	if err := run(os.Stdout, flag.Arg(0), o); err != nil {
		fmt.Fprintln(os.Stderr, "term-replay:", err)
		os.Exit(1)
	}
}

func run(w io.Writer, path string, o options) error {
	mask := masker(o.mask, o.find)
	f := finder{want: []rune(o.find), text: []byte(o.find), min: o.min}
	var b burst
	var term *screen.Term
	var start time.Time
	var bytes, chunks int
	var last int64
	alt := false
	err := records(w, path, func(n int, l line) (bool, error) {
		if o.at >= 0 && l.T > o.at {
			return false, nil
		}
		last = l.T
		if term == nil && l.K != "h" {
			return false, fmt.Errorf("line %d: a record before the header", n)
		}
		switch l.K {
		case "h":
			fmt.Fprintf(w, "recording of %s/%s from %s, %dx%d\n", l.Sandbox, l.Term, l.At, l.Cols, l.Rows)
			start, _ = time.Parse(time.RFC3339Nano, l.At)
			term = screen.New(l.Cols, l.Rows)
		case "o":
			if msg := b.output(term, l.T); msg != "" {
				fmt.Fprintf(w, "%s  %s\n", strings.Repeat(" ", len(stamp(start, 0))), msg)
			}
			term.Write(l.D)
			bytes += len(l.D)
			chunks++
			if term.Alt() != alt {
				alt = term.Alt()
				fmt.Fprintf(w, "%s  alternate screen %s (after %d bytes)\n", stamp(start, l.T), map[bool]string{true: "on", false: "off"}[alt], bytes)
			}
		case "r":
			b.resize(term, n, l.T)
			term.Resize(l.Cols, l.Rows)
			fmt.Fprintf(w, "%s  resized to %dx%d\n", stamp(start, l.T), l.Cols, l.Rows)
		case "e":
			fmt.Fprintf(w, "%s  end: %s\n", stamp(start, l.T), l.Why)
		}
		if len(f.want) > 0 {
			f.check(term, n, l, b)
		}
		return true, nil
	})
	if err != nil {
		return err
	}
	if term == nil {
		return fmt.Errorf("no header in %s", path)
	}
	if len(f.want) > 0 {
		f.finish(last, true)
		if err := f.report(w, path, o, start, mask); err != nil {
			return err
		}
	}
	cols, rows := term.Size()
	cx, cy := term.Cursor()
	fmt.Fprintf(w, "\nreplayed %d output chunks, %d bytes, up to %d ms; screen %dx%d, cursor (%d,%d), alternate screen %v, title %q\n", chunks, bytes, last, cols, rows, cx, cy, term.Alt(), mask(term.Title()))
	if o.hist {
		first, next := term.History()
		fmt.Fprintf(w, "\n--- scrollback (%d lines)\n", next-first)
		for id := first; id < next; id++ {
			if h, ok := term.HistLine(id); ok {
				fmt.Fprintln(w, mask(strings.TrimRight(screen.Text(h.Data), " ")))
			}
		}
	}
	fmt.Fprintf(w, "\n--- screen\n")
	for y := 0; y < rows; y++ {
		fmt.Fprintln(w, mask(rowText(term, y)))
	}
	return nil
}

// records calls fn with each record of a recording until it returns false.
func records(w io.Writer, path string, fn func(n int, l line) (bool, error)) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<26)
	for n := 1; sc.Scan(); n++ {
		var l line
		if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
			// A recording cut off by a crash ends in a partial line.
			fmt.Fprintf(w, "line %d: unreadable (%v); stopping there\n", n, err)
			return nil
		}
		if more, err := fn(n, l); err != nil || !more {
			return err
		}
	}
	return sc.Err()
}

func stamp(start time.Time, t int64) string {
	if start.IsZero() {
		return fmt.Sprintf("%9d ms", t)
	}
	return fmt.Sprintf("%9d ms %s", t, start.Add(time.Duration(t)*time.Millisecond).UTC().Format("15:04:05.000Z"))
}

func rowText(term *screen.Term, y int) string {
	return strings.TrimRight(screen.Text(screen.AppendRow(nil, term.Row(y).C, false)), " ")
}

func screenText(term *screen.Term) string {
	cols, rows := term.Size()
	x, y := term.Cursor()
	s := fmt.Sprintf("%dx%d %d,%d\n", cols, rows, x, y)
	for i := 0; i < rows; i++ {
		s += rowText(term, i) + "\n"
	}
	return s
}

// burst follows resizes less than a second apart (a reconnecting page sends
// two), to tell whether the screen came back with the size: the program
// does not redraw when it reads the size it had.
type burst struct {
	line       int // its first resize; 0: none yet
	at, end    int64
	cols, rows int    // the size before it
	screen     string // the screen before it
	between    int    // output records between its resizes
	since      int    // output records since its last resize
	told       bool
}

func (b *burst) resize(term *screen.Term, n int, t int64) {
	if b.line == 0 || t-b.end > 1000 {
		cols, rows := term.Size()
		*b = burst{line: n, at: t, cols: cols, rows: rows, screen: screenText(term)}
	}
	b.between += b.since
	b.since, b.end, b.told = 0, t, false
}

// output notes an output record, and says before the first one after the
// resizes whether they left the screen as it was.
func (b *burst) output(term *screen.Term, t int64) string {
	if b.line == 0 {
		return ""
	}
	if t-b.end <= 1000 {
		b.since++
	}
	if b.told {
		return ""
	}
	b.told = true
	if cols, rows := term.Size(); cols != b.cols || rows != b.rows {
		return ""
	}
	if screenText(term) == b.screen {
		return "the screen is as before these resizes"
	}
	return fmt.Sprintf("the screen is not as before these resizes (%d output records between them)", b.between)
}

// finder follows the rows its text is on. It looks between frames: inside
// a synchronized update the old frame and the new one are both on the
// screen on purpose.
type finder struct {
	want []rune
	text []byte
	min  int64
	buf  []rune
	cur  seg
	segs []seg // those that lasted min ms or more; equal neighbours merged
}

type seg struct {
	rows     []int // from 1
	from, to int64
	line     int    // the record after which it began
	after    string // what that record was
	size     string
	burst    burst // the last resizes before it
	open     bool  // still so at the end
}

func (f *finder) check(term *screen.Term, n int, l line, b burst) {
	if term.Modes().Sync {
		return
	}
	rows := f.rows(term)
	if f.cur.line != 0 && slices.Equal(rows, f.cur.rows) {
		return
	}
	f.finish(l.T, false)
	cols, rs := term.Size()
	f.cur = seg{rows: rows, from: l.T, line: n, after: f.what(l), size: fmt.Sprintf("%dx%d", cols, rs), burst: b}
}

func (f *finder) what(l line) string {
	switch {
	case l.K == "r":
		return fmt.Sprintf("a resize to %dx%d", l.Cols, l.Rows)
	case l.K == "o" && bytes.Contains(l.D, f.text):
		return "output with the text"
	case l.K == "o":
		return "output without the text"
	}
	return "the header"
}

func (f *finder) finish(t int64, open bool) {
	if f.cur.line == 0 {
		return
	}
	f.cur.to, f.cur.open = t, open
	if f.cur.to-f.cur.from < f.min && !open {
		return
	}
	if k := len(f.segs); k > 0 && slices.Equal(f.segs[k-1].rows, f.cur.rows) {
		f.segs[k-1].to, f.segs[k-1].open = f.cur.to, open
		return
	}
	f.segs = append(f.segs, f.cur)
}

func (f *finder) rows(term *screen.Term) []int {
	_, rows := term.Size()
	var found []int
	for y := 0; y < rows; y++ {
		f.buf = f.buf[:0]
		for _, c := range term.Row(y).C {
			switch {
			case c.W == 0: // the right half of a wide character
			case c.R == 0:
				f.buf = append(f.buf, ' ')
			default:
				f.buf = append(f.buf, c.R)
			}
		}
		if contains(f.buf, f.want) {
			found = append(found, y+1)
		}
	}
	return found
}

func contains(s, sub []rune) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		j := 0
		for j < len(sub) && s[i+j] == sub[j] {
			j++
		}
		if j == len(sub) {
			return true
		}
	}
	return false
}

func (s seg) still() string {
	if s.open {
		return " (still at the end)"
	}
	return ""
}

// report lists when the text was on more than one row and the rows it was
// on before the first time, and shows how it came to the rows it had just
// before, and that first time.
func (f *finder) report(w io.Writer, path string, o options, start time.Time, mask func(string) string) error {
	fmt.Fprintf(w, "\n--- %q on more than one row for %d ms or more\n", string(f.want), o.min)
	first := -1
	for i, s := range f.segs {
		if len(s.rows) > 1 {
			if first < 0 {
				first = i
			}
			fmt.Fprintf(w, "%s  for %d ms%s, rows %v\n", stamp(start, s.from), s.to-s.from, s.still(), s.rows)
		}
	}
	if first < 0 {
		fmt.Fprintln(w, "never")
		return nil
	}
	const lookback = 40 * 60 * 1000
	d := f.segs[first]
	fmt.Fprintf(w, "\n--- the rows it was on from %d minutes before the first time (from 1; size; for how long; after what)\n", lookback/60000)
	for _, s := range f.segs[:first+1] {
		if s.to >= d.from-lookback {
			fmt.Fprintf(w, "%s  %v  %s  for %d ms%s  after %s\n", stamp(start, s.from), s.rows, s.size, s.to-s.from, s.still(), s.after)
		}
	}
	if first > 0 {
		p := f.segs[first-1]
		fmt.Fprintf(w, "\n=== how it came to rows %v at %s\n", p.rows, stamp(start, p.from))
		if err := detail(w, path, o, p, start, mask, true); err != nil {
			return err
		}
	}
	fmt.Fprintf(w, "\n=== the first time on more than one row, %s\n", stamp(start, d.from))
	return detail(w, path, o, d, start, mask, false)
}

// detail replays the recording again up to the record that began s and
// prints the screen before and after it. With window, it also prints what
// the program wrote in the o.before milliseconds before it, or from the
// last resizes when they were less than 10 seconds before (and the screen
// before them), and in the 2 seconds after it, and the screen then.
func detail(w io.Writer, path string, o options, s seg, start time.Time, mask func(string) string, window bool) error {
	var term *screen.Term
	sk := skeleton{mask: mask}
	from, until := s.from-o.before, s.from+2000
	near := window && s.burst.line != 0 && s.from-s.burst.at <= 10000
	if near && s.burst.at-1000 < from {
		from = s.burst.at - 1000
	}
	shown, done := false, false
	later := func() {
		fmt.Fprintf(w, "\n--- the screen 2 seconds later\n")
		printScreen(w, term, mask)
	}
	err := records(w, path, func(n int, l line) (bool, error) {
		if n > s.line && l.T > until {
			later()
			done = true
			return false, nil
		}
		show := n == s.line || window && l.T >= from && (n > s.line || o.before > 0 || near)
		if show && !shown {
			fmt.Fprintf(w, "\n--- what the program wrote from %s (\\e[…m, the colours, left out)\n", stamp(start, l.T))
			shown = true
		}
		if near && n == s.burst.line {
			fmt.Fprintf(w, "\n--- the screen before the resizes\n")
			printScreen(w, term, mask)
			fmt.Fprintln(w)
		}
		if n == s.line && term != nil {
			fmt.Fprintf(w, "\n--- the screen before the next record\n")
			printScreen(w, term, mask)
			fmt.Fprintln(w)
		}
		switch l.K {
		case "h":
			term = screen.New(l.Cols, l.Rows)
		case "o":
			// The record before too: it may end in the start of a sequence.
			if show || n == s.line-1 {
				if r := sk.render(l.D); show {
					fmt.Fprintf(w, "%s  %d B  %s\n", stamp(start, l.T), len(l.D), r)
				}
			}
			term.Write(l.D)
		case "r":
			if show {
				fmt.Fprintf(w, "%s  resized to %dx%d\n", stamp(start, l.T), l.Cols, l.Rows)
			}
			term.Resize(l.Cols, l.Rows)
		}
		if n == s.line {
			fmt.Fprintf(w, "\n--- the screen after it\n")
			printScreen(w, term, mask)
			if !window {
				done = true
				return false, nil
			}
			fmt.Fprintln(w)
		}
		return true, nil
	})
	if err == nil && !done && term != nil {
		later()
	}
	return err
}

func printScreen(w io.Writer, term *screen.Term, mask func(string) string) {
	cols, rows := term.Size()
	cx, cy := term.Cursor()
	fmt.Fprintf(w, "%dx%d, cursor at row %d column %d, alternate screen %v\n", cols, rows, cy+1, cx+1, term.Alt())
	for y := 0; y < rows; y++ {
		fmt.Fprintf(w, "%3d|%s\n", y+1, mask(rowText(term, y)))
	}
}

// masker returns what hides the text but keeps its layout: letters and
// digits become x and 0 (＊ when wide), combining marks go, and spaces,
// punctuation and symbols stay. keep stays as it is.
func masker(on bool, keep string) func(string) string {
	if !on {
		return func(s string) string { return s }
	}
	return func(s string) string {
		var b strings.Builder
		for keep != "" {
			i := strings.Index(s, keep)
			if i < 0 {
				break
			}
			maskTo(&b, s[:i])
			b.WriteString(keep)
			s = s[i+len(keep):]
		}
		maskTo(&b, s)
		return b.String()
	}
}

func maskTo(b *strings.Builder, s string) {
	for _, r := range s {
		switch {
		case unicode.IsMark(r):
		case !unicode.IsLetter(r) && !unicode.IsNumber(r):
			b.WriteRune(r)
		case screen.Width(r) == 2:
			b.WriteRune('＊')
		case screen.Width(r) == 0:
		case unicode.IsNumber(r):
			b.WriteByte('0')
		default:
			b.WriteByte('x')
		}
	}
}

// skeleton spells out what a program wrote, to see where it drew: control
// sequences as \e[… (but SGR), the strings of OSC, DCS, SOS, PM and APC by
// their kind and length only (titles, links and the clipboard are what the
// terminal showed), C0 controls as \r \n \t \b \a or \xNN, and the text
// through mask. A sequence or character cut at the end of a chunk is
// completed by the next one.
type skeleton struct {
	mask  func(string) string
	carry []byte
}

func (s *skeleton) render(d []byte) string {
	b := append(s.carry, d...)
	s.carry = nil
	var out, text strings.Builder
	flush := func() {
		if text.Len() > 0 {
			out.WriteString(strings.ReplaceAll(s.mask(text.String()), `\`, `\\`))
			text.Reset()
		}
	}
	for i := 0; i < len(b); {
		c := b[i]
		switch {
		case c == 0x1b:
			n, tok := escape(b[i:])
			if n == 0 {
				s.keep(b[i:])
				i = len(b)
				continue
			}
			flush()
			out.WriteString(tok)
			i += n
		case c < 0x20 || c == 0x7f:
			flush()
			out.WriteString(control(rune(c)))
			i++
		case c < 0x80:
			text.WriteByte(c)
			i++
		case !utf8.FullRune(b[i:]):
			s.keep(b[i:])
			i = len(b)
		default:
			r, n := utf8.DecodeRune(b[i:])
			if r < 0xa0 {
				flush()
				out.WriteString(control(r))
			} else {
				text.WriteRune(r)
			}
			i += n
		}
	}
	flush()
	return out.String()
}

// keep holds an unfinished sequence for the next chunk (not one longer than
// 64 KiB: that is not a sequence).
func (s *skeleton) keep(b []byte) {
	if len(b) <= 64<<10 {
		s.carry = append([]byte(nil), b...)
	}
}

func control(r rune) string {
	switch r {
	case '\r':
		return `\r`
	case '\n':
		return `\n`
	case '\t':
		return `\t`
	case '\b':
		return `\b`
	case 7:
		return `\a`
	}
	return fmt.Sprintf(`\x%02x`, r)
}

// escape reads the escape sequence b starts with and returns its length (0
// when b ends before it does) and how to show it.
func escape(b []byte) (int, string) {
	if len(b) < 2 {
		return 0, ""
	}
	switch b[1] {
	case '[':
		for j := 2; j < len(b); j++ {
			switch c := b[j]; {
			case c >= 0x40 && c <= 0x7e:
				if c == 'm' {
					return j + 1, ""
				}
				return j + 1, `\e[` + string(b[2:j+1])
			case c < 0x20 || c > 0x7e:
				return j, `\e[` + string(b[2:j]) + "(cut)"
			}
		}
		return 0, ""
	case ']', 'P', 'X', '^', '_':
		for j := 2; j < len(b); j++ {
			end := 0
			switch {
			case b[j] == 7 && b[1] == ']':
				end = 1
			case b[j] != 0x1b:
				continue
			case j+1 == len(b):
				return 0, ""
			case b[j+1] == '\\':
				end = 2
			}
			body := b[2:j]
			kind := string(b[1])
			if b[1] == ']' {
				// Only the number: the rest (a title, a link, the clipboard) is content.
				k := 0
				for k < len(body) && body[k] >= '0' && body[k] <= '9' {
					k++
				}
				kind += string(body[:k])
			}
			if end == 0 {
				return j, fmt.Sprintf(`\e%s(%d B, cut)`, kind, len(body))
			}
			return j + end, fmt.Sprintf(`\e%s(%d B)`, kind, len(body))
		}
		return 0, ""
	}
	for j := 1; j < len(b); j++ {
		switch c := b[j]; {
		case c >= 0x30 && c <= 0x7e:
			return j + 1, `\e` + string(b[1:j+1])
		case c < 0x20 || c > 0x7e:
			return j, `\e` + string(b[1:j]) + "(cut)"
		}
	}
	return 0, ""
}
