// Package dav serves a sandbox's files over WebDAV (golang.org/x/net/webdav),
// backed by an SFTP server in the sandbox: the sandbox daemon's, rooted at
// the home, or OpenShell's (its supervisor answers the SSH subsystem
// "sftp"), rooted at the workspace.
// Iwato keeps no files; every request becomes SFTP requests.
package dav

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/net/webdav"
)

// ErrStopped is what a Dialer returns when the sandbox does not run (stopped,
// not created, or not ready): the sandbox's folder then shows as stopped.
var ErrStopped = errors.New("sandbox is not running")

// Transport is one connection to a sandbox (an SSH connection through the
// gateway's relay) on which SFTP channels open.
type Transport interface {
	// OpenSFTP opens one more SFTP channel: what the server sends, and
	// what goes to it (closing that ends the channel).
	OpenSFTP() (io.Reader, io.WriteCloser, error)
	Close() error
}

// ClientOptioner is a Transport that tells the SFTP client's options for
// its server (the packet size).
type ClientOptioner interface {
	ClientOptions() []sftp.ClientOption
}

func clientOptions(t Transport) []sftp.ClientOption {
	if o, ok := t.(ClientOptioner); ok {
		return o.ClientOptions()
	}
	return nil
}

// NewClient opens an SFTP client on a new channel of t (without the pool's
// watch; for tests and seeding).
func NewClient(t Transport) (*sftp.Client, error) {
	r, w, err := t.OpenSFTP()
	if err != nil {
		return nil, err
	}
	c, err := sftp.NewClientPipe(r, w, clientOptions(t)...)
	if err != nil {
		_ = w.Close()
	}
	return c, err
}

// Dialer opens a sandbox's transport.
type Dialer func(ctx context.Context, sandbox string) (Transport, error)

const (
	// maxChannels bounds the SFTP channels per sandbox. OpenShell serves
	// each channel's requests one after another, so a few let a large
	// download and a listing go on side by side.
	maxChannels = 4
	// stoppedFor keeps a sandbox known as stopped without asking the
	// gateway again on every request.
	stoppedFor = 3 * time.Second
)

// errOpenTimeout is a channel that did not open in time: the transport is
// taken as broken.
var errOpenTimeout = errors.New("opening an SFTP channel timed out")

// Pool keeps one transport per sandbox (opened on first use) with up to
// maxChannels SFTP channels, closed after Idle without use.
type Pool struct {
	dial Dialer
	// Idle is how long an unused transport stays open.
	Idle time.Duration
	// Stall is how long a channel may owe answers without the server
	// sending anything before the transport is closed (requests then fail
	// and the next one dials again). Real requests answer far sooner.
	Stall time.Duration
	// OpenTimeout bounds opening a channel (the SSH session, the
	// subsystem and SFTP's version exchange).
	OpenTimeout time.Duration

	mu        sync.Mutex
	conns     map[string]*conn
	stopped   map[string]time.Time
	locks     map[string]webdav.LockSystem
	props     *propStore
	janitorOn bool
}

type conn struct {
	ready chan struct{} // closed once dialed
	t     Transport
	err   error

	mu       sync.Mutex
	channels []*channel
	opening  chan struct{} // while a channel opens; closed after
	busy     int
	last     time.Time
	closed   bool
	tOnce    sync.Once
}

type channel struct {
	c      *sftp.Client
	m      *meter
	uses   int
	dead   bool
	leases map[*lease]struct{} // what uses it (for the stall's log line)
}

// NewPool makes a pool that opens transports with dial.
func NewPool(dial Dialer) *Pool {
	return &Pool{dial: dial, Idle: 5 * time.Minute, Stall: time.Minute, OpenTimeout: 30 * time.Second, conns: map[string]*conn{}, stopped: map[string]time.Time{}, locks: map[string]webdav.LockSystem{}, props: newPropStore()}
}

