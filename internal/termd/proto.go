// Package termd keeps a sandbox's terminals alive across Iwato restarts:
// cmd/iwato-entrypoint runs in the sandbox as a daemon (Serve) that owns
// every terminal's PTY and process, and Iwato reaches it through a TTY-less
// exec of `iwato-entrypoint attach`, whose stdin and stdout carry this
// package's messages (the daemon's own, relayed unchanged).
//
// Versions: Iwato and the daemon of a sandbox's image may differ (an Iwato
// update leaves the image as it was). The daemon tells its Version and
// Capabilities in each list and hello; Iwato uses what the daemon has and
// shows the rest as available after recreating the sandbox. A message or
// field is never changed in a way an older side cannot read: new ones are
// added, and only a deliberate break raises MinVersion.
//
// Messages are a kind byte, a big-endian uint32 length and the payload, as
// in internal/termsync.
//
// From a client to the daemon, first on its connection (the CLI sends it;
// Iwato never does):
//
//	'O' open: JSON Open; then input, resize and kill as below
//	'L' list: the daemon answers 'l' and closes
//	'K' kill: JSON Kill; the daemon answers 'k' (or 'e') once it ended
//
// On an open connection, from Iwato:
//
//	'i' input: bytes for the terminal
//	'r' resize: uint16 cols, rows
//	'x' kill: end the terminal (its exit follows)
//
// From the daemon:
//
//	'h' hello: JSON Hello, first; the terminal's recent output follows
//	'o' output: bytes the terminal wrote
//	'q' exit: JSON Exit; the terminal ended, the connection ends
//	'e' error: JSON Error, instead of hello (or of a list or kill answer)
//	'l' list: JSON List
//	'k' killed: the kill's answer
package termd

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"regexp"
)

// Version is the daemon's version. 1: terminals (iwato-term); 2: the
// capabilities, the daemon's path and the SFTP server (iwato-entrypoint).
const Version = 2

// MinVersion is the oldest daemon Iwato talks to; an older one counts as
// none (its terminals work as without a daemon).
const MinVersion = 1

// Capabilities: what a daemon serves besides its terminals' protocol.
const (
	// CapTerminals: the terminals (every version).
	CapTerminals = "terminals"
	// CapSFTP: `iwato-entrypoint sftp`, an SFTP server rooted at the home
	// (internal/rootsftp).
	CapSFTP = "sftp"
)

// Capabilities are this daemon's.
var Capabilities = []string{CapTerminals, CapSFTP}

// Has tells whether a daemon of version with caps (as its list or hello
// said them) serves capability c. Version 1 said none: it has terminals.
func Has(version int, caps []string, c string) bool {
	if version <= 1 && len(caps) == 0 {
		return c == CapTerminals
	}
	for _, x := range caps {
		if x == c {
			return true
		}
	}
	return false
}

// MaxMessage bounds one message's payload.
const MaxMessage = 1 << 20

// Message kinds.
const (
	KindOpen   byte = 'O'
	KindList   byte = 'L'
	KindKill   byte = 'K'
	KindInput  byte = 'i'
	KindResize byte = 'r'
	KindEnd    byte = 'x'
	KindHello  byte = 'h'
	KindOutput byte = 'o'
	KindExit   byte = 'q'
	KindError  byte = 'e'
	KindListed byte = 'l'
	KindKilled byte = 'k'
)

// Path is where the sandbox image carries iwato-entrypoint (root's, 0555;
// the image convention, docs/adr/0005-sandbox-image-convention.md).
const Path = "/usr/local/lib/iwato/iwato-entrypoint"

// LegacyPath is where the WSLc driver placed the daemon (then iwato-term)
// before the image carried it. Sandboxes made then run it as their main
// process from there, and the driver keeps placing the current binary
// there on each of their starts (until they are recreated).
const LegacyPath = "/usr/local/lib/iwato/iwato-term"

// IsDaemonPath tells whether p is one of the daemon's paths.
func IsDaemonPath(p string) bool { return p == Path || p == LegacyPath }

// MaxTerminals bounds the terminals one daemon runs (Iwato's maxTerminals).
const MaxTerminals = 8

// RingSize is how much recent output a terminal keeps for an attach.
const RingSize = 1 << 20

// Open asks for a terminal: the one named, or, when it is not running and
// Cmd is given, a new one running Cmd with Env in Dir on a Cols×Rows PTY.
// A size of 0×0 keeps the terminal's.
type Open struct {
	Name string   `json:"name"`
	Cols int      `json:"cols"`
	Rows int      `json:"rows"`
	Cmd  []string `json:"cmd,omitempty"`
	Env  []string `json:"env,omitempty"`
	Dir  string   `json:"dir,omitempty"`
}

