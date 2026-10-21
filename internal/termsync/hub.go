package termsync

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"iwato/internal/screen"
)

// Timing of frames: a frame goes out once output has paused for quiet, or
// at most every busy while it continues. While the program holds a
// synchronized update (mode 2026) the frame waits for its end, up to
// syncLimit. A client gets no new frame while inFlight frames await its ack.
const (
	quiet     = 4 * time.Millisecond
	busy      = 16 * time.Millisecond
	syncLimit = time.Second
	inFlight  = 2
	// maxPage bounds the history lines of one page.
	maxPage = 1000
	// A client's queue of events (clipboard writes and links to open) is
	// bounded while the client lags: it keeps only the latest clipboard
	// write, up to maxOpens links, and maxEventBytes of text in all
	// (screen.maxOSC bounds one clipboard write to 768 KiB).
	maxOpens      = 16
	maxEventBytes = 1 << 20
	// maxAlerts bounds the alerts a hub keeps.
	maxAlerts = 4
)

// Alert is a bell or a desktop notification from the program.
type Alert struct {
	At    time.Time
	Input time.Time // the last client input before it
	Text  string    // a notification's text; "" for a bell
}

// Activity is who last acted on the terminal and what the program asked
// for attention.
type Activity struct {
	// Input is the last client input: keys, paste, mouse, a resize, a
	// client attaching or leaving.
	Input time.Time
	// Alerts counts the alerts so far; Recent are the last of them.
	Alerts uint64
	Recent []Alert
}

// Hub holds a session's terminal and serves its clients.
type Hub struct {
	mu        sync.Mutex
	term      *screen.Term
	clients   map[*client]struct{}
	syncSince time.Time // when the current synchronized update began
	closed    bool
	activity  Activity
	bells     int

	input   func([]byte) error
	inputMu sync.Mutex
	resize  func(cols, rows int) error
	replies chan []byte
	done    chan struct{}
}

// NewHub returns a hub for a program on a terminal of cols×rows. input
// writes to the program (client input and answers to its queries); resize
// sizes the program's terminal. resize is called without the hub's lock.
func NewHub(cols, rows int, input func([]byte) error, resize func(cols, rows int) error) *Hub {
	h := &Hub{term: screen.New(cols, rows), clients: map[*client]struct{}{}, input: input, resize: resize,
		replies: make(chan []byte, 64), done: make(chan struct{})}
	h.term.Reply = func(b []byte) {
		select {
		case h.replies <- append([]byte(nil), b...):
		default:
		}
	}
	go func() {
		for {
			select {
			case b := <-h.replies:
				h.write(b)
			case <-h.done:
				return
			}
		}
	}()
	return h
}

func (h *Hub) write(b []byte) error {
	h.inputMu.Lock()
	defer h.inputMu.Unlock()
	return h.input(b)
}

// Feed takes output from the program.
func (h *Hub) Feed(b []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	was := h.term.Modes().Sync
	h.term.Write(b)
	if m := h.term.Modes(); m.Sync && !was {
		h.syncSince = time.Now()
	}
	events := h.term.TakeEvents()
	if b := h.term.Bells(); b != h.bells {
		h.bells = b
		h.alert("")
	}
	events = slices.DeleteFunc(events, func(e screen.Event) bool {
		if e.Kind == screen.EventNotify {
			h.alert(e.Text)
		}
		return e.Kind == screen.EventNotify
	})
	for c := range h.clients {
		c.queue(events)
		c.wakeUp()
	}
}

func (h *Hub) alert(text string) {
	a := &h.activity
	a.Alerts++
	a.Recent = append(a.Recent, Alert{At: time.Now(), Input: a.Input, Text: text})
	if len(a.Recent) > maxAlerts {
		a.Recent = slices.Delete(a.Recent, 0, len(a.Recent)-maxAlerts)
	}
}

func (h *Hub) touched() {
	h.mu.Lock()
	h.activity.Input = time.Now()
	h.mu.Unlock()
}

// Activity returns the terminal's activity.
func (h *Hub) Activity() Activity {
	h.mu.Lock()
	defer h.mu.Unlock()
	a := h.activity
	a.Recent = slices.Clone(a.Recent)
	return a
}

