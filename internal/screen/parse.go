package screen

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	stGround = iota
	stEscape
	stEscInter
	stCSI
	stOSC
	stOSCEsc
	stString // DCS, SOS, PM, APC: ignored until ST
	stStringEsc
)

const maxOSC = 1 << 20 // OSC 52 carries clipboard contents

type parser struct {
	state  int
	params []int32 // -1 marks an omitted parameter
	sub    []bool  // params[i] was introduced by ':'
	cur    int32
	has    bool
	colon  bool // the parameter being read follows a ':'
	priv   byte // '?', '>', '=', '<' prefix
	inter  []byte
	osc    []byte
	utf8   [4]byte
	un     int
	need   int
}

func (p *parser) reset() {
	p.params, p.sub, p.cur, p.has, p.colon, p.priv, p.inter = p.params[:0], p.sub[:0], 0, false, false, 0, p.inter[:0]
}

func (p *parser) feed(t *Term, c byte) {
	// UTF-8 continuation in the ground state (and in OSC strings).
	if p.need > 0 {
		if c&0xC0 == 0x80 {
			p.utf8[p.un] = c
			p.un++
			if p.un == p.need {
				r, _ := utf8.DecodeRune(p.utf8[:p.un])
				p.need = 0
				if p.state == stGround {
					t.print(r)
				} else if p.state == stOSC && len(p.osc) < maxOSC {
					p.osc = append(p.osc, p.utf8[:p.un]...)
				}
			}
			return
		}
		p.need = 0
		if p.state == stGround {
			t.print(utf8.RuneError)
		}
	}
	switch p.state {
	case stGround:
		switch {
		case c == 0x1b:
			p.state = stEscape
			p.reset()
		case c < 0x20 || c == 0x7f:
			t.control(c)
		case c < 0x80:
			t.print(rune(c))
		case c >= 0xC2 && c <= 0xF4:
			p.startUTF8(c)
		default:
			t.print(utf8.RuneError)
		}
	case stEscape:
		switch {
		case c == '[':
			p.state = stCSI
		case c == ']':
			p.state, p.osc = stOSC, p.osc[:0]
		case c == 'P' || c == 'X' || c == '^' || c == '_':
			p.state = stString
		case c >= 0x20 && c <= 0x2f:
			p.inter = append(p.inter, c)
			p.state = stEscInter
		case c < 0x20:
			t.control(c)
		default:
			p.state = stGround
			t.esc(c, 0)
		}
	case stEscInter:
		switch {
		case c >= 0x20 && c <= 0x2f:
			if len(p.inter) < 4 {
				p.inter = append(p.inter, c)
			}
		case c < 0x20:
			t.control(c)
		default:
			p.state = stGround
			t.esc(c, p.inter[0])
		}
	case stCSI:
		switch {
		case c >= '0' && c <= '9':
			if p.cur < 100000 {
				p.cur = p.cur*10 + int32(c-'0')
			}
			p.has = true
		case c == ';' || c == ':':
			p.push()
			p.colon = c == ':'
		case c >= '<' && c <= '?':
			if len(p.params) == 0 && !p.has {
				p.priv = c
			}
		case c >= 0x20 && c <= 0x2f:
			if len(p.inter) < 4 {
				p.inter = append(p.inter, c)
			}
		case c >= 0x40 && c <= 0x7e:
			p.push()
			p.state = stGround
			t.csi(p, c)
		case c == 0x1b:
			p.state = stEscape
			p.reset()
		case c < 0x20:
			t.control(c)
		}
	case stOSC:
		switch {
		case c == 0x07:
			p.state = stGround
			t.osc(string(p.osc))
		case c == 0x1b:
			p.state = stOSCEsc
		case c >= 0xC2 && c <= 0xF4:
			p.startUTF8(c)
		case c < 0x20:
			// Other C0 controls inside a string are ignored.
		default:
			if len(p.osc) < maxOSC {
				p.osc = append(p.osc, c)
			}
		}
	case stOSCEsc:
		p.state = stGround
		t.osc(string(p.osc))
		if c != '\\' {
			p.feed(t, 0x1b)
			p.feed(t, c)
		}
	case stString:
		if c == 0x1b {
			p.state = stStringEsc
		} else if c == 0x07 {
			p.state = stGround
		}
	case stStringEsc:
		if c == '\\' {
			p.state = stGround
		} else if c != 0x1b {
			p.state = stString
		}
	}
}

func (p *parser) startUTF8(c byte) {
	p.utf8[0], p.un, p.need = c, 1, 2
	if c >= 0xE0 {
		p.need = 3
	}
	if c >= 0xF0 {
		p.need = 4
	}
}

func (p *parser) push() {
	v := p.cur
	if !p.has {
		v = -1
	}
	if len(p.params) < 32 {
		p.params = append(p.params, v)
		p.sub = append(p.sub, p.colon)
	}
	p.cur, p.has, p.colon = 0, false, false
}

