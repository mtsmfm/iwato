// Package screen is the terminal emulator of a session: the only copy of
// its state. Browsers do not parse terminal output; they draw what
// internal/termsync sends them from here (the screen as rows of cells, the
// history as lines with ids that they fetch when shown).
//
// The history keeps lines independent of the width: a row that scrolls off
// is stored with its cells, and browsers wrap long lines themselves. Only
// the screen has a width, so a resize changes the screen alone.
package screen

import (
	"cmp"
	"math"
	"slices"
	"strings"
	"unicode/utf8"
)

// Color is 0 for the default color, PaletteColor|index or RGBColor|0xRRGGBB.
type Color uint32

const (
	PaletteColor Color = 1 << 24
	RGBColor     Color = 2 << 24
)

// Style flags.
const (
	Bold uint16 = 1 << iota
	Dim
	Italic
	Blink
	Inverse
	Hidden
	Strike
	Overline
)

// Style is a cell's rendition. Link is an id from Term.Link (0: none).
type Style struct {
	FG, BG, UL Color
	Flags      uint16
	Underline  uint8 // 0 none, 1 single, 2 double, 3 curly, 4 dotted, 5 dashed
	Link       uint32
}

// Cell is one column of a row. W is 1 or 2 for a character (R == 0 is an
// erased cell, drawn as a space), and 0 for the right half of a wide one.
// S is the style's id in the session's table (Term.Style).
type Cell struct {
	R    rune
	Comb string // combining characters that follow R
	W    uint8
	S    uint16
}

// Row is one row of the screen. Cont marks a row that continues the one
// above (the text reached the right margin and wrapped).
type Row struct {
	C    []Cell
	Cont bool
}

// Modes are what a client needs to encode input and draw the cursor.
type Modes struct {
	AppCursor, AppKeypad, BracketedPaste, Focus, Sync, ColorScheme bool
	Mouse                                                          int // 0, 9, 1000, 1002 or 1003
	MouseEncoding                                                  int // 0, 1005, 1006, 1015 or 1016
	CursorHidden, CursorBlink                                      bool
	CursorShape                                                    int // DECSCUSR: 0 default, 1-6
}

// Event is something output asked of the client.
type Event struct {
	Kind EventKind
	Text string
}

type EventKind byte

const (
	// EventClipboard writes Text to the clipboard (OSC 52).
	EventClipboard EventKind = iota + 1
	// EventOpen opens the link Text (OSC 6464 "open;", from programs in dev).
	EventOpen
)

type cursor struct {
	x, y     int
	wrapNext bool
	pen      Style
	origin   bool
	g        [2]bool
	gl       int
}

type grid struct {
	rows     []*Row
	x, y     int
	wrapNext bool
	top, bot int // scroll region, inclusive
	saved    *cursor
}

// HistLine is a row that scrolled off the top, its cells encoded as by
// AppendRow. Cont marks a line that continues the previous one.
type HistLine struct {
	Cont bool
	Data []byte
}

// Term holds the state. It is not safe for concurrent use.
type Term struct {
	cols, rows int
	main, alt  grid
	altActive  bool
	pen        Style
	penID      uint16
	penOK      bool
	g          [2]bool // charset G0/G1 is DEC special graphics
	gl         int
	autowrap   bool
	origin     bool
	insert     bool
	modes      Modes
	tabs       []bool

	styles    []Style
	styleIDs  map[Style]uint16
	links     map[uint32]link
	linkIDs   map[string]uint32
	lastLink  uint32 // the id the last new link got; ids are never reused
	linkBytes int    // the table's size, as counted by linkSize
	linkUse   uint64 // counts linkID calls, for link.used

	hist                       []HistLine
	histFirst                  uint64 // id of hist[0]
	histBytes                  int
	MaxHistLines, MaxHistBytes int

	title   string
	bells   int
	events  []Event
	version uint64

	// Reply receives answers to the program's queries (device attributes,
	// cursor position, modes, colors). It is called during Write.
	Reply func([]byte)
	// Colors answer OSC 10/11/12 and 4 queries: foreground, background,
	// cursor and the 16 palette colors, as 0xRRGGBB.
	FG, BG, CursorColor uint32
	Palette             [16]uint32

	p parser
}

