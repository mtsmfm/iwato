package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	dm "github.com/NVIDIA/OpenShell/sdk/go/proto/datamodelv1"
	pb "github.com/NVIDIA/OpenShell/sdk/go/proto/openshellv1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Errors of a forwarded connection, wrapped with the gateway's message.
var (
	// ErrConnRefused: nothing listens on the port in the sandbox.
	ErrConnRefused = errors.New("connection refused in the sandbox")
	// ErrNotReady: the sandbox does not run (the gateway has no relay to it).
	ErrNotReady = errors.New("sandbox is not ready")
	// ErrSandboxBusy: the gateway's 20 connections per sandbox are taken.
	ErrSandboxBusy = errors.New("sandbox connection limit reached")
)

// The gateway's limits on ForwardTcp (crates/openshell-server/src/grpc/
// sandbox.rs acquire_ssh_connection_slots): 3 connections per session
// token. The 20 per sandbox are the caller's to respect.
//
// The gateway gives a connection's slots back only when its relay is torn
// down (ForwardConnectionGuard is dropped when the bridge to the sandbox
// ends, after the sandbox side closed too), some time after this side
// closed. A closed connection's use of its token therefore still counts for
// Linger, and a dial the gateway refuses for its token's limit (or the
// token itself) is tried once more with a new token.
const usesPerToken = 3

// Forwarder opens TCP connections to a sandbox's loopback through the
// gateway's ForwardTcp relay. Each connection presents an SSH session
// token, which the gateway validates and counts (the Go SDK's
// TCP().Forward sends none). Tokens are shared by up to three connections
// and revoked once unused for Idle.
type Forwarder struct {
	Conn      grpc.ClientConnInterface
	Workspace string
	// Idle is how long an unused token is kept (30 s when zero); MaxAge
	// how long one is handed out (an hour when zero; the gateway's
	// default lifetime is 24 hours).
	Idle, MaxAge time.Duration
	// Linger is how long a closed connection's use of its token still
	// counts (a second when zero): the gateway's own count drops a little
	// later.
	Linger time.Duration

	mu     sync.Mutex
	tokens map[string][]*fwdToken // by sandbox
}

type fwdToken struct {
	value   string
	users   int
	created time.Time
	dead    bool
	timer   *time.Timer
}

func (f *Forwarder) idle() time.Duration {
	if f.Idle > 0 {
		return f.Idle
	}
	return 30 * time.Second
}

func (f *Forwarder) linger() time.Duration {
	if f.Linger > 0 {
		return f.Linger
	}
	return time.Second
}

func (f *Forwarder) maxAge() time.Duration {
	if f.MaxAge > 0 {
		return f.MaxAge
	}
	return time.Hour
}

// acquire hands out a token with room for one more connection, making one
// when none has (or always, when fresh).
func (f *Forwarder) acquire(ctx context.Context, sandbox string, fresh bool) (*fwdToken, error) {
	f.mu.Lock()
	for _, t := range f.tokens[sandbox] {
		if fresh {
			break
		}
		if !t.dead && t.users < usesPerToken && time.Since(t.created) < f.maxAge() {
			t.users++
			if t.timer != nil {
				t.timer.Stop()
				t.timer = nil
			}
			f.mu.Unlock()
			return t, nil
		}
	}
	f.mu.Unlock()
	resp, err := pb.NewOpenShellClient(f.Conn).CreateSshSession(ctx, &pb.CreateSshSessionRequest{
		Sandbox:        sandbox,
		WorkspaceScope: &dm.WorkspaceSelector{Selection: &dm.WorkspaceSelector_Workspace{Workspace: f.Workspace}},
	})
	if err != nil {
		return nil, classify(err)
	}
	t := &fwdToken{value: resp.GetToken(), users: 1, created: time.Now()}
	f.mu.Lock()
	if f.tokens == nil {
		f.tokens = map[string][]*fwdToken{}
	}
	f.tokens[sandbox] = append(f.tokens[sandbox], t)
	f.mu.Unlock()
	return t, nil
}

// release returns a connection's use of t; an unused token is revoked
// after Idle (at once when it is dead or too old).
func (f *Forwarder) release(sandbox string, t *fwdToken) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t.users--
	if t.users > 0 {
		return
	}
	if t.dead || time.Since(t.created) >= f.maxAge() {
		f.dropLocked(sandbox, t)
		return
	}
	t.timer = time.AfterFunc(f.idle(), func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		if t.users == 0 {
			f.dropLocked(sandbox, t)
		}
	})
}

// dropLocked forgets t and revokes it in the background.
func (f *Forwarder) dropLocked(sandbox string, t *fwdToken) {
	list := f.tokens[sandbox]
	for i, x := range list {
		if x == t {
			f.tokens[sandbox] = append(list[:i:i], list[i+1:]...)
			break
		}
	}
	if len(f.tokens[sandbox]) == 0 {
		delete(f.tokens, sandbox)
	}
	t.dead = true
	go f.revoke(t.value)
}