// param returns parameter i, or def when it is missing or zero.
func (p *parser) param(i, def int) int {
	if i >= len(p.params) || p.params[i] <= 0 {
		return def
	}
	return int(p.params[i])
}

func (t *Term) control(c byte) {
	s := t.scr()
	switch c {
	case 0x07:
		t.bells++
	case 0x08:
		if s.wrapNext {
			s.wrapNext = false
		} else if s.x > 0 {
			s.x--
		}
	case 0x09:
		t.tab(1)
	case 0x0a, 0x0b, 0x0c:
		t.lineFeed()
		s.wrapNext = false
	case 0x0d:
		s.x, s.wrapNext = 0, false
	case 0x0e:
		t.gl = 1
	case 0x0f:
		t.gl = 0
	}
}

func (t *Term) esc(c, inter byte) {
	s := t.scr()
	switch inter {
	case '(', ')':
		g := 0
		if inter == ')' {
			g = 1
		}
		t.g[g] = c == '0'
		return
	case 0:
	default:
		return
	}
	switch c {
	case '7':
		t.saveCursor()
	case '8':
		t.restoreCursor()
	case 'D':
		t.index()
	case 'E':
		t.index()
		s.x = 0
	case 'M':
		t.reverseIndex()
	case 'H':
		t.tabs[s.x] = true
	case '=':
		t.modes.AppKeypad = true
	case '>':
		t.modes.AppKeypad = false
	case 'c':
		t.fullReset()
	}
	s.wrapNext = false
}

func (t *Term) csi(p *parser, c byte) {
	s := t.scr()
	n := p.param(0, 1)
	inter := byte(0)
	if len(p.inter) > 0 {
		inter = p.inter[0]
	}
	switch p.priv {
	case '?':
		switch {
		case inter == 0 && (c == 'h' || c == 'l'):
			for i := range p.params {
				t.setPrivate(int(p.params[i]), c == 'h')
			}
		case inter == 0 && c == 'J':
			t.eraseDisplay(p.param(0, 0))
		case inter == 0 && c == 'K':
			t.eraseLine(p.param(0, 0))
		case inter == '$' && c == 'p':
			mode := p.param(0, 0)
			t.reply(fmt.Sprintf("\x1b[?%d;%d$y", mode, t.privateMode(mode)))
		case inter == 0 && c == 'n' && p.param(0, 0) == 6:
			x, y := t.cursorReport()
			t.reply(fmt.Sprintf("\x1b[?%d;%dR", y, x))
		}
		return
	case '>':
		if inter == 0 && c == 'c' {
			t.reply("\x1b[>0;276;0c")
		}
		return
	case 0:
	default:
		return
	}
	if inter != 0 {
		switch {
		case inter == ' ' && c == 'q':
			t.modes.CursorShape = p.param(0, 0)
		case inter == '!' && c == 'p':
			t.softReset()
		case inter == '$' && c == 'p':
			mode, state := p.param(0, 0), 0
			switch mode {
			case 4:
				state = 2
				if t.insert {
					state = 1
				}
			case 20:
				state = 4 // permanently reset
			}
			t.reply(fmt.Sprintf("\x1b[%d;%d$y", mode, state))
		}
		return
	}
	switch c {
	case '@':
		t.insertChars(n)
	case 'A':
		t.cursorUp(n)
	case 'B', 'e':
		t.cursorDown(n)
	case 'C', 'a':
		s.x, s.wrapNext = min(s.x+n, t.cols-1), false
	case 'D':
		s.x, s.wrapNext = max(s.x-n, 0), false
	case 'E':
		t.cursorDown(n)
		s.x = 0
	case 'F':
		t.cursorUp(n)
		s.x = 0
	case 'G', '`':
		s.x, s.wrapNext = min(n-1, t.cols-1), false
	case 'H', 'f':
		t.moveTo(p.param(0, 1)-1, p.param(1, 1)-1)
	case 'I':
		t.tab(n)
	case 'Z':
		t.backTab(n)
	case 'J':
		t.eraseDisplay(p.param(0, 0))
	case 'K':
		t.eraseLine(p.param(0, 0))
	case 'L':
		t.insertLines(n)
	case 'M':
		t.deleteLines(n)
	case 'P':
		t.deleteChars(n)
	case 'S':
		// Rows scrolled up in a region at the top go to the history, as
		// with a line feed.
		t.scrollUp(s.top, s.bot, n)
	case 'T':
		if len(p.params) <= 1 {
			t.scrollDown(s.top, s.bot, n)
		}
	case 'X':
		t.eraseCells(s.y, s.x, s.x+n)
		s.wrapNext = false
	case 'b':
		if s.x > 0 || s.wrapNext {
			x := s.x - 1
			if s.wrapNext {
				x = s.x
			}
			prev := s.rows[s.y].C[x]
			if prev.W == 0 && x > 0 {
				prev = s.rows[s.y].C[x-1]
			}
			if prev.R != 0 {
				for i := 0; i < min(n, 65535); i++ {
					t.print(prev.R)
				}
			}
		}
	case 'c':
		if p.param(0, 0) == 0 {
			t.reply("\x1b[?1;2c")
		}
	case 'd':
		t.moveTo(n-1, s.x)
	case 'g':
		switch p.param(0, 0) {
		case 0:
			t.tabs[s.x] = false
		case 3:
			t.tabs = make([]bool, t.cols)
		}
	case 'h', 'l':
		for i := range p.params {
			if p.params[i] == 4 {
				t.insert = c == 'h'
			}
		}
	case 'm':
		t.sgr(p)
	case 'n':
		switch p.param(0, 0) {
		case 5:
			t.reply("\x1b[0n")
		case 6:
			x, y := t.cursorReport()
			t.reply(fmt.Sprintf("\x1b[%d;%dR", y, x))
		}
	case 'r':
		top, bot := p.param(0, 1)-1, p.param(1, t.rows)-1
		bot = min(bot, t.rows-1)
		if top < bot {
			s.top, s.bot = top, bot
			t.moveTo(0, 0)
		}
	case 's':
		if len(p.params) <= 1 {
			t.saveCursor()
		}
	case 't':
		if p.param(0, 0) == 18 {
			t.reply(fmt.Sprintf("\x1b[8;%d;%dt", t.rows, t.cols))
		}
	case 'u':
		t.restoreCursor()
	}
}