// New returns a terminal of the given size.
func New(cols, rows int) *Term {
	cols, rows = max(cols, 1), max(rows, 1)
	t := &Term{cols: cols, rows: rows, autowrap: true, styleIDs: map[Style]uint16{}, links: map[uint32]link{}, linkIDs: map[string]uint32{},
		MaxHistLines: 50000, MaxHistBytes: 16 << 20,
		FG: 0xd2dbe5, BG: 0x111419, CursorColor: 0xa1ddba,
		Palette: [16]uint32{0x29343c, 0xf58b95, 0x9bdab1, 0xe6c28c, 0x8eb6e2, 0xc6a3df, 0x86c6cf, 0xd2dbe5,
			0x748597, 0xffabb3, 0xb8f1c8, 0xf9d7a3, 0xb1d0f2, 0xe1bfee, 0xabe3e8, 0xf1f5fa}}
	t.styles = []Style{{}}
	t.styleIDs[Style{}] = 0
	t.main = newGrid(cols, rows)
	t.alt = newGrid(cols, rows)
	t.resetTabs()
	return t
}

func newGrid(cols, rows int) grid {
	g := grid{rows: make([]*Row, rows), bot: rows - 1}
	for i := range g.rows {
		g.rows[i] = &Row{C: blankCells(cols, 0)}
	}
	return g
}

func blankCells(n int, s uint16) []Cell {
	cs := make([]Cell, n)
	for i := range cs {
		cs[i] = Cell{W: 1, S: s}
	}
	return cs
}

func (t *Term) resetTabs() {
	t.tabs = make([]bool, t.cols)
	for i := 8; i < t.cols; i += 8 {
		t.tabs[i] = true
	}
}

func (t *Term) scr() *grid {
	if t.altActive {
		return &t.alt
	}
	return &t.main
}

// ---------------------------------------------------------------- reading

// Size reports the screen's size.
func (t *Term) Size() (cols, rows int) { return t.cols, t.rows }

// Alt reports whether the alternate screen is shown.
func (t *Term) Alt() bool { return t.altActive }

// Row returns row y of the screen shown. The caller must not keep it
// across a Write.
func (t *Term) Row(y int) *Row { return t.scr().rows[y] }

// Cursor reports the cursor position on the screen shown.
func (t *Term) Cursor() (x, y int) {
	s := t.scr()
	return s.x, s.y
}

// Modes reports the modes.
func (t *Term) Modes() Modes { return t.modes }

// Title is the window title (OSC 0/2).
func (t *Term) Title() string { return t.title }

// Bells counts the bells rung so far.
func (t *Term) Bells() int { return t.bells }

// Version changes whenever the state may have changed.
func (t *Term) Version() uint64 { return t.version }

// Style returns style id's rendition.
func (t *Term) Style(id uint16) Style { return t.styles[id] }

// Styles is the number of styles in the table.
func (t *Term) Styles() int { return len(t.styles) }

// Link returns the URL of link id: "" for none, also once the link has
// been evicted (ids are never reused, so an id never names another URL).
func (t *Term) Link(id uint32) string { return t.links[id].url }

// Links is the number of links in the table.
func (t *Term) Links() int { return len(t.links) }

// LinkBytes is the size of the link table, URLs and entries.
func (t *Term) LinkBytes() int { return t.linkBytes }

// History reports the ids of the kept history: first, and next (the id the
// next line to scroll off gets). Lines first..next-1 are kept.
func (t *Term) History() (first, next uint64) {
	return t.histFirst, t.histFirst + uint64(len(t.hist))
}

// HistLine returns the kept line id.
func (t *Term) HistLine(id uint64) (HistLine, bool) {
	if id < t.histFirst || id >= t.histFirst+uint64(len(t.hist)) {
		return HistLine{}, false
	}
	return t.hist[id-t.histFirst], true
}

// TakeEvents returns and forgets the events since the last call.
func (t *Term) TakeEvents() []Event {
	e := t.events
	t.events = nil
	return e
}

// ---------------------------------------------------------------- styles

// styleID interns s. The table only grows; past 65535 styles new ones
// fall back to the default.
func (t *Term) styleID(s Style) uint16 {
	if id, ok := t.styleIDs[s]; ok {
		return id
	}
	if len(t.styles) >= 65535 {
		return 0
	}
	id := uint16(len(t.styles))
	t.styles = append(t.styles, s)
	t.styleIDs[s] = id
	return id
}

func (t *Term) setPen(s Style) {
	if s != t.pen {
		t.pen, t.penOK = s, false
	}
}

func (t *Term) penStyle() uint16 {
	if !t.penOK {
		t.penID, t.penOK = t.styleID(t.pen), true
	}
	return t.penID
}

