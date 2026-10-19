package ingress

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"syscall"
	"time"
)

// DefaultMaxConns is the per-sandbox cap on the ingress's connections to
// the sandbox. OpenShell's gateway allows 20 ForwardTcp streams per sandbox,
// shared with Iwato's terminal and file connections (SSH over the same
// relay): 6 are left for those.
const DefaultMaxConns = 14

// replayLimit bounds what a connection may have sent before the sandbox
// answered and still move to the other loopback address.
const replayLimit = 64 << 10

// Backend is the Dialer to sandboxes: it refuses stopped ones, caps each
// sandbox's connections, and reaches servers listening on 127.0.0.1 or ::1
// only (Node's localhost may be ::1).
type Backend struct {
	// Dial opens a connection to host:port on the sandbox's loopback (host
	// is 127.0.0.1 or ::1). Refusal may show on the first Read or Write.
	Dial func(ctx context.Context, sandbox, host string, port uint16) (net.Conn, error)
	// Classify maps Dial's errors (and its connections') to ErrStopped and
	// ErrRefused, wrapped; nil keeps them (ECONNREFUSED counts as refused).
	Classify func(error) error
	// Running reports the sandbox's last known state (nil: always).
	Running func(sandbox string) bool
	// Max is the per-sandbox cap (DefaultMaxConns when zero); Wait is how
	// long a dial waits for room (10 s when zero).
	Max  int
	Wait time.Duration
	// OnBusy is called when the sandbox has no free place, and again now
	// and then while a dial waits: the HTTP proxy's idle keep-alive
	// connections hold places nobody uses, and dropping them (Server's
	// CloseIdle) frees those. Called without locks held; nil: nothing.
	OnBusy func(sandbox string)

	mu    sync.Mutex
	sems  map[string]chan struct{}
	other map[Target]bool // ports reached on ::1 last time
}

func (b *Backend) classify(err error) error {
	if err == nil {
		return nil
	}
	if b.Classify != nil {
		err = b.Classify(err)
	}
	if !errors.Is(err, ErrRefused) && errors.Is(err, syscall.ECONNREFUSED) {
		err = fmt.Errorf("%w: %v", ErrRefused, err)
	}
	return err
}

func (b *Backend) sem(sandbox string) chan struct{} {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.sems == nil {
		b.sems = map[string]chan struct{}{}
	}
	s := b.sems[sandbox]
	if s == nil {
		n := b.Max
		if n <= 0 {
			n = DefaultMaxConns
		}
		s = make(chan struct{}, n)
		b.sems[sandbox] = s
	}
	return s
}

// InUse is how many of the sandbox's connections are open (tests, page).
func (b *Backend) InUse(sandbox string) int { return len(b.sem(sandbox)) }

// DialContext connects to the target; the connection holds one of the
// sandbox's places until it is closed.
func (b *Backend) DialContext(ctx context.Context, t Target) (net.Conn, error) {
	if b.Running != nil && !b.Running(t.Sandbox) {
		return nil, ErrStopped
	}
	sem := b.sem(t.Sandbox)
	wait := b.Wait
	if wait <= 0 {
		wait = 10 * time.Second
	}
	select {
	case sem <- struct{}{}:
	default:
		if err := b.waitPlace(ctx, sem, t.Sandbox, wait); err != nil {
			return nil, err
		}
	}
	release := sync.OnceFunc(func() { <-sem })
	hosts := []string{"127.0.0.1", "::1"}
	b.mu.Lock()
	if b.other[t] {
		hosts[0], hosts[1] = hosts[1], hosts[0]
	}
	b.mu.Unlock()
	c := &familyConn{b: b, t: t, hosts: hosts, release: release}
	conn, err := b.Dial(ctx, t.Sandbox, hosts[0], t.Port)
	if err != nil {
		release()
		return nil, b.classify(err)
	}
	c.cur = conn
	return c, nil
}

// busyPoll is how often a waiting dial asks again for idle connections to
// be dropped: requests in flight when it first asked go idle later.
const busyPoll = 200 * time.Millisecond