func (f *Forwarder) revoke(token string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = pb.NewOpenShellClient(f.Conn).RevokeSshSession(ctx, &pb.RevokeSshSessionRequest{Token: token, AllowMissing: true})
}

// Forget revokes the sandbox's unused tokens and retires the others (a
// stopped or deleted sandbox).
func (f *Forwarder) Forget(sandbox string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, t := range append([]*fwdToken(nil), f.tokens[sandbox]...) {
		if t.timer != nil {
			t.timer.Stop()
		}
		if t.users == 0 {
			f.dropLocked(sandbox, t)
		} else {
			t.dead = true
		}
	}
}

// Tokens is how many tokens the sandbox holds (tests).
func (f *Forwarder) Tokens(sandbox string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.tokens[sandbox])
}

// Dial connects to host:port on the sandbox's loopback (host is
// 127.0.0.1 or ::1). That nothing listens there shows only on the first
// Read (or a Write): the relay reports it after the stream is open.
func (f *Forwarder) Dial(ctx context.Context, sandbox, host string, port uint16) (net.Conn, error) {
	if port == 0 {
		return nil, errors.New("port must be 1-65535")
	}
	c, err := f.dial(ctx, sandbox, host, port, false)
	if errors.Is(err, errTokenFull) || errors.Is(err, errTokenRejected) {
		// The token is not handed out again; once more with a new one.
		c, err = f.dial(ctx, sandbox, host, port, true)
	}
	return c, err
}

func (f *Forwarder) dial(ctx context.Context, sandbox, host string, port uint16, fresh bool) (net.Conn, error) {
	tok, err := f.acquire(ctx, sandbox, fresh)
	if err != nil {
		return nil, err
	}
	// The stream lives as long as the connection, not ctx.
	streamCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	stream, err := pb.NewOpenShellClient(f.Conn).ForwardTcp(streamCtx)
	if err == nil {
		err = stream.Send(&pb.TcpForwardFrame{Payload: &pb.TcpForwardFrame_Init{Init: &pb.TcpForwardInit{
			Sandbox:            sandbox,
			Workspace:          f.Workspace,
			AuthorizationToken: tok.value,
			Target:             &pb.TcpForwardInit_Tcp{Tcp: &pb.TcpRelayTarget{Host: host, Port: uint32(port)}},
		}}})
		if err == nil || errors.Is(err, io.EOF) {
			err = accepted(ctx, stream, cancel)
		}
	}
	if err != nil {
		cancel()
		err = classify(err)
		if errors.Is(err, errTokenFull) || errors.Is(err, errTokenRejected) {
			f.mu.Lock()
			tok.dead = true
			f.mu.Unlock()
		}
		// Refused before its slots were taken: nothing lingers.
		f.release(sandbox, tok)
		return nil, err
	}
	c := &forwardConn{
		stream: stream, cancel: cancel, data: make(chan []byte, 16), done: make(chan struct{}),
		local: forwardAddr("iwato"), remote: forwardAddr(net.JoinHostPort(host, fmt.Sprint(port)) + "@" + sandbox),
	}
	c.release = func() {
		if errors.Is(c.err, errTokenRejected) {
			f.mu.Lock()
			tok.dead = true
			f.mu.Unlock()
		}
		time.AfterFunc(f.linger(), func() { f.release(sandbox, tok) })
	}
	c.readDeadline = make(chan struct{})
	go c.readLoop()
	return c, nil
}

// accepted waits until the gateway has taken the stream (its response
// headers, sent once the token is checked and the slots taken), returning
// why it refused it otherwise. That nothing listens comes later.
func accepted(ctx context.Context, stream grpc.BidiStreamingClient[pb.TcpForwardFrame, pb.TcpForwardFrame], cancel context.CancelFunc) error {
	stop := context.AfterFunc(ctx, cancel)
	defer stop()
	if md, _ := stream.Header(); md != nil {
		return nil
	}
	// The stream ended without headers: Recv tells why.
	_, err := stream.Recv()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err == nil || errors.Is(err, io.EOF) {
		return errors.New("forward: the gateway ended the stream")
	}
	return err
}

var (
	// errTokenRejected marks a stream the gateway refused for its token (it
	// expired or the sandbox was made again): the token is not used again.
	errTokenRejected = errors.New("session token rejected")
	// errTokenFull marks a stream refused for its token's connection limit.
	errTokenFull = errors.New("session token connection limit reached")
)