// blankStyle is the style of cells erased with the pen: its background.
func (t *Term) blankStyle() uint16 {
	if t.pen.BG == 0 {
		return 0
	}
	return t.styleID(Style{BG: t.pen.BG})
}

// The OSC 8 link table's bounds. Output can make a new link with a few
// bytes, so the table is bounded by its size and its entries: when either
// would be passed, links no cell refers to any more are evicted, and if that
// is not enough the least recently used ones (their cells then show no
// link) until the table is at half its bounds.
const (
	maxLinkURL   = 8192
	maxLinks     = 1 << 16
	maxLinkBytes = 4 << 20
	linkOverhead = 64 // what an entry costs besides its URL (maps, header)
)

type link struct {
	url  string
	used uint64 // linkUse when last looked up
}

func linkSize(url string) int { return len(url) + linkOverhead }

func (t *Term) linkID(url string) uint32 {
	t.linkUse++
	if id, ok := t.linkIDs[url]; ok {
		t.links[id] = link{url: url, used: t.linkUse}
		return id
	}
	if t.lastLink == math.MaxUint32 {
		return 0
	}
	if len(t.links) >= maxLinks || t.linkBytes+linkSize(url) > maxLinkBytes {
		t.evictLinks()
	}
	t.lastLink++
	id := t.lastLink
	t.links[id] = link{url: url, used: t.linkUse}
	t.linkIDs[url] = id
	t.linkBytes += linkSize(url)
	return id
}

func (t *Term) dropLink(id uint32) {
	l := t.links[id]
	delete(t.links, id)
	delete(t.linkIDs, l.url)
	t.linkBytes -= linkSize(l.url)
}

// evictLinks drops the links no cell of the screens or the history refers
// to, then, while the table is over half its bounds, the least recently
// used ones other than the pen's.
func (t *Term) evictLinks() {
	inUse := t.linksInUse()
	for id := range t.links {
		if !inUse[id] {
			t.dropLink(id)
		}
	}
	if len(t.links) <= maxLinks/2 && t.linkBytes <= maxLinkBytes/2 {
		return
	}
	ids := make([]uint32, 0, len(t.links))
	for id := range t.links {
		if id != t.pen.Link {
			ids = append(ids, id)
		}
	}
	slices.SortFunc(ids, func(a, b uint32) int { return cmp.Compare(t.links[a].used, t.links[b].used) })
	for _, id := range ids {
		if len(t.links) <= maxLinks/2 && t.linkBytes <= maxLinkBytes/2 {
			break
		}
		t.dropLink(id)
	}
}

// linksInUse is the set of links the cells of both screens and the history
// refer to (through their styles), and the pens'.
func (t *Term) linksInUse() map[uint32]bool {
	styles := make([]bool, len(t.styles))
	for _, g := range []*grid{&t.main, &t.alt} {
		for _, r := range g.rows {
			for _, c := range r.C {
				styles[c.S] = true
			}
		}
	}
	for _, l := range t.hist {
		eachStyle(l.Data, func(s uint16) {
			if int(s) < len(styles) {
				styles[s] = true
			}
		})
	}
	inUse := map[uint32]bool{t.pen.Link: true}
	for _, g := range []*grid{&t.main, &t.alt} {
		if g.saved != nil {
			inUse[g.saved.pen.Link] = true
		}
	}
	for id, used := range styles {
		if used && t.styles[id].Link != 0 {
			inUse[t.styles[id].Link] = true
		}
	}
	return inUse
}

// eachStyle calls f with the style id of each run of an encoded row.
func eachStyle(b []byte, f func(uint16)) {
	for len(b) > 0 {
		s, n := uvarint(b)
		if n <= 0 || len(b) < n+1 {
			return
		}
		b = b[n+1:]
		l, n := uvarint(b)
		if n <= 0 || uint64(len(b)-n) < l {
			return
		}
		f(uint16(s))
		b = b[n+int(l):]
	}
}

// ---------------------------------------------------------------- writing

// Write feeds output from the program.
func (t *Term) Write(b []byte) (int, error) {
	if len(b) > 0 {
		t.version++
	}
	for _, c := range b {
		t.p.feed(t, c)
	}
	return len(b), nil
}

func (t *Term) reply(s string) {
	if t.Reply != nil {
		t.Reply([]byte(s))
	}
}