// cursorReport is the 1-based cursor position for CPR (relative to the
// scroll region in origin mode).
func (t *Term) cursorReport() (x, y int) {
	s := t.scr()
	y = s.y + 1
	if t.origin {
		y -= s.top
	}
	return s.x + 1, y
}

func (t *Term) osc(data string) {
	num, rest, _ := strings.Cut(data, ";")
	switch num {
	case "0", "2":
		t.title = capText(rest)
	case "8":
		// OSC 8 ; params ; URI
		_, uri, _ := strings.Cut(rest, ";")
		pen := t.pen
		pen.Link = 0
		if uri != "" && len(uri) <= maxLinkURL {
			pen.Link = t.linkID(uri)
		}
		t.setPen(pen)
	case "52":
		_, payload, ok := strings.Cut(rest, ";")
		// "?" asks to read the clipboard: never answered.
		if !ok || payload == "?" {
			return
		}
		if b, err := base64.StdEncoding.DecodeString(payload); err == nil {
			t.events = append(t.events, Event{Kind: EventClipboard, Text: string(b)})
		}
	case "6464":
		if url, ok := strings.CutPrefix(rest, "open;"); ok && len(url) <= maxLinkURL {
			t.events = append(t.events, Event{Kind: EventOpen, Text: url})
		}
	case "9", "777", "99":
		if text, ok := notification(num, rest); ok {
			t.events = append(t.events, Event{Kind: EventNotify, Text: capText(text)})
		}
	case "10", "11", "12":
		if rest == "?" {
			c := map[string]uint32{"10": t.FG, "11": t.BG, "12": t.CursorColor}[num]
			t.reply("\x1b]" + num + ";" + rgb(c) + "\x1b\\")
		}
	case "4":
		// OSC 4 ; index ; ? (pairs may repeat)
		parts := strings.Split(rest, ";")
		for i := 0; i+1 < len(parts); i += 2 {
			if parts[i+1] != "?" {
				continue
			}
			if n, err := strconv.Atoi(parts[i]); err == nil && n >= 0 && n < 256 {
				t.reply("\x1b]4;" + parts[i] + ";" + rgb(t.paletteColor(n)) + "\x1b\\")
			}
		}
	}
}

const maxTitle = 4096

func capText(s string) string {
	if len(s) > maxTitle {
		s = strings.ToValidUTF8(s[:maxTitle], "")
	}
	return s
}

// notification reads a desktop notification's text: OSC 9 (iTerm2; not
// ConEmu's numbered forms such as 9;4 progress), OSC 777 notify;title;body
// and OSC 99 (kitty: the last chunk, without queries and closes).
func notification(num, rest string) (string, bool) {
	switch num {
	case "9":
		n, _, _ := strings.Cut(rest, ";")
		if _, err := strconv.Atoi(n); err == nil {
			return "", false
		}
		return rest, true
	case "777":
		kind, msg, _ := strings.Cut(rest, ";")
		if kind != "notify" {
			return "", false
		}
		title, body, _ := strings.Cut(msg, ";")
		return strings.TrimSpace(title + "\n" + body), true
	case "99":
		meta, payload, _ := strings.Cut(rest, ";")
		b64 := false
		for _, kv := range strings.Split(meta, ":") {
			k, v, _ := strings.Cut(kv, "=")
			switch {
			case k == "d" && v == "0", k == "p" && v != "title" && v != "body":
				return "", false
			case k == "e" && v == "1":
				b64 = true
			}
		}
		if b64 {
			b, err := base64.StdEncoding.DecodeString(payload)
			if err != nil {
				return "", false
			}
			payload = string(b)
		}
		return payload, true
	}
	return "", false
}

