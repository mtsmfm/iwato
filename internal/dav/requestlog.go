package dav

import (
	"log"
	"net/http"
	"strings"
	"time"
)

// Thresholds of Track: a request still running at stallAfter is logged
// then (a client such as Windows' WebClient waits on it and queues the
// rest behind its two connections), one that ends after slowAfter when it
// ends.
var (
	stallAfter = 30 * time.Second
	slowAfter  = 5 * time.Second
)

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

// Track serves r with next and logs what a client may get stuck on: a
// request still running after stallAfter, one that took over slowAfter,
// and refusals other than the 401 and 404 clients cause by asking.
func Track(w http.ResponseWriter, r *http.Request, next func(http.ResponseWriter, *http.Request)) {
	start := time.Now()
	path := RedactPath(r.URL.Path)
	stall := time.AfterFunc(stallAfter, func() {
		log.Printf("files: %s %s still running after %s", r.Method, path, stallAfter)
	})
	sw := &statusWriter{ResponseWriter: w}
	defer func() {
		stall.Stop()
		took := time.Since(start).Round(time.Millisecond)
		status := sw.status
		if status == 0 {
			status = http.StatusOK
		}
		refused := status >= 400 && status != http.StatusUnauthorized && status != http.StatusNotFound
		if refused || took > slowAfter {
			log.Printf("files: %s %s: %d in %s", r.Method, path, status, took)
		}
	}()
	next(sw, r)
}