var decGraphics = map[rune]rune{'`': '◆', 'a': '▒', 'b': '␉', 'c': '␌', 'd': '␍', 'e': '␊', 'f': '°', 'g': '±',
	'h': '␤', 'i': '␋', 'j': '┘', 'k': '┐', 'l': '┌', 'm': '└', 'n': '┼', 'o': '⎺', 'p': '⎻', 'q': '─',
	'r': '⎼', 's': '⎽', 't': '├', 'u': '┤', 'v': '┴', 'w': '┬', 'x': '│', 'y': '≤', 'z': '≥', '{': 'π',
	'|': '≠', '}': '£', '~': '·'}

func (t *Term) print(r rune) {
	if t.g[t.gl] {
		if m, ok := decGraphics[r]; ok {
			r = m
		}
	}
	s := t.scr()
	w := Width(r)
	if w == 0 {
		t.combine(r)
		return
	}
	if s.wrapNext && t.autowrap {
		t.wrapLine()
	}
	if w == 2 && s.x == t.cols-1 {
		if !t.autowrap || t.cols < 2 {
			return
		}
		// The last cell stays empty and the wide character wraps.
		t.setCell(s.y, s.x, Cell{W: 1, S: t.blankStyle()})
		t.wrapLine()
	}
	line := s.rows[s.y].C
	if t.insert {
		copy(line[s.x+w:], line[s.x:])
		fixLine(line)
	}
	pen := t.penStyle()
	t.setCell(s.y, s.x, Cell{R: r, W: uint8(w), S: pen})
	if w == 2 {
		// The right half may cover the left half of another wide character.
		if s.x+2 < len(line) && line[s.x+1].W == 2 {
			line[s.x+2] = Cell{W: 1, S: line[s.x+2].S}
		}
		line[s.x+1] = Cell{W: 0, S: pen}
	}
	if t.insert {
		fixLine(line)
	}
	if s.x+w >= t.cols {
		s.x = t.cols - 1
		s.wrapNext = t.autowrap
	} else {
		s.x += w
		s.wrapNext = false
	}
}

func (t *Term) wrapLine() {
	s := t.scr()
	s.wrapNext = false
	s.x = 0
	t.index()
	s.rows[s.y].Cont = true
}

// combine appends a zero-width character to the previous cell.
func (t *Term) combine(r rune) {
	s := t.scr()
	x := s.x - 1
	if s.wrapNext {
		x = s.x
	}
	if x < 0 {
		return
	}
	line := s.rows[s.y].C
	if line[x].W == 0 && x > 0 {
		x--
	}
	if line[x].R != 0 && len(line[x].Comb) < 32 {
		line[x].Comb += string(r)
	}
}

// setCell writes c at (y, x) and clears the other half of any wide
// character it overwrites.
func (t *Term) setCell(y, x int, c Cell) {
	line := t.scr().rows[y].C
	if x < 0 || x >= len(line) {
		return
	}
	if line[x].W == 0 && x > 0 && line[x-1].W == 2 {
		line[x-1] = Cell{W: 1, S: line[x-1].S}
	}
	if line[x].W == 2 && x+1 < len(line) && c.W != 2 {
		line[x+1] = Cell{W: 1, S: line[x+1].S}
	}
	line[x] = c
}

// fixLine blanks the halves of wide characters that lost their other half.
func fixLine(cs []Cell) {
	for i := range cs {
		switch cs[i].W {
		case 0:
			if i == 0 || cs[i-1].W != 2 {
				cs[i] = Cell{W: 1, S: cs[i].S}
			}
		case 2:
			if i+1 >= len(cs) || cs[i+1].W != 0 {
				cs[i] = Cell{W: 1, S: cs[i].S}
			}
		}
	}
}

// ---------------------------------------------------------------- movement

func (t *Term) index() {
	s := t.scr()
	if s.y == s.bot {
		t.scrollUp(s.top, s.bot, 1)
	} else if s.y < t.rows-1 {
		s.y++
	}
}

// lineFeed is index, except that a row the cursor moves down to (without
// scrolling) no longer continues the one above.
func (t *Term) lineFeed() {
	s := t.scr()
	if s.y != s.bot && s.y < t.rows-1 {
		s.y++
		s.rows[s.y].Cont = false
		return
	}
	t.index()
}

func (t *Term) reverseIndex() {
	s := t.scr()
	if s.y == s.top {
		t.scrollDown(s.top, s.bot, 1)
	} else if s.y > 0 {
		s.y--
	}
}

