package termsync

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"math/rand"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"iwato/internal/screen"
)

// model is a client as the browser is one: it applies frames and pages.
type model struct {
	cols, rows          int
	alt                 bool
	grid                []mrow
	hist                map[uint64]*mrow // nil: unknown
	histFirst, histNext uint64
	styles              map[uint16]bool
	links               map[uint64]string
	styleLinks          map[uint16]string // each style's URL
	clipboard, opens    []string
	cx, cy              int
	modes               uint64 // the mode flags
	title               string
	frames, pages       int
}

type mrow struct {
	cells []screen.Cell
	cont  bool
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
		r.err = fmt.Errorf("bad uvarint")
		return 0
	}
	r.b = r.b[n:]
	return v
}

func (r *reader) byte1() byte {
	if r.err != nil || len(r.b) == 0 {
		r.err = fmt.Errorf("short")
		return 0
	}
	c := r.b[0]
	r.b = r.b[1:]
	return c
}

func (r *reader) bytes() []byte {
	n := r.u()
	if r.err != nil || uint64(len(r.b)) < n {
		r.err = fmt.Errorf("short bytes")
		return nil
	}
	s := r.b[:n]
	r.b = r.b[n:]
	return s
}

// cells decodes a row as the browser does.
func cells(data []byte) []screen.Cell {
	runs, ok := screen.DecodeRow(data)
	if !ok {
		panic("bad row")
	}
	var cs []screen.Cell
	for _, run := range runs {
		add := func(text string) {
			r, n := utf8.DecodeRuneInString(text)
			c := screen.Cell{R: r, Comb: text[n:], W: uint8(run.Width), S: run.Style}
			if r == ' ' && c.Comb == "" {
				c.R = 0
			}
			cs = append(cs, c)
			if run.Width == 2 {
				cs = append(cs, screen.Cell{W: 0, S: run.Style})
			}
		}
		if run.Single {
			add(run.Text)
			continue
		}
		for _, r := range run.Text {
			add(string(r))
		}
	}
	return cs
}

func pad(cs []screen.Cell, n int) []screen.Cell {
	cs = append([]screen.Cell(nil), cs...)
	for len(cs) < n {
		cs = append(cs, screen.Cell{W: 1})
	}
	return cs[:n]
}

func (m *model) blankGrid() {
	m.grid = make([]mrow, m.rows)
	for i := range m.grid {
		m.grid[i] = mrow{cells: pad(nil, m.cols)}
	}
}

func (m *model) apply(p []byte, page bool) error {
	r := &reader{b: p}
	if !page {
		r.u()
		m.frames++
	} else {
		m.pages++
	}
	for len(r.b) > 0 && r.err == nil {
		switch op := r.byte1(); op {
		case OpReset:
			m.cols, m.rows, m.alt = int(r.u()), int(r.u()), r.u() == 1
			m.histFirst, m.histNext = r.u(), r.u()
			m.hist = map[uint64]*mrow{}
			m.styles = map[uint16]bool{}
			m.links, m.styleLinks = map[uint64]string{}, map[uint16]string{}
			m.blankGrid()
		case OpStyle:
			id := uint16(r.u())
			for i := 0; i < 5; i++ {
				r.u()
			}
			if l := r.u(); l != 0 {
				url, ok := m.links[l]
				if !ok {
					return fmt.Errorf("style %d uses link %d before it was defined", id, l)
				}
				m.styleLinks[id] = url
			}
			m.styles[id] = true
		case OpLink:
			id := r.u()
			m.links[id] = string(r.bytes())
		case OpResize:
			m.cols, m.rows = int(r.u()), int(r.u())
			m.blankGrid()
		case OpAlt:
			m.alt = r.byte1() == 1
			m.blankGrid()
		case OpScroll:
			n, k := r.u(), r.u()
			known := min(n, uint64(m.rows))
			if m.alt {
				known = 0
			}
			for i := uint64(0); i < n; i++ {
				m.hist[m.histNext+i] = nil
			}
			for i := uint64(0); i < known; i++ {
				row := m.grid[i]
				m.hist[m.histNext+i] = &row
			}
			for j := uint64(0); j < k; j++ {
				i, cont, data := r.u(), r.byte1() == 1, r.bytes()
				m.hist[m.histNext+i] = &mrow{cells: cells(data), cont: cont}
			}
			if !m.alt {
				copy(m.grid, m.grid[known:])
				for i := m.rows - int(known); i < m.rows; i++ {
					m.grid[i] = mrow{cells: pad(nil, m.cols)}
				}
			}
			m.histNext += n
		case OpHistFirst:
			m.histFirst = r.u()
			for id := range m.hist {
				if id < m.histFirst {
					delete(m.hist, id)
				}
			}
		case OpRow:
			y, cont, data := r.u(), r.byte1() == 1, r.bytes()
			m.need(data)
			m.grid[y] = mrow{cells: pad(cells(data), m.cols), cont: cont}
		case OpPatch:
			y, x, data := int(r.u()), int(r.u()), r.bytes()
			m.need(data)
			copy(m.grid[y].cells[x:], cells(data))
		case OpCursor:
			m.cx, m.cy = int(r.u()), int(r.u())
			r.u()
			r.u()
		case OpModes:
			m.modes = r.u()
			r.u()
			r.u()
		case OpTitle:
			m.title = string(r.bytes())
		case OpBell:
		case OpClipboard:
			m.clipboard = append(m.clipboard, string(r.bytes()))
		case OpOpen:
			m.opens = append(m.opens, string(r.bytes()))
		case OpLines:
			first, count := r.u(), r.u()
			for id := first; id < first+count; id++ {
				cont, data := r.byte1() == 1, r.bytes()
				m.need(data)
				m.hist[id] = &mrow{cells: cells(data), cont: cont}
			}
		default:
			return fmt.Errorf("unknown op %d", op)
		}
	}
	return r.err
}

