// Package hostterm keeps terminal sessions in iwato itself: a session owns
// a terminal process elsewhere (a PTY in an OpenShell sandbox) and its
// screen (a termsync hub over internal/screen), so browsers can come and go.
// Each attachment is a termsync client of the hub: it gets the current
// screen, then the changes, and fetches the history it shows.
package hostterm

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"iwato/internal/termsync"
)

// Backend is the terminal process: its PTY output, input and size.
type Backend interface {
	io.ReadWriter
	Resize(cols, rows int) error
	// Close hangs up the process (or leaves it, when it outlives the
	// connection, as a sandbox's main process does).
	Close() error
}

// Cwder is a backend that can tell its process's working directory.
type Cwder interface {
	Cwd(ctx context.Context) string
}

// Killer is a backend whose process outlives Close: Kill ends it.
type Killer interface {
	Kill() error
}

// Session feeds a backend's output to the hub its clients attach to. It
// ends when the backend's output ends.
type Session struct {
	backend Backend
	hub     *termsync.Hub
	mu      sync.Mutex // the size and err
	cols    int
	rows    int
	err     error // why the output ended
	input   sync.Mutex
	done    chan struct{}
	closing sync.Once
	rec     *Recorder // nil unless the session is recorded
	started time.Time
}

// New starts a session on backend, whose terminal is cols×rows. Everything
// the backend outputs builds the screen, including output it replays from
// before (a reattached main process's recent output).
func New(backend Backend, cols, rows int) *Session { return NewRecorded(backend, cols, rows, nil) }

// NewRecorded is New with a recording of the terminal's output and sizes
// (nil for none); the session ends it.
func NewRecorded(backend Backend, cols, rows int, rec *Recorder) *Session {
	s := &Session{backend: backend, cols: cols, rows: rows, done: make(chan struct{}), rec: rec, started: time.Now()}
	s.hub = termsync.NewHub(cols, rows, func(b []byte) error {
		s.input.Lock()
		defer s.input.Unlock()
		_, err := s.backend.Write(b)
		return err
	}, s.resize)
	go s.pump()
	return s
}

// Done is closed when the process's output has ended.
func (s *Session) Done() <-chan struct{} { return s.done }

// Kill ends the process: a Killer's with Kill (its output ends when it
// has), any other's by closing it.
func (s *Session) Kill() {
	if k, ok := s.backend.(Killer); ok {
		_ = k.Kill()
		return
	}
	s.Close()
}

// Close closes the backend: it hangs up the process, or leaves one that
// outlives it (a Killer, a sandbox's main process).
func (s *Session) Close() { s.closing.Do(func() { _ = s.backend.Close() }) }

// Started is when the session started (its first screen is the backend's
// replay, if any).
func (s *Session) Started() time.Time { return s.started }

// pump feeds output to the hub, which keeps clients in step with frames
// instead of queueing output for them.
func (s *Session) pump() {
	buf := make([]byte, 32<<10)
	for {
		n, err := s.backend.Read(buf)
		if n > 0 {
			s.rec.Output(buf[:n])
			s.hub.Feed(buf[:n])
		}
		if err != nil {
			s.mu.Lock()
			s.err = err
			s.mu.Unlock()
			break
		}
	}
	s.hub.Close()
	s.Close()
	s.rec.End(s.Err())
	close(s.done)
}

// Err is why the session's output ended (nil while it runs).
func (s *Session) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

// Screen returns the terminal's size and the text of the rows it shows.
func (s *Session) Screen() (cols, rows int, lines []string) { return s.hub.Screen() }

// Dir is the terminal's working directory as far as known: the backend's
// (Cwder), else the last one the shell reported (OSC 7); "" otherwise.
func (s *Session) Dir(ctx context.Context) string {
	if c, ok := s.backend.(Cwder); ok {
		if dir := c.Cwd(ctx); dir != "" {
			return dir
		}
	}
	return s.hub.Dir()
}

// Activity is the clients' last input and the program's alerts.
func (s *Session) Activity() termsync.Activity { return s.hub.Activity() }

// Attach returns a client of the session: a Terminal carrying termsync
// messages (the client's hello first, which sizes the session: the most
// recent client decides; a hello of 0×0 keeps the size). Closing it leaves
// the session running.
func (s *Session) Attach() (*Client, error) {
	select {
	case <-s.done:
		return nil, ErrEnded
	default:
	}
	a, b := net.Pipe()
	go func() { _ = s.hub.Serve(b) }()
	return &Client{Conn: a, s: s}, nil
}

// ErrEnded is Attach's error once the process has ended.
var ErrEnded = errors.New("the terminal session has ended")

// resize sizes the process when a client asks for another size.
func (s *Session) resize(cols, rows int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cols == s.cols && rows == s.rows {
		return nil
	}
	if err := s.backend.Resize(cols, rows); err != nil {
		return err
	}
	s.cols, s.rows = cols, rows
	s.rec.Resize(cols, rows)
	return nil
}

// Client is one attachment: a Terminal for iwato's websocket handler.
type Client struct {
	net.Conn
	s *Session
}

// Resize is unused: termsync messages size the session.
func (c *Client) Resize(int, int) error { return nil }

// Exited is closed when the process's output has ended.
func (c *Client) Exited() <-chan struct{} { return c.s.done }

// Err is why the process's output ended (nil while it runs).
func (c *Client) Err() error { return c.s.Err() }
