package main

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"iwato/internal/termsync"
)

// termStep is what happens before one poll: the owner's input (keys, a
// scroll, an attach, a detach), a page coming or going, the program's
// alert, a new screen; then wait passes (termPoll when 0) and the
// notifier polls.
type termStep struct {
	input, enter, leave bool
	ring                *string
	lines               []string
	wait                time.Duration
}

var termBase = []string{"$ claude", "", "", "", "╭────╮", "│ >  │"}

func screenWith(top ...string) []string {
	l := slices.Clone(termBase)
	copy(l, top)
	return l
}

// work is n polls of output that scrolls.
func work(n int, word string) []termStep {
	var out []termStep
	for i := range n {
		out = append(out, termStep{lines: screenWith(fmt.Sprintf("%s %c", word, 'a'+i), fmt.Sprintf("%s %c", word, 'b'+i), fmt.Sprintf("%s %c", word, 'c'+i))})
	}
	return out
}

// rotating is n polls of a row that changes alone (a tip, a status word).
func rotating(n int) []termStep {
	words := []string{"apple", "banana", "cherry", "damson"}
	var out []termStep
	for i := range n {
		out = append(out, termStep{lines: screenWith("Done.", "", "tip: "+words[i%len(words)])})
	}
	return out
}

func quiet(d time.Duration) termStep { return termStep{wait: d} }

func ring(text string) termStep { return termStep{ring: &text} }

func steps(parts ...any) []termStep {
	var out []termStep
	for _, p := range parts {
		switch p := p.(type) {
		case termStep:
			out = append(out, p)
		case []termStep:
			out = append(out, p...)
		}
	}
	return out
}

// runTermSteps feeds the steps to a termWatch and returns its events as
// "step:event".
func runTermSteps(seq []termStep) []string {
	now := time.Unix(1_000_000, 0)
	scr := termScreen{cols: 80, rows: len(termBase), lines: termBase}
	present, uses := false, uint64(0)
	t := newTermWatch(now, scr, present, uses)
	var got []string
	for i, s := range seq {
		if s.input {
			scr.activity.Input = now
		}
		if s.enter {
			present, uses = true, uses+1
		}
		if s.leave {
			present = false
		}
		if s.ring != nil {
			scr.activity.Alerts++
			scr.activity.Recent = append(scr.activity.Recent, termsync.Alert{At: now.Add(time.Second), Input: scr.activity.Input, Text: *s.ring})
		}
		if s.lines != nil {
			scr.lines = s.lines
		}
		if s.wait == 0 {
			s.wait = termPoll
		}
		now = now.Add(s.wait)
		switch ev, text := t.observe(now, scr, present, uses); ev {
		case termDone:
			got = append(got, fmt.Sprintf("%d:done", i))
		case termAlert:
			got = append(got, fmt.Sprintf("%d:alert %s", i, text))
		}
	}
	return got
}

func TestTerminalDone(t *testing.T) {
	scrolled := screenWith("older 1", "older 2", "older 3")
	timer := func(s string) []string { return screenWith("Done.", "", "elapsed "+s) }
	spinner := func(s string) []string { return screenWith("Done.", "", "✻ Pondering… ("+s+" · esc to interrupt)") }
	for _, c := range []struct {
		name  string
		steps []termStep
		want  []string
	}{
		{"sustained output, then quiet", steps(work(6, "build"), termStep{lines: screenWith("Done.")}, quiet(55*time.Second), quiet(5*time.Second), quiet(5*time.Minute)),
			[]string{"8:done"}},
		{"a single burst after idle (a recap)", steps(quiet(10*time.Minute), termStep{lines: screenWith("※ recap: fixed the bug")}, termStep{lines: screenWith("※ recap: fixed the bug", "next: tests")}, quiet(3*time.Minute)),
			nil},
		{"changes right after the owner's input", steps(
			termStep{input: true, lines: screenWith("typing h")}, termStep{input: true, lines: screenWith("typing he")}, termStep{input: true, lines: screenWith("typing hel")},
			termStep{input: true, lines: screenWith("typing hell")}, termStep{input: true, lines: screenWith("typing hello")}, quiet(3*time.Minute)),
			nil},
		{"the owner's input, then the program works", steps(termStep{input: true, lines: screenWith("$ make test")}, work(6, "test"), quiet(2*time.Minute)),
			[]string{"7:done"}},
		{"scroll up and back, then close", steps(
			termStep{enter: true, input: true}, termStep{input: true, lines: scrolled}, termStep{input: true, lines: termBase},
			termStep{leave: true, input: true, lines: screenWith("$ claude (redrawn)")}, termStep{input: true, lines: termBase}, quiet(5*time.Minute)),
			nil},
		{"work that ends where the owner left it", steps(work(6, "redraw"), termStep{lines: termBase}, quiet(2*time.Minute)),
			nil},
		{"a timer ticking alone", steps(termStep{lines: timer("1s")}, termStep{lines: timer("6s")}, termStep{lines: timer("11s")}, termStep{lines: timer("16s")}, termStep{lines: timer("21s")}, quiet(3*time.Minute)),
			nil},
		{"work, then a timer ticks on", steps(work(6, "build"), termStep{lines: timer("1s")}, termStep{lines: timer("6s")}, termStep{lines: timer("11s")}, quiet(55*time.Second), termStep{lines: timer("2m")}),
			[]string{"9:done"}},
		{"work, then a row that changes alone", steps(work(6, "build"), rotating(30)), []string{"20:done"}},
		{"a working hint holds it, its end is done", steps(
			termStep{lines: spinner("1s")}, termStep{lines: spinner("6s")}, termStep{lines: spinner("11s")}, termStep{lines: spinner("16s")}, termStep{lines: spinner("21s")},
			slices.Repeat([]termStep{quiet(termPoll)}, 36), termStep{lines: screenWith("Done.", "", "✻ Worked for 3m")}),
			[]string{"41:done"}},
		{"bells: at once, one a minute, not after the owner's input", steps(
			ring(""), ring("Task done"), quiet(time.Minute), ring("Waiting for input"), termStep{input: true}, ring("")),
			[]string{"0:alert ", "3:alert Waiting for input"}},
	} {
		if got := runTermSteps(c.steps); !slices.Equal(got, c.want) {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}