func (m *model) need(data []byte) {
	runs, _ := screen.DecodeRow(data)
	for _, run := range runs {
		if run.Style != 0 && !m.styles[run.Style] {
			panic(fmt.Sprintf("style %d used before it was defined", run.Style))
		}
	}
}

// compare checks the model against the terminal: the screen, the cursor
// and every history line the model knows.
func compare(t *testing.T, m *model, term *screen.Term) {
	t.Helper()
	cols, rows := term.Size()
	if m.cols != cols || m.rows != rows || m.alt != term.Alt() {
		t.Fatalf("size %dx%d alt %v, want %dx%d alt %v", m.cols, m.rows, m.alt, cols, rows, term.Alt())
	}
	for y := 0; y < rows; y++ {
		want := screen.AppendRow(nil, term.Row(y).C, true)
		got := screen.AppendRow(nil, m.grid[y].cells, true)
		if !bytes.Equal(got, want) || m.grid[y].cont != term.Row(y).Cont {
			t.Fatalf("row %d: %q cont %v, want %q cont %v", y, screen.Text(got), m.grid[y].cont, screen.Text(want), term.Row(y).Cont)
		}
	}
	if x, y := term.Cursor(); x != m.cx || y != m.cy {
		t.Fatalf("cursor %d,%d want %d,%d", m.cx, m.cy, x, y)
	}
	first, next := term.History()
	if m.histFirst != first || m.histNext != next {
		t.Fatalf("history %d..%d want %d..%d", m.histFirst, m.histNext, first, next)
	}
	for id := first; id < next; id++ {
		l, _ := term.HistLine(id)
		got := m.hist[id]
		if got == nil {
			continue
		}
		if !bytes.Equal(screen.AppendRow(nil, got.cells, true), screen.TrimRow(l.Data)) || got.cont != l.Cont {
			t.Fatalf("history line %d: %q cont %v, want %q cont %v", id, screen.Text(screen.AppendRow(nil, got.cells, true)), got.cont, screen.Text(l.Data), l.Cont)
		}
	}
}

// testClient runs a model over a pipe to the hub, acking each frame.
type testClient struct {
	conn net.Conn
	mu   sync.Mutex
	m    *model
	last time.Time
	err  error
	ack  bool
	send func(kind byte, p []byte)
}