// Screen returns the terminal's size and the text of the rows it shows
// (without trailing blanks).
func (h *Hub) Screen() (cols, rows int, lines []string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	cols, rows = h.term.Size()
	lines = make([]string, rows)
	var b strings.Builder
	for y := range rows {
		b.Reset()
		for _, c := range h.term.Row(y).C {
			switch {
			case c.W == 0:
				continue // the right half of a wide character
			case c.R == 0:
				b.WriteByte(' ')
			default:
				b.WriteRune(c.R)
				b.WriteString(c.Comb)
			}
		}
		lines[y] = strings.TrimRight(b.String(), " ")
	}
	return cols, rows, lines
}

// setSize sizes the program's terminal and the screen for a client.
func (h *Hub) setSize(cols, rows int) error {
	if err := h.resize(cols, rows); err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.term.Resize(cols, rows)
	for c := range h.clients {
		c.wakeUp()
	}
	return nil
}

// Close ends the hub: the program ended. Clients get what is left and are
// disconnected.
func (h *Hub) Close() {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.closed = true
	clients := h.clients
	h.clients = map[*client]struct{}{}
	h.mu.Unlock()
	close(h.done)
	for c := range clients {
		c.endOnce.Do(func() { close(c.ending) })
	}
}

// ErrClosed is Serve's error once the program has ended.
var ErrClosed = errors.New("the terminal session has ended")

// Serve serves one client on rw until it disconnects or the hub closes.
// The client's first message must be a hello.
func (h *Hub) Serve(rw io.ReadWriteCloser) error {
	kind, p, err := ReadMessage(rw)
	if err != nil {
		rw.Close()
		return err
	}
	if kind != KindHello {
		rw.Close()
		return errors.New("expected hello")
	}
	return h.ServeHello(rw, p)
}

// ServeHello is Serve after the hello has been read.
func (h *Hub) ServeHello(rw io.ReadWriteCloser, hello []byte) error {
	defer rw.Close()
	cols, rows, err := ParseHello(hello)
	if err != nil {
		return err
	}
	if cols != 0 {
		if err = h.setSize(cols, rows); err != nil {
			return err
		}
	}
	c := &client{h: h, rw: rw, wake: make(chan struct{}, 1), reqs: make(chan [2]uint64, 16), done: make(chan struct{}), ending: make(chan struct{}), sent: make(chan struct{})}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return ErrClosed
	}
	h.clients[c] = struct{}{}
	h.mu.Unlock()
	h.touched()
	c.wakeUp()
	go c.send()
	err = c.read()
	h.mu.Lock()
	delete(h.clients, c)
	h.mu.Unlock()
	h.touched()
	c.finish()
	<-c.sent
	return err
}

// client is one attached browser and what it was last sent.
type client struct {
	h       *Hub
	rw      io.ReadWriteCloser
	wake    chan struct{}
	reqs    chan [2]uint64
	done    chan struct{}
	ending  chan struct{} // the hub closed: send what is left, then end
	sent    chan struct{} // closed when send returns
	once    sync.Once
	endOnce sync.Once
	acked   atomic.Uint64
	seq     uint64

	// Under h.mu:
	events     []screen.Event
	eventBytes int
	// What the client has (only touched by send, under h.mu).
	ready               bool
	version             uint64
	cols, rows          int
	alt                 bool
	grid                []screen.Row
	cx, cy              int
	cursorFlags, shape  int
	modes               []uint64
	title               string
	bells               int
	histFirst, histNext uint64
	styles              []bool
	links               map[uint32]bool
}

// queue adds events for the client, within the queue's bounds. A clipboard
// write replaces the one queued before it (only the last one would stay in
// the clipboard); a link to open that does not fit is dropped.
func (c *client) queue(events []screen.Event) {
	for _, e := range events {
		switch e.Kind {
		case screen.EventClipboard:
			c.events = slices.DeleteFunc(c.events, func(q screen.Event) bool {
				if q.Kind != screen.EventClipboard {
					return false
				}
				c.eventBytes -= len(q.Text)
				return true
			})
		case screen.EventOpen:
			opens := 0
			for _, q := range c.events {
				if q.Kind == screen.EventOpen {
					opens++
				}
			}
			if opens >= maxOpens {
				continue
			}
		}
		if c.eventBytes+len(e.Text) > maxEventBytes {
			continue
		}
		c.events = append(c.events, e)
		c.eventBytes += len(e.Text)
	}
}

