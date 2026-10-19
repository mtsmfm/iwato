package ingress

import (
	"bufio"
	"crypto/tls"
	"net"
	"time"
)

// PeekedConn is a connection whose first bytes were looked at. Idle: the
// client sent nothing within the wait (a protocol where the server speaks
// first, as MySQL or SMTP).
type PeekedConn struct {
	net.Conn
	r    *bufio.Reader
	Idle bool
}

// Peek waits up to wait for the first n bytes of c (fewer when the client
// stops sending) and returns them with a connection that still reads them.
func Peek(c net.Conn, n int, wait time.Duration) (*PeekedConn, []byte) {
	p, ok := c.(*PeekedConn)
	if !ok {
		p = &PeekedConn{Conn: c, r: bufio.NewReaderSize(c, 4096)}
	}
	if p.r.Buffered() >= n {
		b, _ := p.r.Peek(n)
		return p, b
	}
	_ = c.SetReadDeadline(time.Now().Add(wait))
	b, _ := p.r.Peek(n)
	_ = c.SetReadDeadline(time.Time{})
	p.Idle = len(b) == 0
	return p, b
}

func (p *PeekedConn) Read(b []byte) (int, error) { return p.r.Read(b) }

// CloseWrite half-closes when the connection can.
func (p *PeekedConn) CloseWrite() error {
	if cw, ok := p.Conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return nil
}

// httpStarts are the beginnings of HTTP/1 requests and the HTTP/2 preface.
var httpStarts = []string{"GET ", "HEAD ", "POST ", "PUT ", "DELETE ", "OPTIONS ", "PATCH ", "CONNECT ", "TRACE ", "PRI * HTTP/2"}

// looksHTTP reports whether b starts like an HTTP request (b may be cut).
func looksHTTP(b []byte) bool {
	if len(b) == 0 {
		return false
	}
	for _, m := range httpStarts {
		n := min(len(b), len(m))
		if string(b[:n]) == m[:n] {
			return true
		}
	}
	return false
}

// detect tells HTTP from anything else: the ALPN a TLS client chose, else
// the first bytes (waiting up to wait for them).
func detect(c net.Conn, wait time.Duration) (net.Conn, Mode) {
	if tc, ok := c.(*tls.Conn); ok {
		switch tc.ConnectionState().NegotiatedProtocol {
		case "h2", "http/1.1":
			return c, ModeHTTP
		}
	}
	if p, ok := c.(*PeekedConn); ok && p.Idle {
		return c, ModeTCP
	}
	p, b := Peek(c, 8, wait)
	if looksHTTP(b) {
		return p, ModeHTTP
	}
	return p, ModeTCP
}