func startClient(t *testing.T, h *Hub, cols, rows int, ack bool) *testClient {
	a, b := net.Pipe()
	go h.Serve(b)
	c := &testClient{conn: a, m: &model{}, ack: ack}
	var wmu sync.Mutex
	go func() {
		for {
			kind, p, err := ReadMessage(a)
			if err != nil {
				return
			}
			c.mu.Lock()
			if e := c.m.apply(p, kind == KindPage); e != nil && c.err == nil {
				c.err = e
			}
			c.last = time.Now()
			n, ack := c.m.frames, c.ack
			c.mu.Unlock()
			if kind == KindFrame && ack {
				go func() {
					wmu.Lock()
					defer wmu.Unlock()
					WriteMessage(a, KindAck, binary.AppendUvarint(nil, uint64(n)))
				}()
			}
		}
	}()
	wmu.Lock()
	WriteMessage(a, KindHello, Hello(cols, rows))
	wmu.Unlock()
	c.send = func(kind byte, p []byte) {
		wmu.Lock()
		defer wmu.Unlock()
		WriteMessage(a, kind, p)
	}
	return c
}

func (c *testClient) settle(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
		c.mu.Lock()
		quietFor := time.Since(c.last)
		frames := c.m.frames
		c.mu.Unlock()
		if frames > 0 && quietFor > 100*time.Millisecond {
			return
		}
	}
	t.Fatal("no quiet")
}

func newHub(cols, rows int) (*Hub, *bytes.Buffer, *sync.Mutex) {
	var in bytes.Buffer
	var mu sync.Mutex
	h := NewHub(cols, rows, func(b []byte) error { mu.Lock(); in.Write(b); mu.Unlock(); return nil }, func(int, int) error { return nil })
	return h, &in, &mu
}

