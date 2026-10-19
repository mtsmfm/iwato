package hostterm

import (
	"encoding/json"
	"errors"
	"io"
	"sync"
	"time"
)

// Recorder writes what a session's terminal receives, so that a broken
// screen can be reproduced later: the bytes the process output and the sizes
// the terminal was given, one JSON object per line.
//
//	{"k":"h","v":1,"at":"…","sandbox":"dev","term":"main","cols":80,"rows":24}
//	{"t":12,"k":"o","d":"<base64 of the output>"}
//	{"t":950,"k":"r","cols":120,"rows":40}
//	{"t":61000,"k":"e","why":"EOF"}
//
// t is the milliseconds since the header. Only the output is recorded, never
// the input, but the output holds whatever the program printed. Replaying the
// "o" lines into the same emulator (internal/screen), resizing at the "r"
// lines, gives the screen again. A resize is recorded where the session sees
// it, which can differ by a few output chunks from where the screen took it.
//
// A recorder stops at max bytes, with an "e" line, and on the first write
// error: it never blocks or fails the session.
type Recorder struct {
	mu      sync.Mutex
	w       io.Closer
	cw      *countWriter
	enc     *json.Encoder
	start   time.Time
	max     int64
	stopped bool
}

// TraceHeader describes the session a recording is of.
type TraceHeader struct {
	Sandbox, Term string
	Cols, Rows    int
}

type traceLine struct {
	T       int64  `json:"t"`
	K       string `json:"k"`
	V       int    `json:"v,omitempty"`
	At      string `json:"at,omitempty"`
	Sandbox string `json:"sandbox,omitempty"`
	Term    string `json:"term,omitempty"`
	Cols    int    `json:"cols,omitempty"`
	Rows    int    `json:"rows,omitempty"`
	D       []byte `json:"d,omitempty"`
	Why     string `json:"why,omitempty"`
}

// NewRecorder starts a recording on w (which it closes), up to max bytes.
func NewRecorder(w io.WriteCloser, h TraceHeader, max int64) *Recorder {
	cw := &countWriter{w: w}
	r := &Recorder{w: w, cw: cw, enc: json.NewEncoder(cw), start: time.Now(), max: max}
	r.put(traceLine{K: "h", V: 1, At: r.start.UTC().Format(time.RFC3339Nano), Sandbox: h.Sandbox, Term: h.Term, Cols: h.Cols, Rows: h.Rows})
	return r
}

// put writes one line; the caller holds mu (or is the constructor).
func (r *Recorder) put(l traceLine) {
	if r.stopped {
		return
	}
	if r.cw.n >= r.max {
		r.stopped = true
		_ = r.enc.Encode(traceLine{T: time.Since(r.start).Milliseconds(), K: "e", Why: "limit"})
		return
	}
	if err := r.enc.Encode(l); err != nil {
		r.stopped = true
	}
}

type countWriter struct {
	w io.Writer
	n int64
}

func (c *countWriter) Write(b []byte) (int, error) {
	n, err := c.w.Write(b)
	c.n += int64(n)
	return n, err
}

// Output records bytes the process output. Safe on a nil Recorder.
func (r *Recorder) Output(b []byte) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.put(traceLine{T: time.Since(r.start).Milliseconds(), K: "o", D: b})
}

// Resize records the terminal's size. Safe on a nil Recorder.
func (r *Recorder) Resize(cols, rows int) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.put(traceLine{T: time.Since(r.start).Milliseconds(), K: "r", Cols: cols, Rows: rows})
}

// End records why the output ended and closes the file. Safe on a nil
// Recorder.
func (r *Recorder) End(err error) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	why := "closed"
	if err != nil {
		why = err.Error()
	}
	r.put(traceLine{T: time.Since(r.start).Milliseconds(), K: "e", Why: why})
	r.stopped = true
	_ = r.w.Close()
}

// AsyncWriter hands writes to a goroutine, so that a slow or stuck
// destination (a process in a sandbox reached over the network) never holds
// up the session that records to it. The destination is opened by the
// goroutine, and what is written meanwhile waits in the queue. When the queue
// is full a Write fails, which stops the Recorder; when the destination
// cannot be opened or fails, what is written is dropped.
type AsyncWriter struct {
	ch   chan []byte
	once sync.Once
}

// ErrTraceBacklog is the error of a Write that finds the queue full.
var ErrTraceBacklog = errors.New("terminal recording is falling behind")

// NewAsyncWriter starts the goroutine, which calls open once; queue is the
// number of writes that may wait. An error from open is passed to failed.
func NewAsyncWriter(open func() (io.WriteCloser, error), failed func(error), queue int) *AsyncWriter {
	a := &AsyncWriter{ch: make(chan []byte, queue)}
	go func() {
		w, err := open()
		if err != nil {
			failed(err)
			w = nil
		}
		for b := range a.ch {
			if w == nil {
				continue
			}
			if _, err := w.Write(b); err != nil {
				failed(err)
				_ = w.Close()
				w = nil
			}
		}
		if w != nil {
			_ = w.Close()
		}
	}()
	return a
}

// Write queues a copy of b.
func (a *AsyncWriter) Write(b []byte) (int, error) {
	select {
	case a.ch <- append([]byte(nil), b...):
		return len(b), nil
	default:
		return 0, ErrTraceBacklog
	}
}

// Close lets the goroutine write what is queued, then close the destination.
// Write must not be called after it (the Recorder does not).
func (a *AsyncWriter) Close() error {
	a.once.Do(func() { close(a.ch) })
	return nil
}