// scrollUp moves rows top..bot up by n; rows leaving the top of the main
// screen go to the history.
func (t *Term) scrollUp(top, bot, n int) {
	s := t.scr()
	n = min(n, bot-top+1)
	for i := 0; i < n; i++ {
		gone := s.rows[top]
		if top == 0 && !t.altActive {
			t.pushHistory(gone, top+1 <= bot && s.rows[top+1].Cont)
		}
		copy(s.rows[top:bot], s.rows[top+1:bot+1])
		s.rows[bot] = &Row{C: blankCells(t.cols, t.blankStyle())}
	}
}

func (t *Term) scrollDown(top, bot, n int) {
	s := t.scr()
	n = min(n, bot-top+1)
	for i := 0; i < n; i++ {
		copy(s.rows[top+1:bot+1], s.rows[top:bot])
		s.rows[top] = &Row{C: blankCells(t.cols, t.blankStyle())}
	}
}

// pushHistory adds row r. A row that the next one continues keeps its
// trailing blanks: they are part of the line.
func (t *Term) pushHistory(r *Row, nextCont bool) {
	data := AppendRow(nil, r.C, !nextCont)
	t.hist = append(t.hist, HistLine{Cont: r.Cont, Data: data})
	t.histBytes += len(data)
	drop := 0
	for drop < len(t.hist) && (len(t.hist)-drop > t.MaxHistLines || t.histBytes > t.MaxHistBytes) {
		t.histBytes -= len(t.hist[drop].Data)
		drop++
	}
	if drop > 0 {
		clear(t.hist[:drop])
		t.hist = t.hist[drop:]
		t.histFirst += uint64(drop)
	}
	if cap(t.hist) > 2*len(t.hist)+1024 {
		t.hist = append([]HistLine(nil), t.hist...)
	}
}

func (t *Term) clearHistory() {
	t.histFirst += uint64(len(t.hist))
	t.hist, t.histBytes = nil, 0
}

func (t *Term) moveTo(y, x int) {
	s := t.scr()
	if t.origin {
		y += s.top
		y = min(max(y, s.top), s.bot)
	} else {
		y = min(max(y, 0), t.rows-1)
	}
	s.x, s.y, s.wrapNext = min(max(x, 0), t.cols-1), y, false
}

func (t *Term) cursorUp(n int) {
	s := t.scr()
	lim := 0
	if s.y >= s.top {
		lim = s.top
	}
	s.y, s.wrapNext = max(s.y-n, lim), false
}

func (t *Term) cursorDown(n int) {
	s := t.scr()
	lim := t.rows - 1
	if s.y <= s.bot {
		lim = s.bot
	}
	s.y, s.wrapNext = min(s.y+n, lim), false
}

func (t *Term) tab(n int) {
	s := t.scr()
	for ; n > 0 && s.x < t.cols-1; n-- {
		s.x++
		for s.x < t.cols-1 && !t.tabs[s.x] {
			s.x++
		}
	}
	s.wrapNext = false
}

func (t *Term) backTab(n int) {
	s := t.scr()
	for ; n > 0 && s.x > 0; n-- {
		s.x--
		for s.x > 0 && !t.tabs[s.x] {
			s.x--
		}
	}
	s.wrapNext = false
}

// ---------------------------------------------------------------- erasing

func (t *Term) eraseCells(y, from, to int) {
	line := t.scr().rows[y].C
	from, to = max(from, 0), min(to, t.cols)
	if from >= to {
		return
	}
	t.setCell(y, from, Cell{W: 1})
	if to-1 > from {
		t.setCell(y, to-1, Cell{W: 1})
	}
	b := t.blankStyle()
	for i := from; i < to; i++ {
		line[i] = Cell{W: 1, S: b}
	}
	fixLine(line)
}

// from is where erasing "to the end" starts: nothing on the cursor row
// while a wrap is pending.
func (t *Term) from() int {
	if s := t.scr(); !s.wrapNext {
		return s.x
	}
	return t.cols
}

func (t *Term) eraseDisplay(mode int) {
	s := t.scr()
	b := t.blankStyle()
	switch mode {
	case 0:
		if t.from() == 0 {
			s.rows[s.y].Cont = false
		}
		t.eraseCells(s.y, t.from(), t.cols)
		for y := s.y + 1; y < t.rows; y++ {
			s.rows[y] = &Row{C: blankCells(t.cols, b)}
		}
	case 1:
		for y := 0; y < s.y; y++ {
			s.rows[y] = &Row{C: blankCells(t.cols, b)}
		}
		t.eraseCells(s.y, 0, s.x+1)
		s.rows[s.y].Cont = false
	case 2:
		for y := range s.rows {
			s.rows[y] = &Row{C: blankCells(t.cols, b)}
		}
	case 3:
		if !t.altActive {
			t.clearHistory()
		}
	}
}