func TestFramesFollowOutput(t *testing.T) {
	h, _, _ := newHub(20, 5)
	defer h.Close()
	c := startClient(t, h, 20, 5, true)
	for i := 0; i < 30; i++ {
		h.Feed([]byte(fmt.Sprintf("\x1b[3%dmline %d 日本語\x1b[0m\r\n", i%8, i)))
	}
	h.Feed([]byte("prompt$ "))
	c.settle(t)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		t.Fatal(c.err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	compare(t, c.m, h.term)
}

func TestHistoryPages(t *testing.T) {
	h, _, _ := newHub(20, 3)
	defer h.Close()
	for i := 0; i < 50; i++ {
		h.Feed([]byte(fmt.Sprintf("old %d\r\n", i)))
	}
	c := startClient(t, h, 20, 3, true)
	c.settle(t)
	c.send(KindHistory, uvarints(1<<62, 1000))
	time.Sleep(100 * time.Millisecond)
	c.mu.Lock()
	defer c.mu.Unlock()
	// 3 rows: the last two lines and the cursor's row stay on the screen.
	if c.m.pages != 1 || len(c.m.hist) != 48 {
		t.Fatalf("pages %d lines %d", c.m.pages, len(c.m.hist))
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	compare(t, c.m, h.term)
}

func TestSlowClientSkipsFrames(t *testing.T) {
	h, _, _ := newHub(40, 10)
	defer h.Close()
	// Without acks only the first frames go out.
	c := startClient(t, h, 40, 10, false)
	for i := 0; i < 200; i++ {
		h.Feed([]byte(fmt.Sprintf("\r\x1b[Kprogress %d", i)))
		time.Sleep(time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	c.mu.Lock()
	frames := c.m.frames
	c.mu.Unlock()
	if frames > inFlight {
		t.Fatalf("%d frames without acks", frames)
	}
	// Acking catches up with the latest state in one frame.
	c.send(KindAck, uvarints(uint64(frames)))
	c.settle(t)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m.frames != frames+1 {
		t.Fatalf("frames %d, want %d", c.m.frames, frames+1)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	compare(t, c.m, h.term)
}

func TestRepliesGoToProgram(t *testing.T) {
	h, in, mu := newHub(20, 5)
	defer h.Close()
	h.Feed([]byte("\x1b[c"))
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if in.String() != "\x1b[?1;2c" {
		t.Fatalf("reply %q", in.String())
	}
}

// Arrow keys depend on application cursor mode (DECCKM): a browser that
// connects later, or after a screen switch and a resize, still learns it,
// and the keys' bytes reach the program unchanged.
func TestCursorKeysMode(t *testing.T) {
	h, in, mu := newHub(20, 5)
	defer h.Close()
	h.Feed([]byte("\x1b[?1h\x1b[?1049h"))
	c := startClient(t, h, 20, 5, true)
	c.settle(t)
	c.send(KindResize, uvarints(30, 8))
	h.Feed([]byte("\x1b[?1049lx"))
	late := startClient(t, h, 30, 8, true)
	late.settle(t)
	for _, c := range []*testClient{c, late} {
		c.mu.Lock()
		if c.m.modes&1 == 0 {
			t.Fatalf("application cursor mode not sent: %b", c.m.modes)
		}
		c.mu.Unlock()
	}
	h.Feed([]byte("\x1b[?1l"))
	time.Sleep(50 * time.Millisecond)
	late.settle(t)
	late.mu.Lock()
	if late.m.modes&1 != 0 {
		t.Fatalf("application cursor mode still on: %b", late.m.modes)
	}
	late.mu.Unlock()
	late.send(KindInput, []byte("\x1bOA\x1b[B\x10\x0e"))
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if in.String() != "\x1bOA\x1b[B\x10\x0e" {
		t.Fatalf("input %q", in.String())
	}
}

// Random output, resizes and screen switches against the model.
func TestRandomOutput(t *testing.T) {
	for seed := int64(1); seed <= 20; seed++ {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			rng := rand.New(rand.NewSource(seed))
			h, _, _ := newHub(30, 8)
			defer h.Close()
			c := startClient(t, h, 30, 8, true)
			words := []string{"hello", "日本語", "x", "  ", "é", "🙂", "tab\t", "\x1b[31m", "\x1b[0m", "\x1b[1;44m", "\x1b[38;2;10;20;30m"}
			for step := 0; step < 400; step++ {
				var b strings.Builder
				switch rng.Intn(14) {
				case 0:
					fmt.Fprintf(&b, "\x1b[%d;%dH", rng.Intn(10)+1, rng.Intn(35)+1)
				case 1:
					b.WriteString("\r\n")
				case 2:
					fmt.Fprintf(&b, "\x1b[%dK", rng.Intn(3))
				case 3:
					fmt.Fprintf(&b, "\x1b[%dJ", rng.Intn(4))
				case 4:
					if rng.Intn(4) == 0 {
						b.WriteString([]string{"\x1b[?1049h", "\x1b[?1049l"}[rng.Intn(2)])
					}
				case 5:
					fmt.Fprintf(&b, "\x1b[%d;%dr", rng.Intn(3)+1, rng.Intn(4)+5)
				case 6:
					fmt.Fprintf(&b, "\x1b[%dS\x1b[r", rng.Intn(3)+1)
				case 7:
					fmt.Fprintf(&b, "\x1b[%dL", rng.Intn(3)+1)
				case 8:
					fmt.Fprintf(&b, "\x1b[%dP\x1b[%d@", rng.Intn(4), rng.Intn(4))
				case 9:
					if rng.Intn(5) == 0 {
						h.mu.Lock()
						cols, rows := 20+rng.Intn(30), 4+rng.Intn(10)
						h.mu.Unlock()
						c.send(KindResize, uvarints(uint64(cols), uint64(rows)))
						time.Sleep(5 * time.Millisecond)
					}
				default:
					for i := rng.Intn(6); i >= 0; i-- {
						b.WriteString(words[rng.Intn(len(words))])
					}
				}
				h.Feed([]byte(b.String()))
				if rng.Intn(20) == 0 {
					time.Sleep(time.Duration(rng.Intn(20)) * time.Millisecond)
				}
			}
			c.settle(t)
			c.send(KindHistory, uvarints(1<<62, 1000))
			time.Sleep(50 * time.Millisecond)
			c.mu.Lock()
			defer c.mu.Unlock()
			if c.err != nil {
				t.Fatal(c.err)
			}
			h.mu.Lock()
			defer h.mu.Unlock()
			compare(t, c.m, h.term)
		})
	}
}

func TestLinkFlood(t *testing.T) {
	h, _, _ := newHub(80, 4)
	defer h.Close()
	c := startClient(t, h, 80, 4, true)
	// Far more link bytes than the table keeps, each link on its own line.
	pad := strings.Repeat("x", 4000)
	url := func(i int) string { return fmt.Sprintf("https://e.example/%d/%s", i, pad) }
	const n = 3000
	for i := range n {
		h.Feed([]byte(fmt.Sprintf("\x1b]8;;%s\x1b\\L%d\x1b]8;;\x1b\\\r\n", url(i), i)))
	}
	c.settle(t)
	h.mu.Lock()
	links, bytes := h.term.Links(), h.term.LinkBytes()
	h.mu.Unlock()
	if links > n/2 || bytes > 4<<20 {
		t.Fatalf("%d links, %d bytes kept", links, bytes)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	// The screen's links are the URLs their text was written with.
	for y := 0; y < 3; y++ {
		cs := c.m.grid[y].cells
		got := c.m.styleLinks[cs[0].S]
		var i int
		fmt.Sscanf(string(cs[1].R)+string(cs[2].R)+string(cs[3].R)+string(cs[4].R), "%d", &i)
		if got != url(i) {
			t.Fatalf("row %d (L%d) links to %.40q", y, i, got)
		}
	}
	// An old line's link, if any, is still its own.
	for id, row := range c.m.hist {
		if row == nil || len(row.cells) < 2 {
			continue
		}
		if got := c.m.styleLinks[row.cells[0].S]; got != "" && !strings.HasPrefix(got, fmt.Sprintf("https://e.example/%d/", id)) {
			t.Fatalf("history line %d links to %.40q", id, got)
		}
	}
}

func TestLaggingClientEvents(t *testing.T) {
	h, _, _ := newHub(40, 4)
	defer h.Close()
	// The client acks nothing: its frames stop and the events queue.
	c := startClient(t, h, 40, 4, false)
	time.Sleep(50 * time.Millisecond)
	big := strings.Repeat("A", 700<<10)
	for i := range 50 {
		b64 := base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("%d%s", i, big)))
		h.Feed([]byte("\x1b]52;c;" + b64 + "\x07\x1b]6464;open;https://o.example/" + fmt.Sprint(i) + "\x07line\r\n"))
	}
	h.mu.Lock()
	for cl := range h.clients {
		if len(cl.events) > 1+maxOpens || cl.eventBytes > maxEventBytes {
			t.Errorf("%d events, %d bytes queued", len(cl.events), cl.eventBytes)
		}
	}
	h.mu.Unlock()
	c.mu.Lock()
	frames := c.m.frames
	c.ack = true
	c.mu.Unlock()
	c.send(KindAck, uvarints(uint64(frames)))
	c.settle(t)
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.m.clipboard) == 0 || !strings.HasPrefix(c.m.clipboard[len(c.m.clipboard)-1], "49A") {
		t.Fatalf("last clipboard write not delivered (%d writes)", len(c.m.clipboard))
	}
	if len(c.m.opens) > 2*maxOpens {
		t.Fatalf("%d opens", len(c.m.opens))
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	compare(t, c.m, h.term)
}

// Client input marks the activity; bells and notifications are alerts that
// carry the input before them, and the notifications do not reach clients.
func TestActivity(t *testing.T) {
	h, in, mu := newHub(20, 5)
	defer h.Close()
	h.Feed([]byte("\a\a"))
	if a := h.Activity(); a.Alerts != 1 || !a.Recent[0].Input.IsZero() {
		t.Fatalf("one bell before any input: %+v", a)
	}
	c := startClient(t, h, 20, 5, true)
	c.settle(t)
	attached := h.Activity().Input
	if attached.IsZero() {
		t.Fatal("attaching is no input")
	}
	time.Sleep(10 * time.Millisecond)
	c.send(KindInput, []byte("x"))
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		got := in.String()
		mu.Unlock()
		if got == "x" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no input")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !h.Activity().Input.After(attached) {
		t.Fatal("input not marked")
	}
	for i := range 5 {
		h.Feed([]byte(fmt.Sprintf("\x1b]9;n%d\a", i)))
	}
	a := h.Activity()
	if a.Alerts != 6 || len(a.Recent) != maxAlerts || a.Recent[maxAlerts-1].Text != "n4" || !a.Recent[maxAlerts-1].Input.Equal(a.Input) {
		t.Fatalf("%+v", a)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m.clipboard != nil || c.m.opens != nil || c.err != nil {
		t.Fatalf("a notification reached the client: %v %v %v", c.m.clipboard, c.m.opens, c.err)
	}
}