// classify turns the gateway's status into this package's errors.
func classify(err error) error {
	if err == nil || errors.Is(err, io.EOF) {
		return err
	}
	st, ok := status.FromError(err)
	if !ok {
		return err
	}
	msg := st.Message()
	switch {
	case st.Code() == codes.FailedPrecondition, st.Code() == codes.NotFound:
		return fmt.Errorf("%w: %s", ErrNotReady, msg)
	case st.Code() == codes.Unauthenticated:
		return fmt.Errorf("%w: %s", errTokenRejected, msg)
	case st.Code() == codes.ResourceExhausted && strings.Contains(msg, "sandbox SSH connection limit"):
		return fmt.Errorf("%w: %s", ErrSandboxBusy, msg)
	case st.Code() == codes.ResourceExhausted && strings.Contains(msg, "SSH session connection limit"):
		return fmt.Errorf("forward: %w: %s", errTokenFull, msg)
	case strings.Contains(strings.ToLower(msg), "connection refused"):
		return fmt.Errorf("%w: %s", ErrConnRefused, msg)
	}
	return fmt.Errorf("forward: %s (%s)", msg, st.Code())
}

type forwardAddr string

func (forwardAddr) Network() string  { return "openshell" }
func (a forwardAddr) String() string { return string(a) }

// forwardConn is a ForwardTcp stream as a net.Conn: CloseWrite ends the
// stream's sending side (the gateway shuts the sandbox side's write), and
// read deadlines work.
type forwardConn struct {
	stream  grpc.BidiStreamingClient[pb.TcpForwardFrame, pb.TcpForwardFrame]
	cancel  context.CancelFunc
	release func()
	data    chan []byte
	done    chan struct{}
	err     error // set before done closes
	buf     []byte

	local, remote forwardAddr

	sendMu    sync.Mutex
	closeOnce sync.Once

	dlMu         sync.Mutex
	readDeadline chan struct{} // closed when the read deadline passes
	dlTimer      *time.Timer
}

func (c *forwardConn) readLoop() {
	defer close(c.done)
	defer close(c.data)
	for {
		f, err := c.stream.Recv()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				c.err = classify(err)
			}
			return
		}
		d := f.GetData()
		if len(d) == 0 {
			continue
		}
		select {
		case c.data <- d:
		case <-c.stream.Context().Done():
			return
		}
	}
}

func (c *forwardConn) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if len(c.buf) == 0 {
	wait:
		for {
			c.dlMu.Lock()
			deadline := c.readDeadline
			c.dlMu.Unlock()
			select {
			case d, ok := <-c.data:
				if !ok {
					if c.err != nil {
						return 0, c.err
					}
					return 0, io.EOF
				}
				c.buf = d
				break wait
			case <-deadline:
				// A deadline moved meanwhile does not count.
				c.dlMu.Lock()
				current := c.readDeadline == deadline
				c.dlMu.Unlock()
				if current {
					return 0, os.ErrDeadlineExceeded
				}
			}
		}
	}
	n := copy(p, c.buf)
	c.buf = c.buf[n:]
	return n, nil
}

func (c *forwardConn) Write(p []byte) (int, error) {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	// Large writes go in chunks so one frame stays well under gRPC's
	// message limit.
	written := 0
	for len(p) > 0 {
		chunk := p
		if len(chunk) > 64<<10 {
			chunk = chunk[:64<<10]
		}
		if err := c.stream.Send(&pb.TcpForwardFrame{Payload: &pb.TcpForwardFrame_Data{Data: append([]byte(nil), chunk...)}}); err != nil {
			// io.EOF: the gateway ended the stream; its status (why)
			// comes from the receiving side.
			if errors.Is(err, io.EOF) {
				select {
				case <-c.done:
				case <-time.After(5 * time.Second):
				}
				if c.err != nil {
					return written, c.err
				}
				return written, net.ErrClosed
			}
			return written, classify(err)
		}
		written += len(chunk)
		p = p[len(chunk):]
	}
	return written, nil
}

// CloseWrite tells the sandbox side that no more data comes.
func (c *forwardConn) CloseWrite() error {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	return c.stream.CloseSend()
}

func (c *forwardConn) Close() error {
	c.closeOnce.Do(func() {
		c.sendMu.Lock()
		_ = c.stream.CloseSend()
		c.sendMu.Unlock()
		c.cancel()
		<-c.done
		c.release()
	})
	return nil
}

func (c *forwardConn) LocalAddr() net.Addr  { return c.local }
func (c *forwardConn) RemoteAddr() net.Addr { return c.remote }

func (c *forwardConn) SetDeadline(t time.Time) error { return c.SetReadDeadline(t) }

// SetWriteDeadline is not supported: a write waits for gRPC's flow control.
func (c *forwardConn) SetWriteDeadline(time.Time) error { return nil }

func (c *forwardConn) SetReadDeadline(t time.Time) error {
	c.dlMu.Lock()
	defer c.dlMu.Unlock()
	if c.dlTimer != nil {
		c.dlTimer.Stop()
		c.dlTimer = nil
	}
	// Each deadline has a channel of its own, closed once when it passes.
	ch := make(chan struct{})
	c.readDeadline = ch
	if t.IsZero() {
		return nil
	}
	if d := time.Until(t); d <= 0 {
		close(ch)
	} else {
		c.dlTimer = time.AfterFunc(d, func() { close(ch) })
	}
	return nil
}
