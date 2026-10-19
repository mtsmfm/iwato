package client

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	v1 "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1"
	"golang.org/x/crypto/ssh"
)

// MainProcess is a connection to a sandbox's canonical main process (its
// terminal), as `openshell sandbox connect` makes: the "openshell-main" SSH
// subsystem through the gateway's relay. On connect OpenShell replays up to
// 1 MiB of recent output; closing leaves the process running.
type MainProcess struct {
	client  *ssh.Client
	session *ssh.Session
	stdin   io.WriteCloser
	stdout  io.Reader
	// stderr keeps what OpenShell says on the channel's stderr, such as
	// why it cannot attach to the main process.
	mu         sync.Mutex
	stderr     bytes.Buffer
	stderrDone chan struct{}
	// lost is closed when the SSH connection (the relay) has ended;
	// closed is set when Close ended it.
	lost   chan struct{}
	closed atomic.Bool
}

// ErrRelayLost is Read's error when the output ended because the connection
// to the sandbox did (the gateway's relay, a restarting sandbox), not because
// OpenShell ended the main process's channel: attaching again may work.
var ErrRelayLost = errors.New("the connection to the sandbox was lost")

// AttachMain connects to the main process with a terminal of cols×rows and
// has it draw its screen again (repaint).
func AttachMain(ctx context.Context, sdk *v1.Client, workspace, sandbox string, cols, rows int) (*MainProcess, error) {
	c, err := DialSSH(ctx, sdk, workspace, sandbox)
	if err != nil {
		return nil, err
	}
	m, err := attach(c, cols, rows)
	if err != nil {
		c.Close()
		return nil, err
	}
	return m, nil
}

// DialSSH opens an SSH connection to the sandbox's supervisor through the
// gateway's relay, as `openshell sandbox connect` does. Its sessions take
// the supervisor's subsystems ("openshell-main", "sftp").
func DialSSH(ctx context.Context, sdk *v1.Client, workspace, sandbox string) (*ssh.Client, error) {
	// The port is not used for the SSH target; the SDK wants a valid one.
	tunnel, err := sdk.SSH().Tunnel(ctx, workspace, sandbox, 22)
	if err != nil {
		return nil, err
	}
	// The relay is authorized by the gateway (mutual TLS and a session
	// token), so SSH itself authenticates nothing.
	conn, chans, reqs, err := ssh.NewClientConn(rwConn{tunnel}, "sandbox", &ssh.ClientConfig{
		User:            "sandbox",
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         30 * time.Second,
	})
	if err != nil {
		tunnel.Close()
		return nil, err
	}
	return ssh.NewClient(conn, chans, reqs), nil
}

func attach(c *ssh.Client, cols, rows int) (*MainProcess, error) {
	s, err := c.NewSession()
	if err != nil {
		return nil, err
	}
	_ = s.Setenv("TERM", "xterm-256color")
	if err := s.RequestPty("xterm-256color", rows, cols, ssh.TerminalModes{}); err != nil {
		return nil, err
	}
	stdin, err := s.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := s.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := s.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := s.RequestSubsystem("openshell-main"); err != nil {
		return nil, err
	}
	m := &MainProcess{client: c, session: s, stdin: stdin, stdout: stdout, stderrDone: make(chan struct{}), lost: make(chan struct{})}
	m.repaint(cols, rows)
	go func() { _ = c.Wait(); close(m.lost) }()
	go func() {
		defer close(m.stderrDone)
		buf := make([]byte, 4096)
		for {
			n, err := stderr.Read(buf)
			m.mu.Lock()
			if m.stderr.Len() < 16<<10 {
				m.stderr.Write(buf[:n])
			}
			m.mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	return m, nil
}

// repaintDelay is how long the main process stays a row shorter: long
// enough for it to handle the first SIGWINCH before the second (Node.js
// reads the size when it handles the signal and ignores an unchanged one).
var repaintDelay = 200 * time.Millisecond

// repaint makes the main process draw its whole screen again, as tmux does
// on reattach: a session that attaches starts from a blank screen, and a
// program that only redraws what changed (Claude Code) would leave the rest
// of it blank. The kernel signals SIGWINCH only when the size changes, so
// the terminal shrinks by a row and comes back. Before the session uses m,
// so no other resize comes in between; iwato's screen keeps cols×rows.
func (m *MainProcess) repaint(cols, rows int) {
	if rows < 2 || m.session.WindowChange(rows-1, cols) != nil {
		return
	}
	time.Sleep(repaintDelay)
	_ = m.session.WindowChange(rows, cols)
}

// Read returns the terminal's output. When it ends, the error carries what
// OpenShell said on stderr, if anything (a subsystem session never
// "starts" in x/crypto/ssh, so there is no exit status to wait for).
func (m *MainProcess) Read(b []byte) (int, error) {
	n, err := m.stdout.Read(b)
	if err != io.EOF {
		return n, err
	}
	select {
	case <-m.stderrDone:
	case <-time.After(2 * time.Second):
	}
	m.mu.Lock()
	said := strings.TrimSpace(m.stderr.String())
	m.mu.Unlock()
	if said != "" {
		return n, errors.New(said)
	}
	// When the connection drops, x/crypto/ssh ends its channels (EOF) just
	// before the connection: an EOF of the channel alone is OpenShell's.
	if !m.closed.Load() {
		select {
		case <-m.lost:
			if !m.closed.Load() {
				return n, ErrRelayLost
			}
		case <-time.After(500 * time.Millisecond):
		}
	}
	return n, io.EOF
}
func (m *MainProcess) Write(b []byte) (int, error) { return m.stdin.Write(b) }
func (m *MainProcess) Resize(cols, rows int) error { return m.session.WindowChange(rows, cols) }

// Close detaches; the main process keeps running.
func (m *MainProcess) Close() error {
	m.closed.Store(true)
	m.session.Close()
	return m.client.Close()
}

// rwConn adapts the relay stream to the net.Conn that x/crypto/ssh takes.
type rwConn struct{ io.ReadWriteCloser }

func (rwConn) LocalAddr() net.Addr              { return relayAddr{} }
func (rwConn) RemoteAddr() net.Addr             { return relayAddr{} }
func (rwConn) SetDeadline(time.Time) error      { return nil }
func (rwConn) SetReadDeadline(time.Time) error  { return nil }
func (rwConn) SetWriteDeadline(time.Time) error { return nil }

type relayAddr struct{}

func (relayAddr) Network() string { return "openshell" }
func (relayAddr) String() string  { return "openshell-relay" }

// Shell is a new shell in the sandbox on a terminal of its own (an exec
// with a TTY). It ends with its connection.
type Shell struct{ v1.InteractiveSession }

// StartShell runs command with a cols×rows terminal.
func StartShell(ctx context.Context, sdk *v1.Client, workspace, sandbox string, command []string, o Options, cols, rows int) (*Shell, error) {
	s, err := sdk.Exec().Interactive(ctx, workspace, sandbox, command, uint32(cols), uint32(rows), v1.ExecOptions{Env: o.Env, WorkDir: o.Workdir, NoLoginShell: true})
	if err != nil {
		return nil, err
	}
	return &Shell{s}, nil
}

func (s *Shell) Resize(cols, rows int) error {
	return s.InteractiveSession.Resize(uint32(cols), uint32(rows))
}