func (t *Term) eraseLine(mode int) {
	s := t.scr()
	switch mode {
	case 0:
		if t.from() == 0 {
			s.rows[s.y].Cont = false
		}
		t.eraseCells(s.y, t.from(), t.cols)
	case 1:
		t.eraseCells(s.y, 0, s.x+1)
	case 2:
		s.rows[s.y].Cont = false
		t.eraseCells(s.y, 0, t.cols)
	}
}

func (t *Term) insertChars(n int) {
	s := t.scr()
	line := s.rows[s.y].C
	n = min(n, t.cols-s.x)
	t.setCell(s.y, s.x, line[s.x])
	copy(line[s.x+n:], line[s.x:])
	b := t.blankStyle()
	for i := s.x; i < s.x+n; i++ {
		line[i] = Cell{W: 1, S: b}
	}
	fixLine(line)
	s.wrapNext = false
}

func (t *Term) deleteChars(n int) {
	s := t.scr()
	line := s.rows[s.y].C
	n = min(n, t.cols-s.x)
	t.setCell(s.y, s.x, line[s.x])
	if s.x+n < t.cols {
		t.setCell(s.y, s.x+n, line[s.x+n])
	}
	copy(line[s.x:], line[s.x+n:])
	b := t.blankStyle()
	for i := t.cols - n; i < t.cols; i++ {
		line[i] = Cell{W: 1, S: b}
	}
	fixLine(line)
	s.wrapNext = false
}

func (t *Term) insertLines(n int) {
	s := t.scr()
	if s.y < s.top || s.y > s.bot {
		return
	}
	t.scrollDown(s.y, s.bot, n)
	s.x, s.wrapNext = 0, false
}

func (t *Term) deleteLines(n int) {
	s := t.scr()
	if s.y < s.top || s.y > s.bot {
		return
	}
	n = min(n, s.bot-s.y+1)
	b := t.blankStyle()
	for i := 0; i < n; i++ {
		copy(s.rows[s.y:s.bot], s.rows[s.y+1:s.bot+1])
		s.rows[s.bot] = &Row{C: blankCells(t.cols, b)}
	}
	s.x, s.wrapNext = 0, false
}

// ---------------------------------------------------------------- modes

func (t *Term) saveCursor() {
	s := t.scr()
	s.saved = &cursor{x: s.x, y: s.y, wrapNext: s.wrapNext, pen: t.pen, origin: t.origin, g: t.g, gl: t.gl}
}

func (t *Term) restoreCursor() {
	s := t.scr()
	c := s.saved
	if c == nil {
		c = &cursor{}
	}
	s.x, s.y, s.wrapNext = min(c.x, t.cols-1), min(max(c.y, 0), t.rows-1), false
	t.setPen(c.pen)
	t.origin, t.g, t.gl = c.origin, c.g, c.gl
}

// setAlt switches screens; 1049 saves the cursor first and restores it on
// the way back. The alternate screen starts blank.
func (t *Term) setAlt(on, save bool) {
	if on && save {
		t.saveCursor()
	}
	if on && !t.altActive {
		t.altActive = true
		t.alt = newGrid(t.cols, t.rows)
		t.alt.x, t.alt.y = t.main.x, t.main.y
	} else if !on && t.altActive {
		t.altActive = false
		if save {
			t.restoreCursor()
		}
	}
}

func (t *Term) setPrivate(mode int, on bool) {
	m := &t.modes
	switch mode {
	case 1:
		m.AppCursor = on
	case 6:
		t.origin = on
		t.moveTo(0, 0)
	case 7:
		t.autowrap = on
		if !on {
			t.scr().wrapNext = false
		}
	case 12:
		m.CursorBlink = on
	case 25:
		m.CursorHidden = !on
	case 47, 1047:
		t.setAlt(on, false)
	case 1048:
		if on {
			t.saveCursor()
		} else {
			t.restoreCursor()
		}
	case 1049:
		t.setAlt(on, true)
	case 9, 1000, 1002, 1003:
		if on {
			m.Mouse = mode
		} else if m.Mouse == mode {
			m.Mouse = 0
		}
	case 1005, 1006, 1015, 1016:
		if on {
			m.MouseEncoding = mode
		} else if m.MouseEncoding == mode {
			m.MouseEncoding = 0
		}
	case 1004:
		m.Focus = on
	case 2004:
		m.BracketedPaste = on
	case 2026:
		m.Sync = on
	case 2031:
		m.ColorScheme = on
	}
}

