package dav

import (
	"log"
	"net"
	"net/http"
	"strings"
	"time"
)

// stallAfter is when Track logs a request still running (a client such
// as Windows' WebClient waits on it and queues the rest behind its two
// connections).
var stallAfter = 30 * time.Second

// RedactPath hides a secret URL's token, the first segment after /dav/
// when it is longer than a sandbox name can be.
func RedactPath(p string) string {
	rest, ok := strings.CutPrefix(p, "/dav/")
	if !ok {
		return p
	}
	first, after, cut := strings.Cut(rest, "/")
	if len(first) <= 32 {
		return p
	}
	if cut {
		return "/dav/<secret>/" + after
	}
	return "/dav/<secret>"
}

// statusWriter records the status; a response written without
// WriteHeader is a 200.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// Track serves r with next and logs it: every request when it ends, with
// the client's port (which connection), Depth, status and time, and one
// still running after stallAfter at that moment.
func Track(w http.ResponseWriter, r *http.Request, next func(http.ResponseWriter, *http.Request)) {
	start := time.Now()
	req := r.Method + " " + RedactPath(r.URL.Path)
	if d := r.Header.Get("Depth"); d != "" {
		req += " depth=" + strings.ToValidUTF8(d[:min(len(d), 10)], "?")
	}
	if _, port, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		req = port + " " + req
	}
	req = strings.Map(func(c rune) rune {
		if c < 0x20 || c == 0x7f {
			return '?'
		}
		return c
	}, req)
	stall := time.AfterFunc(stallAfter, func() {
		log.Printf("files: %s still running after %s", req, stallAfter)
	})
	sw := &statusWriter{ResponseWriter: w}
	defer func() {
		stall.Stop()
		status := sw.status
		if status == 0 {
			status = http.StatusOK
		}
		log.Printf("files: %s -> %d in %s", req, status, time.Since(start).Round(time.Millisecond))
	}()
	next(sw, r)
}
