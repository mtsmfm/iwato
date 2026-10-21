//go:build uitest

package main

import (
	"fmt"
	"io"
	"net"
	"regexp"
	"strings"
	"time"
)

// ccscroll is a full-screen app that scrolls a list 3 lines per wheel
// report and redraws the whole screen each time, as Claude Code does. Its
// first row shows the top line (POS) and the reports read (SEQ); q quits.
type ccscroll struct{ pos, seq int }

var wheelReport = regexp.MustCompile(`\x1b\[<(\d+);\d+;\d+[Mm]`)

func (t *fakeTerm) startScroll() {
	t.app = &ccscroll{}
	t.output("\x1b[?1049h\x1b[?7l\x1b[?1000h\x1b[?1006h")
	t.drawScroll()
}

// scrollInput takes the input while ccscroll runs (t.mu held).
func (t *fakeTerm) scrollInput(b []byte) {
	if strings.ContainsRune(string(b), 'q') {
		t.app = nil
		t.output("\x1b[?1000l\x1b[?1006l\x1b[?7h\x1b[?1049l" + t.id + " $ ")
		return
	}
	a := t.app
	for _, m := range wheelReport.FindAllSubmatch(b, -1) {
		switch string(m[1]) {
		case "64":
			a.pos = max(0, a.pos-3)
		case "65":
			a.pos = min(10000, a.pos+3)
		}
		a.seq++
	}
	t.drawScroll()
}

func (t *fakeTerm) drawScroll() {
	a := t.app
	var b strings.Builder
	fmt.Fprintf(&b, "\x1b[?2026h\x1b[H\x1b[7mPOS=%d SEQ=%d\x1b[0m\x1b[K", a.pos, a.seq)
	for y := 1; y < max(t.rows, 2); y++ {
		n := a.pos + y
		fmt.Fprintf(&b, "\r\n\x1b[3%dm%05d\x1b[0m ⏺ 行 %d: the quick brown fox jumps over the lazy dog \x1b[2m%d\x1b[0m\x1b[K", n%7+1, n, n, n*7919%10007)
	}
	b.WriteString("\x1b[?2026l")
	t.output(b.String())
}

// delayed relays c with d added to the round trip: d/2 each way, without
// limiting the throughput.
func delayed(c net.Conn, d time.Duration) net.Conn {
	a, b := net.Pipe()
	end := func() { c.Close(); b.Close() }
	go delayCopy(c, b, d/2, end)
	go delayCopy(b, c, d/2, end)
	return a
}

func delayCopy(dst io.Writer, src io.Reader, d time.Duration, end func()) {
	type chunk struct {
		at time.Time
		b  []byte
	}
	ch := make(chan chunk, 1024)
	go func() {
		defer close(ch)
		for {
			buf := make([]byte, 32<<10)
			n, err := src.Read(buf)
			if n > 0 {
				ch <- chunk{time.Now().Add(d), buf[:n]}
			}
			if err != nil {
				return
			}
		}
	}()
	for k := range ch {
		time.Sleep(time.Until(k.at))
		if _, err := dst.Write(k.b); err != nil {
			break
		}
	}
	end()
	for range ch {
	}
}