func (c *client) wakeUp() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

func (c *client) finish() {
	c.once.Do(func() {
		close(c.done)
		c.rw.Close()
	})
}

func (c *client) read() error {
	for {
		kind, p, err := ReadMessage(c.rw)
		if err != nil {
			return err
		}
		switch kind {
		case KindInput:
			c.h.touched()
			if err = c.h.write(p); err != nil {
				return err
			}
		case KindResize:
			v, ok := readUvarints(p, 2)
			if !ok || !validSize(int(v[0]), int(v[1])) {
				return errors.New("invalid resize")
			}
			c.h.touched()
			if err = c.h.setSize(int(v[0]), int(v[1])); err != nil {
				return err
			}
		case KindAck:
			v, ok := readUvarints(p, 1)
			if !ok {
				return errors.New("invalid ack")
			}
			c.acked.Store(v[0])
			c.wakeUp()
		case KindHistory:
			v, ok := readUvarints(p, 2)
			if !ok {
				return errors.New("invalid history request")
			}
			select {
			case c.reqs <- [2]uint64{v[0], v[1]}:
			default:
			}
		case KindHello:
		default:
			return errors.New("unknown message")
		}
	}
}

// send writes frames when something changed, pages when asked.
func (c *client) send() {
	defer close(c.sent)
	defer c.finish()
	var timer *time.Timer
	var timeout <-chan time.Time
	dirty := false
	var first, last time.Time
	for {
		select {
		case <-c.done:
			return
		case <-c.ending:
			// The program's last output still goes out.
			if f := c.frame(); f != nil {
				_ = WriteMessage(c.rw, KindFrame, f)
			}
			return
		case r := <-c.reqs:
			if WriteMessage(c.rw, KindPage, c.page(r[0], r[1])) != nil {
				return
			}
			continue
		case <-c.wake:
			now := time.Now()
			if !dirty {
				dirty, first = true, now
			}
			last = now
		case <-timeout:
			timeout = nil
		}
		if !dirty {
			continue
		}
		now := time.Now()
		wait := time.Duration(0)
		if now.Sub(last) < quiet && now.Sub(first) < busy {
			wait = min(last.Add(quiet).Sub(now), first.Add(busy).Sub(now))
		}
		c.h.mu.Lock()
		syncing, since := c.h.term.Modes().Sync, c.h.syncSince
		c.h.mu.Unlock()
		if syncing && now.Sub(since) < syncLimit {
			wait = max(wait, since.Add(syncLimit).Sub(now))
		}
		if c.seq-c.acked.Load() >= inFlight {
			continue // an ack wakes us
		}
		if wait > 0 {
			if timer != nil {
				timer.Stop()
			}
			timer = time.NewTimer(wait)
			timeout = timer.C
			continue
		}
		dirty = false
		if f := c.frame(); f != nil {
			if WriteMessage(c.rw, KindFrame, f) != nil {
				return
			}
		}
	}
}