// privateMode reports a DEC private mode for DECRQM: 1 set, 2 reset, 0 not
// recognized.
func (t *Term) privateMode(mode int) int {
	m := t.modes
	b := func(v bool) int {
		if v {
			return 1
		}
		return 2
	}
	switch mode {
	case 1:
		return b(m.AppCursor)
	case 6:
		return b(t.origin)
	case 7:
		return b(t.autowrap)
	case 12:
		return b(m.CursorBlink)
	case 25:
		return b(!m.CursorHidden)
	case 47, 1047, 1049:
		return b(t.altActive)
	case 9, 1000, 1002, 1003:
		return b(m.Mouse == mode)
	case 1005, 1006, 1015, 1016:
		return b(m.MouseEncoding == mode)
	case 1004:
		return b(m.Focus)
	case 2004:
		return b(m.BracketedPaste)
	case 2026:
		return b(m.Sync)
	case 2031:
		return b(m.ColorScheme)
	}
	return 0
}

func (t *Term) softReset() {
	t.setPen(Style{})
	t.insert, t.origin, t.autowrap = false, false, true
	t.modes.CursorHidden, t.modes.AppKeypad, t.modes.AppCursor = false, false, false
	t.g, t.gl = [2]bool{}, 0
	s := t.scr()
	s.top, s.bot, s.saved = 0, t.rows-1, nil
}

// fullReset (RIS) clears the screens and modes; the history stays.
func (t *Term) fullReset() {
	t.altActive = false
	t.main, t.alt = newGrid(t.cols, t.rows), newGrid(t.cols, t.rows)
	t.setPen(Style{})
	t.g, t.gl, t.autowrap, t.origin, t.insert = [2]bool{}, 0, true, false, false
	t.modes = Modes{}
	t.title = ""
	t.resetTabs()
}

// ---------------------------------------------------------------- resize

// Resize changes the size of the screens. The history is not touched (it
// has no width). Fewer rows first drop blank rows below the cursor, then
// move the top rows to the history; more rows are added blank at the
// bottom. Rows are cut or padded to the new width.
func (t *Term) Resize(cols, rows int) {
	cols, rows = max(cols, 1), max(rows, 1)
	if cols == t.cols && rows == t.rows {
		return
	}
	t.version++
	t.resizeRows(&t.main, rows, true)
	t.resizeRows(&t.alt, rows, false)
	for _, g := range []*grid{&t.main, &t.alt} {
		for _, r := range g.rows {
			r.C = resizeCells(r.C, cols)
		}
		g.x, g.wrapNext = min(g.x, cols-1), false
		g.top, g.bot = 0, rows-1
		if c := g.saved; c != nil {
			c.x, c.y, c.wrapNext = min(c.x, cols-1), min(c.y, rows-1), false
		}
	}
	t.cols, t.rows = cols, rows
	t.resetTabs()
}

func (t *Term) resizeRows(g *grid, rows int, main bool) {
	n := len(g.rows)
	for n > rows && g.y < n-1 && blankRow(g.rows[n-1]) {
		n--
	}
	g.rows = g.rows[:n]
	if extra := n - rows; extra > 0 {
		if main {
			for i := 0; i < extra; i++ {
				t.pushHistory(g.rows[i], i+1 < n && g.rows[i+1].Cont)
			}
			g.rows = g.rows[extra:]
			g.y = max(g.y-extra, 0)
			if g.saved != nil {
				g.saved.y = max(g.saved.y-extra, 0)
			}
		} else {
			// The alternate screen keeps the part with the cursor.
			cut := max(0, min(extra, g.y-rows+1))
			g.rows = g.rows[cut : cut+rows]
			g.y -= cut
		}
	}
	for len(g.rows) < rows {
		g.rows = append(g.rows, &Row{C: blankCells(t.cols, 0)})
	}
	g.y = min(g.y, rows-1)
}

func blankRow(r *Row) bool {
	for _, c := range r.C {
		if c.R != 0 || c.S != 0 {
			return false
		}
	}
	return true
}

func resizeCells(cs []Cell, cols int) []Cell {
	if len(cs) >= cols {
		cs = cs[:cols]
		if cols > 0 && cs[cols-1].W == 2 {
			cs[cols-1] = Cell{W: 1, S: cs[cols-1].S}
		}
		return cs
	}
	return append(cs, blankCells(cols-len(cs), 0)...)
}

