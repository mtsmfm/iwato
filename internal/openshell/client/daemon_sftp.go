package client

import (
	"context"
	"io"
	"sync"

	"github.com/pkg/sftp"
	"google.golang.org/grpc"
)

// DaemonSFTP opens SFTP channels on the daemon's SFTP server
// (`iwato-entrypoint sftp`, rooted at Root): each channel is a TTY-less
// exec of it, as the sandbox user, whose stdin and stdout carry SFTP.
type DaemonSFTP struct {
	Conn      grpc.ClientConnInterface
	Workspace string
	Sandbox   string
	// Bin is the daemon's path in the sandbox, Root the folder it serves.
	Bin, Root string

	mu     sync.Mutex
	procs  map[*Process]struct{}
	closed bool
}

// OpenSFTP starts one more server: what it sends, and what goes to it
// (closing that ends it).
func (d *DaemonSFTP) OpenSFTP() (io.Reader, io.WriteCloser, error) {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return nil, nil, io.ErrClosedPipe
	}
	d.mu.Unlock()
	// The process lives as long as the channel, not the request that opened
	// it (the pool bounds the opening).
	p, err := Start(context.Background(), d.Conn, d.Workspace, d.Sandbox, termCommand(d.Bin, "sftp", "-root", d.Root), Options{})
	if err != nil {
		return nil, nil, err
	}
	d.mu.Lock()
	if d.procs == nil {
		d.procs = map[*Process]struct{}{}
	}
	d.procs[p] = struct{}{}
	d.mu.Unlock()
	return p, daemonChannel{p, d}, nil
}

// ClientOptions: the server (pkg/sftp's) takes packets up to 256 KiB, the
// exec's messages carry 64 KiB ones well.
func (d *DaemonSFTP) ClientOptions() []sftp.ClientOption {
	return []sftp.ClientOption{sftp.MaxPacketUnchecked(64 << 10), sftp.UseConcurrentWrites(true)}
}

// Close ends every server it started.
func (d *DaemonSFTP) Close() error {
	d.mu.Lock()
	d.closed = true
	procs := d.procs
	d.procs = nil
	d.mu.Unlock()
	for p := range procs {
		_ = p.CloseWrite()
		_ = p.Close()
	}
	return nil
}

type daemonChannel struct {
	p *Process
	d *DaemonSFTP
}

func (c daemonChannel) Write(b []byte) (int, error) { return c.p.Write(b) }

// Close ends the server's input (it then exits) and the exec.
func (c daemonChannel) Close() error {
	c.d.mu.Lock()
	delete(c.d.procs, c.p)
	c.d.mu.Unlock()
	err := c.p.CloseWrite()
	_ = c.p.Close()
	return err
}