// waitPlace waits for one of the sandbox's places, asking OnBusy to free
// those idle connections hold.
func (b *Backend) waitPlace(ctx context.Context, sem chan struct{}, sandbox string, wait time.Duration) error {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	var poll <-chan time.Time
	if b.OnBusy != nil {
		b.OnBusy(sandbox)
		ticker := time.NewTicker(busyPoll)
		defer ticker.Stop()
		poll = ticker.C
	}
	for {
		select {
		case sem <- struct{}{}:
			return nil
		case <-poll:
			b.OnBusy(sandbox)
		case <-timer.C:
			return ErrBusy
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// familyConn is a connection to one of the sandbox's loopback addresses
// that moves to the other one when the first refuses before answering,
// sending again what was written. Writes go through while the first
// answer is awaited.
type familyConn struct {
	b       *Backend
	t       Target
	hosts   []string
	idx     int
	release func()

	mu          sync.Mutex
	cur         net.Conn
	sent        []byte
	overflow    bool
	established bool
	closed      bool
}

func (c *familyConn) current() (net.Conn, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cur, c.established
}

// failover moves to the next address after failed refused before any
// answer. It returns the error to report when it cannot.
func (c *familyConn) failover(failed net.Conn, err error) error {
	err = c.b.classify(err)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cur != failed {
		return nil // moved already
	}
	if c.established || c.closed || !errors.Is(err, ErrRefused) || c.overflow || c.idx+1 >= len(c.hosts) {
		if !c.established && (errors.Is(err, ErrRefused) || c.idx > 0) {
			// The first address refused; the other one refused too or
			// cannot be used (no IPv6 loopback).
			return fmt.Errorf("%w (%s: %v)", ErrRefused, strings.Join(c.hosts, ", "), err)
		}
		return err
	}
	c.idx++
	_ = failed.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	next, derr := c.b.Dial(ctx, c.t.Sandbox, c.hosts[c.idx], c.t.Port)
	if derr != nil {
		return c.b.classify(derr)
	}
	if len(c.sent) > 0 {
		if _, werr := next.Write(c.sent); werr != nil {
			_ = next.Close()
			if errors.Is(c.b.classify(werr), ErrRefused) {
				return fmt.Errorf("%w (%s)", ErrRefused, strings.Join(c.hosts, ", "))
			}
			return c.b.classify(werr)
		}
	}
	c.cur = next
	return nil
}

func (c *familyConn) Read(p []byte) (int, error) {
	for {
		conn, established := c.current()
		n, err := conn.Read(p)
		if n > 0 && !established {
			c.mu.Lock()
			if c.cur == conn && !c.established {
				c.established = true
				c.sent = nil
				// The address that answered is the one to try first.
				c.b.mu.Lock()
				if c.b.other == nil {
					c.b.other = map[Target]bool{}
				}
				if c.hosts[c.idx] == "::1" {
					c.b.other[c.t] = true
				} else {
					delete(c.b.other, c.t)
				}
				c.b.mu.Unlock()
			}
			c.mu.Unlock()
		}
		if err == nil || n > 0 || established {
			if err != nil && !isEOF(err) {
				err = c.b.classify(err)
			}
			return n, err
		}
		if isEOF(err) {
			return 0, err
		}
		if ferr := c.failover(conn, err); ferr != nil {
			return 0, ferr
		}
	}
}

func (c *familyConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	if c.established {
		conn := c.cur
		c.mu.Unlock()
		n, err := conn.Write(p)
		return n, c.b.classify(err)
	}
	if len(c.sent)+len(p) <= replayLimit {
		c.sent = append(c.sent, p...)
	} else {
		c.overflow = true
	}
	conn := c.cur
	n, err := conn.Write(p)
	c.mu.Unlock()
	if err == nil {
		return n, nil
	}
	if ferr := c.failover(conn, err); ferr != nil {
		return n, ferr
	}
	// The next address got everything sent so far, p included.
	return len(p), nil
}

func (c *familyConn) Close() error {
	c.mu.Lock()
	c.closed = true
	conn := c.cur
	c.mu.Unlock()
	err := conn.Close()
	c.release()
	return err
}

// CloseWrite half-closes when the connection can.
func (c *familyConn) CloseWrite() error {
	conn, _ := c.current()
	if cw, ok := conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return nil
}

func (c *familyConn) LocalAddr() net.Addr  { conn, _ := c.current(); return conn.LocalAddr() }
func (c *familyConn) RemoteAddr() net.Addr { conn, _ := c.current(); return conn.RemoteAddr() }
func (c *familyConn) SetDeadline(t time.Time) error {
	conn, _ := c.current()
	return conn.SetDeadline(t)
}
func (c *familyConn) SetReadDeadline(t time.Time) error {
	conn, _ := c.current()
	return conn.SetReadDeadline(t)
}
func (c *familyConn) SetWriteDeadline(t time.Time) error {
	conn, _ := c.current()
	return conn.SetWriteDeadline(t)
}

func isEOF(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed)
}
