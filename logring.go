package main

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"sync"
)

// Iwato logs at two levels:
//   - the standard logger (log.Printf, log.Writer) is the debug log: the
//     gateway's and the driver's output, image builds, requests. It goes to
//     the page's log view (logRing) and to iwato.log, and to the console
//     only with -verbose;
//   - noticef is for what the owner has to see on the console (the driver
//     stopping, port problems, ...). It goes to the same places too.
var notice = log.New(os.Stderr, "", log.LstdFlags)

func noticef(format string, a ...any) { notice.Printf(format, a...) }

// logFileName is the debug log next to the configuration; logFileMax is
// when it is rotated (one previous file, .1, is kept).
const (
	logFileName = "iwato.log"
	logFileMax  = 10 << 20
)

// setupLogging routes the debug log to ring and the file at path (none when
// empty), and also to console with verbose; notices go to all of them. The
// returned func restores the previous outputs and closes the file.
func setupLogging(console io.Writer, ring *logRing, path string, verbose bool) (func(), error) {
	prevOut, prevFlags, prevNotice := log.Writer(), log.Flags(), notice.Writer()
	sinks := []io.Writer{ring}
	var file *rotatingFile
	var err error
	if path != "" {
		if file, err = openRotating(path, logFileMax); err == nil {
			sinks = append(sinks, file)
		}
	}
	debug := io.MultiWriter(sinks...)
	all := io.MultiWriter(append([]io.Writer{console}, sinks...)...)
	if verbose {
		debug = all
	}
	log.SetOutput(debug)
	log.SetFlags(log.LstdFlags)
	notice.SetOutput(all)
	return func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
		notice.SetOutput(prevNotice)
		if file != nil {
			file.Close()
		}
	}, err
}

// rotatingFile appends to a file and, once it reaches max bytes, moves it
// to path.1 (replacing the previous one) and starts a new one.
type rotatingFile struct {
	mu   sync.Mutex
	path string
	max  int64
	f    *os.File
	size int64
}

func openRotating(path string, max int64) (*rotatingFile, error) {
	r := &rotatingFile{path: path, max: max}
	if err := r.open(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *rotatingFile) open() error {
	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	r.f, r.size = f, st.Size()
	return nil
}

func (r *rotatingFile) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		return len(p), nil
	}
	if r.size > 0 && r.size+int64(len(p)) > r.max {
		r.f.Close()
		r.f = nil
		// Windows does not rename over an existing file.
		_ = os.Remove(r.path + ".1")
		_ = os.Rename(r.path, r.path+".1")
		if err := r.open(); err != nil {
			// Logging must not fail Iwato's work: drop lines until restart.
			fmt.Fprintln(os.Stderr, "log file:", err)
			return len(p), nil
		}
	}
	n, err := r.f.Write(p)
	r.size += int64(n)
	if err != nil {
		return len(p), nil
	}
	return n, nil
}

func (r *rotatingFile) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		return nil
	}
	err := r.f.Close()
	r.f = nil
	return err
}

// logRing keeps the last lines Iwato logged (the preparation's steps, image
// builds, the gateway's and the driver's output) for the page's log view.
type logRing struct {
	mu      sync.Mutex
	lines   []string
	next    int // sequence number of the line after the last one
	partial []byte
}

const logRingLines = 2000

func (r *logRing) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	data := append(r.partial, p...)
	for {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			break
		}
		r.add(string(bytes.TrimRight(data[:i], "\r")))
		data = data[i+1:]
	}
	if len(data) > 4096 {
		r.add(string(data))
		data = nil
	}
	r.partial = append([]byte(nil), data...)
	return len(p), nil
}

func (r *logRing) add(line string) {
	r.lines = append(r.lines, line)
	if len(r.lines) > logRingLines {
		r.lines = r.lines[len(r.lines)-logRingLines:]
	}
	r.next++
}

// since returns the lines after sequence number after (all kept lines for
// -1) and the next sequence number.
func (r *logRing) since(after int) ([]string, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	first := r.next - len(r.lines)
	start := 0
	if after >= first {
		start = after - first
	}
	if start > len(r.lines) {
		start = len(r.lines)
	}
	return append([]string{}, r.lines[start:]...), r.next
}

// handleLog returns the log lines after ?after=N (all kept lines without
// it), so the page can follow it.
func (s *Server) handleLog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		fail(w, 405, "method not allowed")
		return
	}
	if !s.require(w, r, false) {
		return
	}
	after := -1
	if v := r.URL.Query().Get("after"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			fail(w, 400, "invalid after")
			return
		}
		after = n
	}
	lines, next := s.logs.since(after)
	jsonResponse(w, 200, map[string]any{"lines": lines, "next": next})
}
