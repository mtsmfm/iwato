package dav

import (
	"errors"
	"io"
	"net"
	"os"
	"path"
	"sync"

	"github.com/pkg/sftp"
)

// MemServer is an SFTP server on memory (pkg/sftp's InMemHandler, whose
// rename refuses an existing destination as OpenShell's does). The fake
// and the tests use it in place of a sandbox.
type MemServer struct{ h sftp.Handlers }

func NewMemServer() *MemServer { return &MemServer{h: sftp.InMemHandler()} }

// Transport is a new connection to the server.
func (m *MemServer) Transport() Transport { return &memTransport{m: m} }

// WriteFile puts a file (and the directories above it) on the server.
func (m *MemServer) WriteFile(p string, data []byte) error {
	t := m.Transport()
	defer t.Close()
	c, err := NewClient(t)
	if err != nil {
		return err
	}
	if dir := path.Dir(p); dir != "." {
		if err := c.MkdirAll(dir); err != nil {
			return err
		}
	}
	f, err := c.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// Symlink makes link a symbolic link to target (relative to link's folder).
func (m *MemServer) Symlink(target, link string) error {
	t := m.Transport()
	defer t.Close()
	c, err := NewClient(t)
	if err != nil {
		return err
	}
	return c.Symlink(target, link)
}

type memTransport struct {
	m      *MemServer
	mu     sync.Mutex
	pipes  []net.Conn
	closed bool
}

func (t *memTransport) OpenSFTP() (io.Reader, io.WriteCloser, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil, nil, errors.New("transport closed")
	}
	a, b := net.Pipe()
	srv := sftp.NewRequestServer(a, t.m.h)
	go func() {
		_ = srv.Serve()
		_ = srv.Close()
	}()
	t.pipes = append(t.pipes, b)
	return b, b, nil
}

func (t *memTransport) Close() error {
	t.mu.Lock()
	pipes := t.pipes
	t.pipes, t.closed = nil, true
	t.mu.Unlock()
	for _, c := range pipes {
		_ = c.Close()
	}
	return nil
}