// frame builds the next frame, or nil when nothing changed.
func (c *client) frame() []byte {
	h := c.h
	h.mu.Lock()
	defer h.mu.Unlock()
	t := h.term
	if c.ready && c.version == t.Version() && len(c.events) == 0 {
		return nil
	}
	c.version = t.Version()
	// A reset goes first: it drops the styles the client knew.
	var head, defs, body []byte
	cols, rows := t.Size()
	first, next := t.History()
	if !c.ready {
		c.ready = true
		head = append(head, OpReset)
		head = append(head, uvarints(uint64(cols), uint64(rows), boolv(t.Alt()), first, next)...)
		c.cols, c.rows, c.alt, c.histFirst, c.histNext = cols, rows, t.Alt(), first, next
		c.grid = nil
		c.cx, c.cy, c.cursorFlags, c.shape = -1, -1, -1, -1
	}
	if next > c.histNext {
		body = c.scroll(body, &defs, next-c.histNext)
		c.histNext = next
	}
	if cols != c.cols || rows != c.rows {
		body = append(body, OpResize)
		body = append(body, uvarints(uint64(cols), uint64(rows))...)
		c.cols, c.rows, c.grid = cols, rows, nil
	}
	if t.Alt() != c.alt {
		body = append(body, OpAlt, byte(boolv(t.Alt())))
		c.alt, c.grid = t.Alt(), nil
	}
	if first > c.histFirst {
		body = append(body, OpHistFirst)
		body = binary.AppendUvarint(body, first)
		c.histFirst = first
	}
	body = c.rowsDiff(body, &defs)
	x, y := t.Cursor()
	m := t.Modes()
	flags := 0
	if m.CursorHidden {
		flags |= 1
	}
	if m.CursorBlink {
		flags |= 2
	}
	if x != c.cx || y != c.cy || flags != c.cursorFlags || m.CursorShape != c.shape {
		body = append(body, OpCursor)
		body = append(body, uvarints(uint64(x), uint64(y), uint64(flags), uint64(m.CursorShape))...)
		c.cx, c.cy, c.cursorFlags, c.shape = x, y, flags, m.CursorShape
	}
	modes := modeFields(m)
	if !equalUints(modes, c.modes) {
		body = append(body, OpModes)
		body = append(body, uvarints(modes...)...)
		c.modes = modes
	}
	if t.Title() != c.title {
		body = append(body, OpTitle)
		body = appendString(body, t.Title())
		c.title = t.Title()
	}
	if t.Bells() != c.bells {
		body = append(body, OpBell)
		c.bells = t.Bells()
	}
	for _, e := range c.events {
		switch e.Kind {
		case screen.EventClipboard:
			body = append(body, OpClipboard)
		case screen.EventOpen:
			body = append(body, OpOpen)
		default:
			continue
		}
		body = appendString(body, e.Text)
	}
	c.events, c.eventBytes = nil, 0
	if len(head)+len(body) == 0 {
		return nil
	}
	c.seq++
	f := binary.AppendUvarint(nil, c.seq)
	f = append(f, head...)
	f = append(f, defs...)
	return append(f, body...)
}

// scroll tells the client that n rows left its screen for the history.
func (c *client) scroll(body []byte, defs *[]byte, n uint64) []byte {
	t := c.h.term
	// A client showing the alternate screen has none of these rows.
	known := min(n, uint64(len(c.grid)))
	if c.alt {
		known = 0
	}
	var changed []byte
	m := 0
	for i := uint64(0); i < known; i++ {
		line, ok := t.HistLine(c.histNext + i)
		if !ok {
			continue
		}
		if line.Cont == c.grid[i].Cont && bytes.Equal(screen.TrimRow(line.Data), screen.AppendRow(nil, c.grid[i].C, true)) {
			continue
		}
		m++
		changed = binary.AppendUvarint(changed, i)
		changed = append(changed, byte(boolv(line.Cont)))
		changed = appendBytes(changed, line.Data)
		c.needRow(defs, line.Data)
	}
	body = append(body, OpScroll)
	body = binary.AppendUvarint(body, n)
	body = binary.AppendUvarint(body, uint64(m))
	body = append(body, changed...)
	// The client's rows moved up; blank rows came in at the bottom.
	if c.grid != nil && !c.alt {
		k := int(known)
		copy(c.grid, c.grid[k:])
		for i := len(c.grid) - k; i < len(c.grid); i++ {
			c.grid[i] = screen.Row{C: blank(c.cols)}
		}
	}
	return body
}

func blank(n int) []screen.Cell {
	cs := make([]screen.Cell, n)
	for i := range cs {
		cs[i].W = 1
	}
	return cs
}

