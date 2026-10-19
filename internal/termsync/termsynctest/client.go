// Package termsynctest is a termsync client for tests outside the package:
// it applies frames as the browser does (text only) so that a test can wait
// for output on the screen or in the history.
package termsynctest

import (
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"iwato/internal/screen"
	"iwato/internal/termsync"
)

// Client is one attachment's view: the screen's rows and the history lines
// that scrolled off while it watched (or that it fetched).
type Client struct {
	rw      io.ReadWriter
	wmu     sync.Mutex
	mu      sync.Mutex
	changed chan struct{}
	cols    int
	rows    int
	grid    [][]string // cells: "" is blank, a wide character's second cell too
	hist    map[uint64]string
	next    uint64
	alt     bool
	err     error
}

// Start says hello at cols×rows on rw and applies what comes back,
// acknowledging each frame.
func Start(rw io.ReadWriter, cols, rows int) (*Client, error) {
	c := &Client{rw: rw, changed: make(chan struct{}, 1), hist: map[uint64]string{}}
	if err := c.send(termsync.KindHello, termsync.Hello(cols, rows)); err != nil {
		return nil, err
	}
	go c.run()
	return c, nil
}

func (c *Client) send(kind byte, p []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	return termsync.WriteMessage(c.rw, kind, p)
}

// Input sends bytes to the program.
func (c *Client) Input(b []byte) error { return c.send(termsync.KindInput, b) }

// Resize asks for another size.
func (c *Client) Resize(cols, rows int) error {
	return c.send(termsync.KindResize, binary.AppendUvarint(binary.AppendUvarint(nil, uint64(cols)), uint64(rows)))
}

// History asks for up to count history lines before line before.
func (c *Client) History(before, count uint64) error {
	return c.send(termsync.KindHistory, binary.AppendUvarint(binary.AppendUvarint(nil, before), count))
}

func (c *Client) run() {
	for {
		kind, p, err := termsync.ReadMessage(c.rw)
		if err == nil && kind != termsync.KindFrame && kind != termsync.KindPage {
			err = errors.New("unknown message")
		}
		var seq uint64
		if err == nil {
			c.mu.Lock()
			seq, err = c.apply(p, kind == termsync.KindPage)
			c.mu.Unlock()
		}
		if err == nil && kind == termsync.KindFrame {
			go func() { _ = c.send(termsync.KindAck, binary.AppendUvarint(nil, seq)) }()
		}
		if err != nil {
			c.mu.Lock()
			c.err = err
			c.mu.Unlock()
		}
		select {
		case c.changed <- struct{}{}:
		default:
		}
		if err != nil {
			return
		}
	}
}

// Text is the history lines the client knows, then the screen's rows,
// each without trailing blanks.
func (c *Client) Text() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.text()
}

func (c *Client) text() string {
	var b strings.Builder
	for id := uint64(0); id < c.next; id++ {
		if l, ok := c.hist[id]; ok {
			b.WriteString(l)
			b.WriteByte('\n')
		}
	}
	for _, row := range c.grid {
		b.WriteString(strings.TrimRight(strings.Join(row, ""), " "))
		b.WriteByte('\n')
	}
	return b.String()
}

// WaitFor waits until want shows in Text and returns that text. It fails
// when the connection ends first or after timeout.
func (c *Client) WaitFor(want string, timeout time.Duration) (string, error) {
	deadline := time.After(timeout)
	for {
		c.mu.Lock()
		text, err := c.text(), c.err
		c.mu.Unlock()
		if strings.Contains(text, want) {
			return text, nil
		}
		if err != nil {
			return text, err
		}
		select {
		case <-c.changed:
		case <-deadline:
			return text, errors.New("timed out waiting for " + want)
		}
	}
}

// Ended waits until the connection ends (the hub closed it).
func (c *Client) Ended(timeout time.Duration) error {
	deadline := time.After(timeout)
	for {
		c.mu.Lock()
		err := c.err
		c.mu.Unlock()
		if err != nil {
			return nil
		}
		select {
		case <-c.changed:
		case <-deadline:
			return errors.New("the connection did not end")
		}
	}
}

type reader struct {
	b   []byte
	err error
}

func (r *reader) u() uint64 {
	if r.err != nil {
		return 0
	}
	v, n := binary.Uvarint(r.b)
	if n <= 0 {
		r.err = errors.New("bad uvarint")
		return 0
	}
	r.b = r.b[n:]
	return v
}

func (r *reader) byte1() byte {
	if r.err != nil || len(r.b) == 0 {
		r.err = errors.New("short message")
		return 0
	}
	v := r.b[0]
	r.b = r.b[1:]
	return v
}

