package main

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// The debug log goes to the page's log view and iwato.log only; notices
// also go to the console.
func TestConsoleShowsOnlyNotices(t *testing.T) {
	var console bytes.Buffer
	var ring logRing
	path := filepath.Join(t.TempDir(), logFileName)
	restore, err := setupLogging(&console, &ring, path, false)
	if err != nil {
		t.Fatal(err)
	}
	log.Printf("gateway: listening")
	log.Writer().Write([]byte("#5 building layer\n")) // image build streaming
	noticef("compute driver stopped: boom")
	restore()
	if c := console.String(); strings.Contains(c, "gateway") || strings.Contains(c, "building") || !strings.Contains(c, "compute driver stopped: boom") {
		t.Fatalf("console: %q", c)
	}
	file, _ := os.ReadFile(path)
	lines, _ := ring.since(-1)
	for _, got := range []string{string(file), strings.Join(lines, "\n")} {
		for _, want := range []string{"gateway: listening", "#5 building layer", "compute driver stopped: boom"} {
			if !strings.Contains(got, want) {
				t.Fatalf("missing %q in %q", want, got)
			}
		}
	}
}

// -verbose echoes the debug log on the console too, notices once.
func TestVerboseEchoesTheDebugLog(t *testing.T) {
	var console bytes.Buffer
	var ring logRing
	restore, _ := setupLogging(&console, &ring, "", true)
	log.Printf("gateway: listening")
	noticef("port 17600 is in use")
	restore()
	c := console.String()
	if !strings.Contains(c, "gateway: listening") || strings.Count(c, "port 17600 is in use") != 1 {
		t.Fatalf("console: %q", c)
	}
}

func TestLogFileRotates(t *testing.T) {
	path := filepath.Join(t.TempDir(), logFileName)
	f, err := openRotating(path, 100)
	if err != nil {
		t.Fatal(err)
	}
	first, second, third := strings.Repeat("a", 60)+"\n", strings.Repeat("b", 60)+"\n", strings.Repeat("c", 60)+"\n"
	f.Write([]byte(first))
	f.Write([]byte(second))
	f.Write([]byte(third))
	f.Close()
	cur, _ := os.ReadFile(path)
	old, _ := os.ReadFile(path + ".1")
	if string(cur) != third || string(old) != second {
		t.Fatalf("%q %q", cur, old)
	}
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}
func (b *syncBuffer) String() string { b.mu.Lock(); defer b.mu.Unlock(); return b.b.String() }

func waitFor(t *testing.T, out *syncBuffer, s string) string {
	t.Helper()
	for i := 0; i < 200; i++ {
		if strings.Contains(out.String(), s) {
			return out.String()
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no %q in %q", s, out.String())
	return ""
}

var loginURL = regexp.MustCompile(`(http://127\.0\.0\.1:\d+/)(#token=[0-9a-f]+)`)