// ---------------------------------------------------------------- encoding

// AppendRow appends cells encoded as runs, the format of the history and
// of the wire (internal/termsync): runs of cells of one style and width,
// each a uvarint style id, a kind byte (bits 0-1 the width, bit 2 set for
// a single cell with combining characters), a uvarint byte length and the
// UTF-8 text (one code point per cell, or the cell's whole text). Erased
// cells are spaces; the right halves of wide characters are left out.
// With trim, trailing default blank cells are left out.
func AppendRow(b []byte, cs []Cell, trim bool) []byte {
	if trim {
		n := len(cs)
		for n > 0 && (cs[n-1].R == 0 || cs[n-1].R == ' ') && cs[n-1].Comb == "" && cs[n-1].S == 0 && cs[n-1].W == 1 {
			n--
		}
		cs = cs[:n]
	}
	var text []byte
	for i := 0; i < len(cs); {
		c := cs[i]
		if c.W == 0 {
			i++
			continue
		}
		if c.Comb != "" {
			text = appendCellText(text[:0], c)
			b = appendRun(b, c.S, c.W|4, text)
			i++
			continue
		}
		text = text[:0]
		j := i
		for j < len(cs) && cs[j].Comb == "" && (cs[j].W == 0 || cs[j].S == c.S && cs[j].W == c.W) {
			if cs[j].W != 0 {
				text = appendCellText(text, cs[j])
			}
			j++
		}
		b = appendRun(b, c.S, c.W, text)
		i = j
	}
	return b
}

func appendCellText(b []byte, c Cell) []byte {
	if c.R == 0 {
		return append(b, ' ')
	}
	b = utf8.AppendRune(b, c.R)
	return append(b, c.Comb...)
}

func appendRun(b []byte, style uint16, kind uint8, text []byte) []byte {
	b = AppendUvarint(b, uint64(style))
	b = append(b, kind)
	b = AppendUvarint(b, uint64(len(text)))
	return append(b, text...)
}

// AppendUvarint appends v as a uvarint (encoding/binary's format).
func AppendUvarint(b []byte, v uint64) []byte {
	for v >= 0x80 {
		b = append(b, byte(v)|0x80)
		v >>= 7
	}
	return append(b, byte(v))
}

// Run is one decoded run.
type Run struct {
	Style  uint16
	Width  int
	Single bool // one cell with combining characters
	Text   string
}

// DecodeRow decodes AppendRow's encoding.
func DecodeRow(b []byte) ([]Run, bool) {
	var runs []Run
	for len(b) > 0 {
		s, n := uvarint(b)
		if n <= 0 || len(b) < n+1 {
			return nil, false
		}
		kind := b[n]
		b = b[n+1:]
		l, n := uvarint(b)
		if n <= 0 || uint64(len(b)-n) < l {
			return nil, false
		}
		runs = append(runs, Run{Style: uint16(s), Width: int(kind & 3), Single: kind&4 != 0, Text: string(b[n : n+int(l)])})
		b = b[n+int(l):]
	}
	return runs, true
}

func uvarint(b []byte) (uint64, int) {
	var v uint64
	for i := 0; i < len(b) && i < 10; i++ {
		v |= uint64(b[i]&0x7f) << (7 * i)
		if b[i] < 0x80 {
			return v, i + 1
		}
	}
	return 0, 0
}

// Text is the plain text of an encoded row.
func Text(b []byte) string {
	runs, _ := DecodeRow(b)
	var s []byte
	for _, r := range runs {
		s = append(s, r.Text...)
	}
	return string(s)
}

// TrimRow re-encodes an encoded row without its trailing default blanks
// (rows the next one continued keep them in the history).
func TrimRow(b []byte) []byte {
	runs, ok := DecodeRow(b)
	if !ok {
		return b
	}
	for len(runs) > 0 {
		r := &runs[len(runs)-1]
		if r.Style != 0 || r.Width != 1 || r.Single {
			break
		}
		r.Text = strings.TrimRight(r.Text, " ")
		if r.Text != "" {
			break
		}
		runs = runs[:len(runs)-1]
	}
	var out []byte
	for _, r := range runs {
		kind := uint8(r.Width)
		if r.Single {
			kind |= 4
		}
		out = appendRun(out, r.Style, kind, []byte(r.Text))
	}
	return out
}
