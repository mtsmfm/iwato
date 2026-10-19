package ingress

import (
	"context"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"sync"
	"time"
)

// Server is the provider-independent core: auth, protocol and backend.
type Server struct {
	Routes Routes
	Dialer Dialer
	Auth   Authorizer
	// Logf is the debug log (log.Printf when nil).
	Logf func(format string, args ...any)
	// AdminURL is the management page's address for the error pages
	// ("" when not known).
	AdminURL func() string
	// DetectWait is how long the first bytes are awaited (300 ms when
	// zero); DialWait bounds connecting to the sandbox (15 s when zero).
	DetectWait, DialWait time.Duration
	// Forwarded tells whether connections to the target go in (Admit);
	// nil: every port.
	Forwarded func(t Target) bool

	mu      sync.Mutex
	proxies map[string]*sandboxProxy
}

func (s *Server) logf(format string, args ...any) {
	if s.Logf != nil {
		s.Logf(format, args...)
		return
	}
	log.Printf(format, args...)
}

func (s *Server) detectWait() time.Duration {
	if s.DetectWait > 0 {
		return s.DetectWait
	}
	return 300 * time.Millisecond
}

// Admit refuses ports closed by the configuration and those Forwarded
// refuses.
func (s *Server) Admit(t Target) bool {
	if opts, ok := s.Routes.Options(t); !ok || opts.Disabled {
		return false
	}
	return s.Forwarded == nil || s.Forwarded(t)
}

// Serve handles one accepted connection until it ends.
func (s *Server) Serve(c Conn) {
	if c.Target == nil {
		// Every provider sets Target.
		_ = c.Close()
		return
	}
	t := *c.Target
	opts, ok := s.Routes.Options(t)
	if !ok || opts.Disabled {
		if c.HTTPOnly {
			s.serveError(c, t, ErrDisabled)
			return
		}
		_ = c.Close()
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	err := s.auth(c).AllowConn(ctx, c.Identity, t)
	cancel()
	if err != nil {
		s.logf("ingress %s:%d: refused %s: %v", t.Sandbox, t.Port, c.RemoteAddr(), err)
		_ = c.Close()
		return
	}
	mode := opts.Mode
	if c.HTTPOnly {
		// A provider of HTTP only asks nothing before it accepts: the
		// refusals are pages here (TCP mode cannot go through it).
		switch {
		case mode == ModeTCP:
			s.serveError(c, t, ErrTCPOnly)
			return
		case !s.Admit(t):
			s.serveError(c, t, ErrRefused)
			return
		}
		mode = ModeHTTP
	}
	if mode == ModeAuto {
		c.Conn, mode = detect(c.Conn, s.detectWait())
	}
	if mode == ModeHTTP {
		s.serveHTTP(c, t, opts)
		return
	}
	s.serveTCP(c, t)
}

// auth is the connection's provider's Authorizer, else the Server's.
func (s *Server) auth(c Conn) Authorizer {
	if c.Auth != nil {
		return c.Auth
	}
	return s.Auth
}

// serveTCP relays the connection to the sandbox's port as it is.
func (s *Server) serveTCP(c Conn, t Target) {
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), s.dialWait())
	up, err := s.Dialer.DialContext(ctx, t)
	cancel()
	if err != nil {
		s.logf("ingress %s:%d: TCP from %s: %v", t.Sandbox, t.Port, c.RemoteAddr(), err)
		return
	}
	defer up.Close()
	Relay(c.Conn, up)
}

func (s *Server) dialWait() time.Duration {
	if s.DialWait > 0 {
		return s.DialWait
	}
	return 15 * time.Second
}

// Relay copies both ways until both directions end, passing a half-close
// on when the connection can.
func Relay(a, b net.Conn) {
	var wg sync.WaitGroup
	copyHalf := func(dst, src net.Conn) {
		defer wg.Done()
		_, err := io.Copy(dst, src)
		if cw, ok := dst.(interface{ CloseWrite() error }); ok && err == nil {
			_ = cw.CloseWrite()
			return
		}
		// Without half-close (or after an error) both ends go.
		_ = dst.Close()
		_ = src.Close()
	}
	wg.Add(2)
	go copyHalf(b, a)
	go copyHalf(a, b)
	wg.Wait()
}

// Reset drops the sandbox's idle HTTP connections (it stopped or was
// deleted).
func (s *Server) Reset(sandbox string) {
	s.mu.Lock()
	p := s.proxies[sandbox]
	delete(s.proxies, sandbox)
	s.mu.Unlock()
	if p != nil {
		p.transport.CloseIdleConnections()
	}
}

// CloseIdle drops the sandbox's idle HTTP connections, freeing the places
// they hold (Backend's OnBusy).
func (s *Server) CloseIdle(sandbox string) {
	s.mu.Lock()
	p := s.proxies[sandbox]
	s.mu.Unlock()
	if p != nil {
		p.transport.CloseIdleConnections()
	}
}

// serveHTTP serves HTTP on the connection (HTTP/2 when the client chose
// it), each request through the sandbox's reverse proxy.
func (s *Server) serveHTTP(c Conn, t Target, opts PortOptions) {
	s.serveConnHTTP(c, s.handler(c, t, opts))
}

// serveError answers every request on the connection with err's page.
func (s *Server) serveError(c Conn, t Target, err error) {
	ri := &reqInfo{t: t, admin: c.AdminURL}
	s.serveConnHTTP(c, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Connection", "close")
		s.refusalPage(w, ri, err)
	}))
}

func (s *Server) serveConnHTTP(c Conn, h http.Handler) {
	ln := newOneConn(c.Conn)
	srv := &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 30 * time.Second,
		IdleTimeout:       5 * time.Minute,
		ErrorLog:          log.New(io.Discard, "", 0),
		ConnState: func(_ net.Conn, st http.ConnState) {
			if st == http.StateClosed || st == http.StateHijacked {
				ln.finish()
			}
		},
	}
	srv.Protocols = new(http.Protocols)
	srv.Protocols.SetHTTP1(true)
	srv.Protocols.SetHTTP2(true)
	srv.Protocols.SetUnencryptedHTTP2(true)
	_ = srv.Serve(ln)
}

// oneConn is a listener of a single connection: Accept returns it, then
// waits until the server is done with it.
type oneConn struct {
	conn net.Conn
	once sync.Once
	done chan struct{}
	took bool
	mu   sync.Mutex
}

func newOneConn(c net.Conn) *oneConn { return &oneConn{conn: c, done: make(chan struct{})} }

func (l *oneConn) Accept() (net.Conn, error) {
	l.mu.Lock()
	if !l.took {
		l.took = true
		l.mu.Unlock()
		return l.conn, nil
	}
	l.mu.Unlock()
	<-l.done
	return nil, net.ErrClosed
}

func (l *oneConn) finish()        { l.once.Do(func() { close(l.done) }) }
func (l *oneConn) Close() error   { l.finish(); return nil }
func (l *oneConn) Addr() net.Addr { return l.conn.LocalAddr() }

// isClientGone tells errors of a client that went away from the sandbox's.
func isClientGone(r *http.Request, err error) bool {
	return r.Context().Err() != nil || errors.Is(err, context.Canceled)
}
