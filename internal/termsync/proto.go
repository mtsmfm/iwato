// Package termsync keeps browsers in step with a session's terminal
// (internal/screen) instead of streaming its output to them. Each client
// gets frames: the changes since the frame it last got (rows, cursor,
// modes), so a slow client skips intermediate states instead of falling
// behind. The history is sent as pages that the client asks for when it
// shows them; lines that scroll off the screen only move there.
//
// Messages, in both directions, are a kind byte, a big-endian uint32
// length and the payload. Between iwato and the browser each message is
// one WebSocket binary message: the kind byte and the payload.
//
// Client to server:
//
//	'v' hello: uvarint version, cols, rows (must be first; 0×0 keeps the size)
//	'd' input: bytes for the program (keys, mouse and paste already encoded)
//	'r' resize: uvarint cols, rows
//	'a' ack: uvarint frame number, once the client has applied it
//	'g' history: uvarint before, count: up to count lines older than id before
//	'p' ping: up to 8 bytes, answered at once by a pong with the same bytes
//	    (iwato answers it, not the session; it finds a connection that died)
//
// Server to client:
//
//	'F' frame: uvarint number, then operations
//	'P' page: operations (styles, links and one OpLines)
//	'Q' pong: the ping's bytes
//
// Operations are a code byte and uvarint fields; rows are AppendRow's
// encoding, preceded by its uvarint byte length:
//
//	OpReset     cols rows alt histFirst histNext: drop everything known
//	OpStyle     id fg bg ul flags underline link
//	OpLink      id url
//	OpResize    cols rows: rows follow in full
//	OpAlt       on: the other screen is shown; rows follow in full
//	OpScroll    n m {i cont row}×m: the top n rows moved to the history
//	            (ids histNext...); the m listed lines (by index i from the
//	            top) changed before they left, the rest are as last sent.
//	            Past the rows the client had, lines are unknown until fetched.
//	OpHistFirst first: lines before first are gone
//	OpRow       y cont row: the whole row
//	OpPatch     y x row: cells from column x
//	OpCursor    x y flags(1 hidden, 2 blink) shape
//	OpModes     flags(1 app cursor, 2 app keypad, 4 bracketed paste,
//	            8 focus events, 16 color scheme reports) mouse encoding
//	OpTitle     text
//	OpBell
//	OpClipboard text: OSC 52 wrote it
//	OpOpen      url: OSC 6464 asked to open it
//	OpLines     first count {cont row}×count: history lines first...
package termsync

import (
	"encoding/binary"
	"errors"
	"io"
)

// Version is the protocol's version in hello.
const Version = 1

// MaxMessage bounds one message.
const MaxMessage = 1 << 20

// Message kinds.
const (
	KindHello   byte = 'v'
	KindInput   byte = 'd'
	KindResize  byte = 'r'
	KindAck     byte = 'a'
	KindHistory byte = 'g'
	KindPing    byte = 'p'
	KindFrame   byte = 'F'
	KindPage    byte = 'P'
	KindPong    byte = 'Q'
)

// Operation codes.
const (
	OpReset byte = iota + 1
	OpStyle
	OpLink
	OpResize
	OpAlt
	OpScroll
	OpHistFirst
	OpRow
	OpPatch
	OpCursor
	OpModes
	OpTitle
	OpBell
	OpClipboard
	OpOpen
	OpLines
)

// WriteMessage writes one message.
func WriteMessage(w io.Writer, kind byte, payload []byte) error {
	if len(payload) > MaxMessage {
		return errors.New("message too large")
	}
	b := make([]byte, 5, 5+len(payload))
	b[0] = kind
	binary.BigEndian.PutUint32(b[1:], uint32(len(payload)))
	_, err := w.Write(append(b, payload...))
	return err
}

// ReadMessage reads one message.
func ReadMessage(r io.Reader) (byte, []byte, error) {
	var head [5]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return 0, nil, err
	}
	n := binary.BigEndian.Uint32(head[1:])
	if n > MaxMessage {
		return 0, nil, errors.New("message too large")
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}
	return head[0], payload, nil
}

// Hello is the payload of a hello message.
func Hello(cols, rows int) []byte {
	return uvarints(Version, uint64(cols), uint64(rows))
}

// ParseHello reads a hello payload. 0×0 is a client without a size (its
// terminal is not laid out): it attaches at the terminal's size.
func ParseHello(p []byte) (cols, rows int, err error) {
	v, ok := readUvarints(p, 3)
	if !ok || v[0] != Version || !validSize(int(v[1]), int(v[2])) && (v[1] != 0 || v[2] != 0) {
		return 0, 0, errors.New("unsupported hello")
	}
	return int(v[1]), int(v[2]), nil
}

func validSize(cols, rows int) bool { return cols >= 2 && cols <= 1000 && rows >= 2 && rows <= 1000 }

func uvarints(vs ...uint64) []byte {
	var b []byte
	for _, v := range vs {
		b = binary.AppendUvarint(b, v)
	}
	return b
}

// readUvarints reads n uvarints that make up all of p.
func readUvarints(p []byte, n int) ([]uint64, bool) {
	out := make([]uint64, n)
	for i := range out {
		v, k := binary.Uvarint(p)
		if k <= 0 {
			return nil, false
		}
		out[i], p = v, p[k:]
	}
	return out, len(p) == 0
}

func appendBytes(b, s []byte) []byte {
	b = binary.AppendUvarint(b, uint64(len(s)))
	return append(b, s...)
}

func appendString(b []byte, s string) []byte {
	b = binary.AppendUvarint(b, uint64(len(s)))
	return append(b, s...)
}