// Hello starts an attachment. Started and DaemonStarted are Unix
// milliseconds; Offset is the position in the terminal's output where the
// replayed output starts (bytes before it are gone).
type Hello struct {
	Version       int      `json:"version"`
	Capabilities  []string `json:"capabilities,omitempty"`
	DaemonStarted int64    `json:"daemonStarted"`
	Started       int64    `json:"started"`
	Created       bool     `json:"created"`
	Cols          int      `json:"cols"`
	Rows          int      `json:"rows"`
	Offset        uint64   `json:"offset"`
}

// Exit is a terminal's end: its process's exit status (128+signal when a
// signal ended it).
type Exit struct {
	Status int `json:"status"`
}

// Error codes.
const (
	ErrAbsent      = "absent"      // no such terminal, and no command to start one
	ErrLimit       = "limit"       // MaxTerminals are running
	ErrInvalid     = "invalid"     // a bad request
	ErrStart       = "start"       // the command did not start
	ErrUnavailable = "unavailable" // (the CLI's) the daemon cannot be reached
)

// Error is a refused request.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Message }

// Kill names the terminal to end.
type Kill struct {
	Name string `json:"name"`
}

// Terminal is one running terminal in a List.
type Terminal struct {
	Name    string `json:"name"`
	Started int64  `json:"started"`
	Cols    int    `json:"cols"`
	Rows    int    `json:"rows"`
}

// List is the daemon's version, capabilities and path (its executable:
// the same binary serves the CLI's commands, sftp included) and its running
// terminals. Version 1 sent neither capabilities nor path.
type List struct {
	Version       int        `json:"version"`
	Capabilities  []string   `json:"capabilities,omitempty"`
	Path          string     `json:"path,omitempty"`
	DaemonStarted int64      `json:"daemonStarted"`
	Terminals     []Terminal `json:"terminals"`
}

var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// ValidName accepts Iwato's terminal names.
func ValidName(name string) bool { return namePattern.MatchString(name) }

var keyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// ValidKey accepts a daemon's key (Iwato's sandbox name), which names its
// socket.
func ValidKey(key string) bool { return keyPattern.MatchString(key) }

// WriteMessage writes one message.
func WriteMessage(w io.Writer, kind byte, payload []byte) error {
	_, err := w.Write(Message(kind, payload))
	return err
}

// Message encodes one message.
func Message(kind byte, payload []byte) []byte {
	b := make([]byte, 5, 5+min(len(payload), MaxMessage))
	b[0] = kind
	binary.BigEndian.PutUint32(b[1:], uint32(len(payload)))
	return append(b, payload...)
}

// WriteJSON writes a message whose payload is v in JSON.
func WriteJSON(w io.Writer, kind byte, v any) error {
	p, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return WriteMessage(w, kind, p)
}

// ErrTooLarge is ReadMessage's error for a length over MaxMessage (also
// what reading something that is not this protocol usually gives).
var ErrTooLarge = errors.New("termd: message too large")

// ReadMessage reads one message.
func ReadMessage(r io.Reader) (byte, []byte, error) {
	var head [5]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return 0, nil, err
	}
	n := binary.BigEndian.Uint32(head[1:])
	if n > MaxMessage {
		return 0, nil, ErrTooLarge
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(r, payload); err != nil {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return 0, nil, err
	}
	return head[0], payload, nil
}

// ResizePayload encodes a resize.
func ResizePayload(cols, rows int) []byte {
	return binary.BigEndian.AppendUint16(binary.BigEndian.AppendUint16(nil, uint16(cols)), uint16(rows))
}

// ParseResize decodes a resize.
func ParseResize(p []byte) (cols, rows int, ok bool) {
	if len(p) != 4 {
		return 0, 0, false
	}
	return int(binary.BigEndian.Uint16(p)), int(binary.BigEndian.Uint16(p[2:])), true
}

// Ring keeps the last RingSize bytes of a terminal's output.
type Ring struct {
	buf   []byte
	total uint64 // bytes ever written
}

// NewRing returns a ring of size bytes.
func NewRing(size int) *Ring { return &Ring{buf: make([]byte, size)} }

func (r *Ring) Write(p []byte) {
	n := len(r.buf)
	r.total += uint64(len(p))
	if len(p) > n {
		p = p[len(p)-n:]
	}
	// p's first byte goes where the byte at output offset total-len(p) does.
	at := int((r.total - uint64(len(p))) % uint64(n))
	c := copy(r.buf[at:], p)
	copy(r.buf, p[c:])
}

// Bytes returns what the ring keeps, oldest first, and the offset in the
// output of its first byte.
func (r *Ring) Bytes() ([]byte, uint64) {
	n := uint64(len(r.buf))
	if r.total <= n {
		return append([]byte(nil), r.buf[:r.total]...), 0
	}
	at := int(r.total % n)
	out := make([]byte, 0, n)
	out = append(out, r.buf[at:]...)
	out = append(out, r.buf[:at]...)
	return out, r.total - n
}

// Total is how many bytes were ever written.
func (r *Ring) Total() uint64 { return r.total }
