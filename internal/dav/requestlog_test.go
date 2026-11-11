package dav

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// syncBuffer is written by Track's timer and read by the test.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *syncBuffer) String() string { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }
func (s *syncBuffer) Reset()         { s.mu.Lock(); defer s.mu.Unlock(); s.b.Reset() }

func TestTrack(t *testing.T) {
	var logs syncBuffer
	defer log.SetOutput(log.Writer())
	log.SetOutput(&logs)
	defer func(s, w time.Duration) { stallAfter, slowAfter = s, w }(stallAfter, slowAfter)
	stallAfter, slowAfter = 20*time.Millisecond, time.Hour

	secret := strings.Repeat("a", 52)
	serve := func(path string, h func(http.ResponseWriter, *http.Request)) {
		logs.Reset()
		Track(httptest.NewRecorder(), httptest.NewRequest("PROPFIND", path, nil), h)
	}
	serve("/dav/"+secret+"/dev/x", func(w http.ResponseWriter, _ *http.Request) { time.Sleep(60 * time.Millisecond) })
	if got := logs.String(); !strings.Contains(got, "PROPFIND /dav/<secret>/dev/x still running") || strings.Contains(got, secret) {
		t.Fatalf("a stalled request: %q", got)
	}
	serve("/dav/dev/x", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusLocked) })
	if got := logs.String(); !strings.Contains(got, "PROPFIND /dav/dev/x: 423") {
		t.Fatalf("a refusal: %q", got)
	}
	serve("/dav/dev/desktop.ini", func(w http.ResponseWriter, _ *http.Request) { http.NotFound(w, nil) })
	serve("/dav/dev/", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	if logs.String() != "" {
		t.Fatalf("a 404 or a quick answer: %q", logs.String())
	}
}