// lease is one use of an SFTP channel; release gives it back.
type lease struct {
	c     *sftp.Client
	cn    *conn
	ch    *channel
	what  string // the operation and path
	start time.Time
	once  sync.Once
}

func (l *lease) release() {
	l.once.Do(func() {
		l.cn.mu.Lock()
		delete(l.ch.leases, l)
		l.ch.uses--
		l.cn.busy--
		l.cn.last = time.Now()
		l.cn.mu.Unlock()
	})
}

// acquire returns a channel of the sandbox's transport, dialing it first
// when needed. A broken transport is dialed again once. what (the
// operation and path) goes to the log when the channel stalls.
func (p *Pool) acquire(ctx context.Context, name, what string) (*lease, error) {
	for attempt := 0; ; attempt++ {
		cn, err := p.connFor(ctx, name)
		if err != nil {
			return nil, err
		}
		l, err := p.lease(ctx, name, cn, what)
		if err == nil {
			return l, nil
		}
		if ctx.Err() != nil {
			return nil, err
		}
		// The transport is gone (the sandbox stopped, the relay broke).
		p.drop(name, cn)
		if attempt > 0 {
			return nil, err
		}
	}
}

func (p *Pool) connFor(ctx context.Context, name string) (*conn, error) {
	for {
		p.mu.Lock()
		if until, ok := p.stopped[name]; ok {
			if time.Now().Before(until) {
				p.mu.Unlock()
				return nil, ErrStopped
			}
			delete(p.stopped, name)
		}
		cn := p.conns[name]
		if cn == nil {
			cn = &conn{ready: make(chan struct{}), last: time.Now()}
			p.conns[name] = cn
			p.mu.Unlock()
			t, err := p.dial(ctx, name)
			cn.t, cn.err = t, err
			close(cn.ready)
			p.mu.Lock()
			if err != nil {
				if p.conns[name] == cn {
					delete(p.conns, name)
				}
				if errors.Is(err, ErrStopped) {
					p.stopped[name] = time.Now().Add(stoppedFor)
				}
				p.mu.Unlock()
				return nil, err
			}
			if !p.janitorOn {
				p.janitorOn = true
				go p.janitor()
			}
			p.mu.Unlock()
			return cn, nil
		}
		p.mu.Unlock()
		select {
		case <-cn.ready:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		if cn.err != nil {
			// That dial failed; it was taken out of the map. Try again
			// (a stopped sandbox is answered from p.stopped).
			if errors.Is(cn.err, ErrStopped) {
				return nil, ErrStopped
			}
			return nil, cn.err
		}
		return cn, nil
	}
}

// lease picks the least used live channel, opening another one while the
// ones there are busy (up to maxChannels). The channel opens outside cn.mu:
// meanwhile the others go on, and requests that have none wait for it.
func (p *Pool) lease(ctx context.Context, name string, cn *conn, what string) (*lease, error) {
	cn.mu.Lock()
	for {
		if cn.closed {
			cn.mu.Unlock()
			return nil, errors.New("connection closed")
		}
		best := cn.pick()
		if best != nil && (best.uses == 0 || len(cn.channels) >= maxChannels || cn.opening != nil) {
			l := cn.take(best, what)
			cn.mu.Unlock()
			return l, nil
		}
		if wait := cn.opening; wait != nil {
			cn.mu.Unlock()
			select {
			case <-wait:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			cn.mu.Lock()
			continue
		}
		opening := make(chan struct{})
		cn.opening = opening
		cn.mu.Unlock()
		ch, err := p.openChannel(name, cn)
		cn.mu.Lock()
		cn.opening = nil
		close(opening)
		if err == nil && cn.closed {
			go ch.c.Close()
			err = errors.New("connection closed")
		}
		if err != nil {
			if best == nil || best.dead || cn.closed || errors.Is(err, errOpenTimeout) {
				cn.mu.Unlock()
				return nil, err
			}
			l := cn.take(best, what)
			cn.mu.Unlock()
			return l, nil
		}
		cn.channels = append(cn.channels, ch)
		go func() {
			_ = ch.c.Wait()
			cn.mu.Lock()
			ch.dead = true
			cn.mu.Unlock()
		}()
		l := cn.take(ch, what)
		cn.mu.Unlock()
		return l, nil
	}
}

// pick drops the dead channels nothing uses and returns the least used
// live one (nil when there is none). cn.mu is held.
func (cn *conn) pick() *channel {
	var best *channel
	live := cn.channels[:0]
	for _, ch := range cn.channels {
		if ch.dead && ch.uses == 0 {
			continue
		}
		live = append(live, ch)
		if !ch.dead && (best == nil || ch.uses < best.uses) {
			best = ch
		}
	}
	clear(cn.channels[len(live):])
	cn.channels = live
	return best
}

// take leases ch. cn.mu is held.
func (cn *conn) take(ch *channel, what string) *lease {
	l := &lease{c: ch.c, cn: cn, ch: ch, what: what, start: time.Now()}
	ch.leases[l] = struct{}{}
	ch.uses++
	cn.busy++
	cn.last = time.Now()
	return l
}

// openChannel opens an SFTP channel on cn's transport, watched by a
// meter. One that does not open within OpenTimeout is errOpenTimeout (the
// caller drops the transport, which ends the attempt).
func (p *Pool) openChannel(name string, cn *conn) (*channel, error) {
	type result struct {
		ch  *channel
		err error
	}
	done := make(chan result, 1)
	go func() {
		r, w, err := cn.t.OpenSFTP()
		if err != nil {
			done <- result{err: err}
			return
		}
		m := &meter{}
		c, err := sftp.NewClientPipe(meterReader{r, m}, meterWriter{w, m}, clientOptions(cn.t)...)
		if err != nil {
			_ = w.Close()
			done <- result{err: err}
			return
		}
		done <- result{ch: &channel{c: c, m: m, leases: map[*lease]struct{}{}}}
	}()
	timer := time.NewTimer(p.OpenTimeout)
	defer timer.Stop()
	select {
	case r := <-done:
		return r.ch, r.err
	case <-timer.C:
		log.Printf("files: %s: opening an SFTP channel took over %s; closing the connection", name, p.OpenTimeout)
		go func() {
			if r := <-done; r.ch != nil {
				_ = r.ch.c.Close()
			}
		}()
		return nil, errOpenTimeout
	}
}

// stalled reports the first channel that owes answers and got nothing from
// the server for longer than d, describing what uses it.
func (cn *conn) stalled(d time.Duration) (string, bool) {
	cn.mu.Lock()
	defer cn.mu.Unlock()
	now := time.Now()
	for i, ch := range cn.channels {
		if ch.dead {
			continue
		}
		owed, since := ch.m.owed()
		if owed <= 0 || now.Sub(since) <= d {
			continue
		}
		var uses []*lease
		for l := range ch.leases {
			uses = append(uses, l)
		}
		sort.Slice(uses, func(a, b int) bool { return uses[a].start.Before(uses[b].start) })
		var what []string
		for _, l := range uses {
			what = append(what, fmt.Sprintf("%s (%s)", l.what, now.Sub(l.start).Round(time.Second)))
		}
		if len(what) == 0 {
			what = append(what, "nothing")
		}
		return fmt.Sprintf("SFTP channel %d of %d stalled: %d requests unanswered, nothing from the server for %s; in use by %s",
			i+1, len(cn.channels), owed, now.Sub(since).Round(time.Second), strings.Join(what, ", ")), true
	}
	return "", false
}

// closeIfIdle marks the connection closed when nothing used it for idle,
// so that no lease starts on it while the caller closes it.
func (cn *conn) closeIfIdle(idle time.Duration) bool {
	cn.mu.Lock()
	if cn.closed || cn.busy > 0 || time.Since(cn.last) <= idle {
		cn.mu.Unlock()
		return false
	}
	cn.closed = true
	cn.mu.Unlock()
	return true // the caller drops it: close
}

func (cn *conn) close() {
	cn.mu.Lock()
	cn.closed = true
	channels := cn.channels
	cn.channels = nil
	cn.mu.Unlock()
	// The transport first: an SFTP client's Close waits until the server
	// ends its channel, which OpenShell's does only after 30-60 s. With the
	// connection gone, the channels end at once.
	cn.tOnce.Do(func() {
		if cn.t != nil {
			_ = cn.t.Close()
		}
	})
	for _, ch := range channels {
		_ = ch.c.Close()
	}
}

func (p *Pool) drop(name string, cn *conn) {
	p.mu.Lock()
	if p.conns[name] == cn {
		delete(p.conns, name)
	}
	p.mu.Unlock()
	cn.close()
}

// Close closes the sandbox's transport (it stopped, started or was
// deleted); the next request dials again.
func (p *Pool) Close(name string) {
	p.mu.Lock()
	cn := p.conns[name]
	delete(p.conns, name)
	delete(p.stopped, name)
	p.mu.Unlock()
	if cn != nil {
		<-cn.ready
		cn.close()
	}
}

// CloseAll closes every transport (a credential was revoked: its
// downloads in progress end too).
func (p *Pool) CloseAll() {
	p.mu.Lock()
	names := make([]string, 0, len(p.conns))
	for name := range p.conns {
		names = append(names, name)
	}
	p.mu.Unlock()
	for _, name := range names {
		p.Close(name)
	}
}

// Open reports whether the sandbox has a transport open (for tests).
func (p *Pool) Open(name string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.conns[name] != nil
}

// janitor closes transports unused for Idle; it ends when none is left.
func (p *Pool) janitor() {
	every := min(p.Idle, p.Stall) / 4
	if every < 10*time.Millisecond {
		every = 10 * time.Millisecond
	}
	if every > 30*time.Second {
		every = 30 * time.Second
	}
	for {
		time.Sleep(every)
		p.mu.Lock()
		var idle []string
		stalled := map[string]*conn{}
		for name, cn := range p.conns {
			select {
			case <-cn.ready:
			default:
				continue
			}
			if why, ok := cn.stalled(p.Stall); ok {
				log.Printf("files: %s: %s; closing the connection", name, why)
				stalled[name] = cn
				continue
			}
			cn.mu.Lock()
			if cn.busy == 0 && time.Since(cn.last) > p.Idle {
				idle = append(idle, name)
			}
			cn.mu.Unlock()
		}
		p.mu.Unlock()
		// A stalled channel's requests would wait forever (and new ones
		// would queue behind them): closing the transport fails them, and
		// the next request dials again. The janitor does not wait for the
		// close: it goes on watching the other sandboxes.
		for name, cn := range stalled {
			p.mu.Lock()
			if p.conns[name] == cn {
				delete(p.conns, name)
			}
			p.mu.Unlock()
			go cn.close()
		}
		for _, name := range idle {
			p.mu.Lock()
			cn := p.conns[name]
			p.mu.Unlock()
			if cn != nil && cn.closeIfIdle(p.Idle) {
				p.drop(name, cn)
			}
		}
		p.mu.Lock()
		if len(p.conns) == 0 {
			p.janitorOn = false
			p.mu.Unlock()
			return
		}
		p.mu.Unlock()
	}
}

// Do runs fn on one of the sandbox's SFTP channels (dialing the sandbox
// first when needed); what (the operation and path) goes to the log when
// the channel stalls. A sandbox that does not run is ErrStopped.
func (p *Pool) Do(ctx context.Context, name, what string, fn func(*sftp.Client) error) error {
	l, err := p.acquire(ctx, name, what)
	if err != nil {
		return err
	}
	defer l.release()
	return fn(l.c)
}