// rowsDiff sends the rows that differ from what the client has: whole
// rows, or the changed parts of them when those are small.
func (c *client) rowsDiff(body []byte, defs *[]byte) []byte {
	t := c.h.term
	full := c.grid == nil
	if full {
		c.grid = make([]screen.Row, c.rows)
	}
	for y := 0; y < c.rows; y++ {
		cur := t.Row(y)
		prev := &c.grid[y]
		if full || prev.Cont != cur.Cont || len(prev.C) != len(cur.C) {
			body = c.row(body, defs, y, cur)
			continue
		}
		var parts [][2]int
		total := 0
		for i := 0; i < len(cur.C); {
			if cur.C[i] == prev.C[i] {
				i++
				continue
			}
			a, b := i, i
			for j := i + 1; j < len(cur.C) && j <= b+6; j++ {
				if cur.C[j] != prev.C[j] {
					b = j
				}
			}
			for a > 0 && (cur.C[a].W == 0 || prev.C[a].W == 0) {
				a--
			}
			if b+1 < len(cur.C) && (cur.C[b].W == 2 || prev.C[b].W == 2) {
				b++
			}
			parts = append(parts, [2]int{a, b})
			total += b - a + 1
			i = b + 1
		}
		if len(parts) == 0 {
			continue
		}
		if total > len(cur.C)/2 {
			body = c.row(body, defs, y, cur)
			continue
		}
		for _, p := range parts {
			cells := cur.C[p[0] : p[1]+1]
			c.needCells(defs, cells)
			body = append(body, OpPatch)
			body = append(body, uvarints(uint64(y), uint64(p[0]))...)
			body = appendBytes(body, screen.AppendRow(nil, cells, false))
		}
		copy(prev.C, cur.C)
	}
	return body
}

func (c *client) row(body []byte, defs *[]byte, y int, r *screen.Row) []byte {
	c.needCells(defs, r.C)
	body = append(body, OpRow)
	body = append(body, uvarints(uint64(y), boolv(r.Cont))...)
	body = appendBytes(body, screen.AppendRow(nil, r.C, true))
	c.grid[y] = screen.Row{C: append([]screen.Cell(nil), r.C...), Cont: r.Cont}
	return body
}

func (c *client) needCells(defs *[]byte, cs []screen.Cell) {
	last := -1
	for _, cell := range cs {
		if int(cell.S) != last {
			c.needStyle(defs, cell.S)
			last = int(cell.S)
		}
	}
}

func (c *client) needRow(defs *[]byte, data []byte) {
	runs, _ := screen.DecodeRow(data)
	for _, r := range runs {
		c.needStyle(defs, r.Style)
	}
}

// needStyle defines style id (and its link) for the client once.
func (c *client) needStyle(defs *[]byte, id uint16) {
	if id == 0 {
		return
	}
	for len(c.styles) <= int(id) {
		c.styles = append(c.styles, false)
	}
	if c.styles[id] {
		return
	}
	c.styles[id] = true
	s := c.h.term.Style(id)
	// An evicted link is sent as none (its id is never reused).
	if s.Link != 0 && c.h.term.Link(s.Link) == "" {
		s.Link = 0
	}
	if s.Link != 0 && !c.links[s.Link] {
		if c.links == nil {
			c.links = map[uint32]bool{}
		}
		c.links[s.Link] = true
		*defs = append(*defs, OpLink)
		*defs = binary.AppendUvarint(*defs, uint64(s.Link))
		*defs = appendString(*defs, c.h.term.Link(s.Link))
	}
	*defs = append(*defs, OpStyle)
	*defs = append(*defs, uvarints(uint64(id), uint64(s.FG), uint64(s.BG), uint64(s.UL), uint64(s.Flags), uint64(s.Underline), uint64(s.Link))...)
}

// page returns up to count history lines older than the id before.
func (c *client) page(before, count uint64) []byte {
	h := c.h
	h.mu.Lock()
	defer h.mu.Unlock()
	first, next := h.term.History()
	end := min(before, next)
	start := max(first, end-min(end, min(count, maxPage)))
	if start > end {
		start = end
	}
	var defs, body []byte
	body = append(body, OpLines)
	body = append(body, uvarints(start, end-start)...)
	for id := start; id < end; id++ {
		line, _ := h.term.HistLine(id)
		c.needRow(&defs, line.Data)
		body = append(body, byte(boolv(line.Cont)))
		body = appendBytes(body, line.Data)
	}
	return append(defs, body...)
}

func modeFields(m screen.Modes) []uint64 {
	var flags uint64
	for i, on := range []bool{m.AppCursor, m.AppKeypad, m.BracketedPaste, m.Focus, m.ColorScheme} {
		if on {
			flags |= 1 << i
		}
	}
	return []uint64{flags, uint64(m.Mouse), uint64(m.MouseEncoding)}
}

func equalUints(a, b []uint64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func boolv(b bool) uint64 {
	if b {
		return 1
	}
	return 0
}