func (r *reader) bytes() []byte {
	n := r.u()
	if r.err != nil || uint64(len(r.b)) < n {
		r.err = errors.New("short message")
		return nil
	}
	v := r.b[:n]
	r.b = r.b[n:]
	return v
}

// cells decodes a row into one string per cell.
func cells(data []byte) ([]string, error) {
	runs, ok := screen.DecodeRow(data)
	if !ok {
		return nil, errors.New("bad row")
	}
	var cs []string
	for _, run := range runs {
		add := func(text string) {
			cs = append(cs, text)
			if run.Width == 2 {
				cs = append(cs, "")
			}
		}
		if run.Single {
			add(run.Text)
			continue
		}
		for len(run.Text) > 0 {
			_, n := utf8.DecodeRuneInString(run.Text)
			add(run.Text[:n])
			run.Text = run.Text[n:]
		}
	}
	return cs, nil
}

func (c *Client) blankRow() []string {
	row := make([]string, c.cols)
	for i := range row {
		row[i] = " "
	}
	return row
}

func (c *Client) blankGrid() {
	c.grid = make([][]string, c.rows)
	for i := range c.grid {
		c.grid[i] = c.blankRow()
	}
}

func rowText(cs []string) string { return strings.TrimRight(strings.Join(cs, ""), " ") }

// apply applies a frame or a page; it returns a frame's number.
func (c *Client) apply(p []byte, page bool) (uint64, error) {
	r := &reader{b: p}
	var seq uint64
	if !page {
		seq = r.u()
	}
	for len(r.b) > 0 && r.err == nil {
		switch op := r.byte1(); op {
		case termsync.OpReset:
			c.cols, c.rows, c.alt = int(r.u()), int(r.u()), r.u() == 1
			r.u()
			c.next = r.u()
			c.hist = map[uint64]string{}
			c.blankGrid()
		case termsync.OpStyle:
			for i := 0; i < 7; i++ {
				r.u()
			}
		case termsync.OpLink:
			r.u()
			r.bytes()
		case termsync.OpResize:
			c.cols, c.rows = int(r.u()), int(r.u())
			c.blankGrid()
		case termsync.OpAlt:
			c.alt = r.byte1() == 1
			c.blankGrid()
		case termsync.OpScroll:
			n, k := r.u(), r.u()
			known := min(n, uint64(c.rows))
			if c.alt {
				known = 0
			}
			for i := uint64(0); i < known; i++ {
				c.hist[c.next+i] = rowText(c.grid[i])
			}
			for j := uint64(0); j < k && r.err == nil; j++ {
				i := r.u()
				r.byte1()
				cs, err := cells(r.bytes())
				if err != nil {
					return 0, err
				}
				c.hist[c.next+i] = rowText(cs)
			}
			if !c.alt {
				copy(c.grid, c.grid[known:])
				for i := c.rows - int(known); i < c.rows; i++ {
					c.grid[i] = c.blankRow()
				}
			}
			c.next += n
		case termsync.OpHistFirst:
			first := r.u()
			for id := range c.hist {
				if id < first {
					delete(c.hist, id)
				}
			}
		case termsync.OpRow:
			y := int(r.u())
			r.byte1()
			cs, err := cells(r.bytes())
			if err != nil {
				return 0, err
			}
			if y >= len(c.grid) {
				return 0, errors.New("row out of range")
			}
			row := c.blankRow()
			copy(row, cs)
			c.grid[y] = row
		case termsync.OpPatch:
			y, x := int(r.u()), int(r.u())
			cs, err := cells(r.bytes())
			if err != nil {
				return 0, err
			}
			if y >= len(c.grid) || x > c.cols {
				return 0, errors.New("patch out of range")
			}
			copy(c.grid[y][x:], cs)
		case termsync.OpCursor:
			for i := 0; i < 4; i++ {
				r.u()
			}
		case termsync.OpModes:
			for i := 0; i < 3; i++ {
				r.u()
			}
		case termsync.OpTitle, termsync.OpClipboard, termsync.OpOpen:
			r.bytes()
		case termsync.OpBell:
		case termsync.OpLines:
			first, count := r.u(), r.u()
			for id := first; id < first+count && r.err == nil; id++ {
				r.byte1()
				cs, err := cells(r.bytes())
				if err != nil {
					return 0, err
				}
				c.hist[id] = rowText(cs)
			}
		default:
			return 0, errors.New("unknown operation")
		}
	}
	return seq, r.err
}