func rgb(c uint32) string {
	r, g, b := c>>16&0xff, c>>8&0xff, c&0xff
	return fmt.Sprintf("rgb:%02x%02x/%02x%02x/%02x%02x", r, r, g, g, b, b)
}

// paletteColor is color n of the 256-color palette: the theme's 16, the
// 6×6×6 cube and the gray ramp.
func (t *Term) paletteColor(n int) uint32 {
	switch {
	case n < 16:
		return t.Palette[n]
	case n < 232:
		n -= 16
		level := func(v int) uint32 {
			if v == 0 {
				return 0
			}
			return uint32(55 + 40*v)
		}
		return level(n/36)<<16 | level(n/6%6)<<8 | level(n%6)
	}
	v := uint32(8 + 10*(n-232))
	return v<<16 | v<<8 | v
}

func (t *Term) sgr(p *parser) {
	ps := p.params
	pen := t.pen
	defer func() { t.setPen(pen) }()
	if len(ps) == 0 {
		pen = Style{Link: pen.Link}
		return
	}
	for i := 0; i < len(ps); i++ {
		v := ps[i]
		if v < 0 {
			v = 0
		}
		// Colon sub-parameters belong to the parameter before them.
		end := i + 1
		for end < len(ps) && p.sub[end] {
			end++
		}
		switch {
		case v == 0:
			pen = Style{Link: pen.Link}
		case v == 1:
			pen.Flags |= Bold
		case v == 2:
			pen.Flags |= Dim
		case v == 3:
			pen.Flags |= Italic
		case v == 4:
			pen.Underline = 1
			if end > i+1 {
				pen.Underline = uint8(min(max(ps[i+1], 0), 5))
			}
		case v == 5 || v == 6:
			pen.Flags |= Blink
		case v == 7:
			pen.Flags |= Inverse
		case v == 8:
			pen.Flags |= Hidden
		case v == 9:
			pen.Flags |= Strike
		case v == 21:
			pen.Underline = 2
		case v == 22:
			pen.Flags &^= Bold | Dim
		case v == 23:
			pen.Flags &^= Italic
		case v == 24:
			pen.Underline = 0
		case v == 25:
			pen.Flags &^= Blink
		case v == 27:
			pen.Flags &^= Inverse
		case v == 28:
			pen.Flags &^= Hidden
		case v == 29:
			pen.Flags &^= Strike
		case v >= 30 && v <= 37:
			pen.FG = PaletteColor | Color(v-30)
		case v == 39:
			pen.FG = 0
		case v >= 40 && v <= 47:
			pen.BG = PaletteColor | Color(v-40)
		case v == 49:
			pen.BG = 0
		case v == 53:
			pen.Flags |= Overline
		case v == 55:
			pen.Flags &^= Overline
		case v == 59:
			pen.UL = 0
		case v >= 90 && v <= 97:
			pen.FG = PaletteColor | Color(v-90+8)
		case v >= 100 && v <= 107:
			pen.BG = PaletteColor | Color(v-100+8)
		case v == 38 || v == 48 || v == 58:
			var col Color
			var used int
			if end > i+1 {
				col, _ = extColor(ps[i+1:end], true)
				used = end - i - 1
			} else {
				col, used = extColor(ps[i+1:], false)
			}
			switch v {
			case 38:
				pen.FG = col
			case 48:
				pen.BG = col
			default:
				pen.UL = col
			}
			if end > i+1 {
				i = end - 1
			} else {
				i += used
			}
			continue
		}
		i = end - 1
	}
}

// extColor reads "5;n" or "2;r;g;b" (with colons, "2::r:g:b" may carry a
// color space id). It returns the color and how many parameters it used.
func extColor(ps []int32, colon bool) (Color, int) {
	if len(ps) == 0 {
		return 0, 0
	}
	at := func(i int) Color {
		if i < len(ps) && ps[i] > 0 {
			return Color(min(ps[i], 255))
		}
		return 0
	}
	switch ps[0] {
	case 5:
		return PaletteColor | at(1), 2
	case 2:
		if colon && len(ps) >= 5 {
			return RGBColor | at(2)<<16 | at(3)<<8 | at(4), len(ps)
		}
		return RGBColor | at(1)<<16 | at(2)<<8 | at(3), 4
	}
	return 0, 1
}
