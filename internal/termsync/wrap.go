package termsync

import (
	"io"
	"net"
)

// Wrap serves a terminal that has no session of its own (its output,
// input and size; the fake's) to one client: the returned
// connection carries termsync messages, the client's hello first. The
// terminal is closed when the client disconnects or its output ends.
func Wrap(t io.ReadWriteCloser, resize func(cols, rows int) error, cols, rows int) net.Conn {
	h := NewHub(cols, rows, func(b []byte) error {
		_, err := t.Write(b)
		return err
	}, resize)
	go func() {
		buf := make([]byte, 32<<10)
		for {
			n, err := t.Read(buf)
			if n > 0 {
				h.Feed(buf[:n])
			}
			if err != nil {
				h.Close()
				return
			}
		}
	}()
	a, b := net.Pipe()
	go func() {
		_ = h.Serve(b)
		t.Close()
	}()
	return a
}
