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

func TestTrack(t *testing.T) {
	var logs syncBuffer
	defer log.SetOutput(log.Writer())
	log.SetOutput(&logs)
	defer func(s time.Duration) { stallAfter = s }(stallAfter)
	stallAfter = 20 * time.Millisecond

	secret := strings.Repeat("a", 52)
	r := httptest.NewRequest("PROPFIND", "/dav/"+secret+"/dev/x", nil)
	r.Header.Set("Depth", "1")
	Track(httptest.NewRecorder(), r, func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(60 * time.Millisecond)
		w.WriteHeader(http.StatusMultiStatus)
	})
	got := logs.String()
	if !strings.Contains(got, "1234 PROPFIND /dav/<secret>/dev/x depth=1 still running") || !strings.Contains(got, "depth=1 -> 207 in") || strings.Contains(got, secret) {
		t.Fatalf("%q", got)
	}
}
