package hostterm

import (
	"io"
	"strings"
	"testing"
	"time"

	"iwato/internal/termsync/termsynctest"
)

// fakeBackend echoes input as output, like a shell's terminal would.
type fakeBackend struct {
	r     *io.PipeReader
	w     *io.PipeWriter
	sizes chan [2]int
}

func newFake() *fakeBackend {
	r, w := io.Pipe()
	return &fakeBackend{r: r, w: w, sizes: make(chan [2]int, 8)}
}
func (f *fakeBackend) Read(b []byte) (int, error)  { return f.r.Read(b) }
func (f *fakeBackend) Write(b []byte) (int, error) { return f.w.Write(b) }
func (f *fakeBackend) Resize(c, r int) error       { f.sizes <- [2]int{c, r}; return nil }
func (f *fakeBackend) Close() error                { return f.w.Close() }

func attach(t *testing.T, s *Session, cols, rows int) (*Client, *termsynctest.Client) {
	t.Helper()
	c, err := s.Attach()
	if err != nil {
		t.Fatal(err)
	}
	v, err := termsynctest.Start(c, cols, rows)
	if err != nil {
		t.Fatal(err)
	}
	return c, v
}

func waitFor(t *testing.T, v *termsynctest.Client, want string) string {
	t.Helper()
	text, err := v.WaitFor(want, 5*time.Second)
	if err != nil {
		t.Fatalf("%v; got %q", err, text)
	}
	return text
}

func TestSessionOutlivesItsClients(t *testing.T) {
	f := newFake()
	s := New(f, 80, 24)
	// Output from before (a reattached process's replay) builds the screen.
	go f.w.Write([]byte("replayed\r\n"))
	c, v := attach(t, s, 80, 24)
	waitFor(t, v, "replayed")
	if err := v.Input([]byte("live-1\r\n")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, v, "live-1")
	c.Close()

	// A new client gets the screen so far, at its size.
	_, second := attach(t, s, 100, 30)
	if size := <-f.sizes; size != [2]int{100, 30} {
		t.Fatalf("resized to %v", size)
	}
	if out := waitFor(t, second, "live-1"); !strings.Contains(out, "replayed") {
		t.Fatalf("screen %q", out)
	}

	f.w.Close()
	select {
	case <-s.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("session did not end with its output")
	}
	if err := second.Ended(5 * time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Attach(); err != ErrEnded {
		t.Fatal(err)
	}
}

type bufCloser struct{ strings.Builder }

func (*bufCloser) Close() error { return nil }

func TestRecorderKeepsOutputAndSizes(t *testing.T) {
	var out bufCloser
	rec := NewRecorder(&out, TraceHeader{Sandbox: "dev", Term: "main", Cols: 80, Rows: 24}, 1<<20)
	f := newFake()
	s := NewRecorded(f, 80, 24, rec)
	go f.w.Write([]byte("hello\r\n"))
	_, v := attach(t, s, 100, 30)
	waitFor(t, v, "hello")
	<-f.sizes
	f.w.Close()
	<-s.Done()

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) < 4 {
		t.Fatalf("lines %q", lines)
	}
	if !strings.HasPrefix(lines[0], `{"t":0,"k":"h","v":1,`) || !strings.Contains(lines[0], `"sandbox":"dev"`) || !strings.Contains(lines[0], `"cols":80`) {
		t.Errorf("header %q", lines[0])
	}
	text := out.String()
	// base64 of "hello\r\n"
	if !strings.Contains(text, `"k":"o","d":"aGVsbG8NCg=="`) {
		t.Errorf("no output line in %q", text)
	}
	if !strings.Contains(text, `"k":"r","cols":100,"rows":30`) {
		t.Errorf("no resize line in %q", text)
	}
	// Only a change of size is recorded, not every client's request for it.
	if n := strings.Count(text, `"k":"r"`); n != 1 {
		t.Errorf("%d resize lines in %q", n, text)
	}
	if !strings.Contains(lines[len(lines)-1], `"k":"e"`) {
		t.Errorf("no end line: %q", lines[len(lines)-1])
	}
}

func TestRecorderStopsAtItsLimit(t *testing.T) {
	var out bufCloser
	rec := NewRecorder(&out, TraceHeader{Cols: 80, Rows: 24}, 200)
	for i := 0; i < 20; i++ {
		rec.Output([]byte("0123456789"))
	}
	rec.End(nil)
	text := out.String()
	if strings.Count(text, `"k":"e"`) != 1 || !strings.Contains(text, `"why":"limit"`) {
		t.Errorf("recording %q", text)
	}
	var nilRec *Recorder
	nilRec.Output([]byte("x"))
	nilRec.Resize(1, 1)
	nilRec.End(nil)
}

func TestAsyncWriterNeverBlocksTheRecorder(t *testing.T) {
	// A destination that never opens: writes queue, then fail instead of blocking.
	block := make(chan struct{})
	defer close(block)
	failed := make(chan error, 4)
	a := NewAsyncWriter(func() (io.WriteCloser, error) { <-block; return nil, io.EOF }, func(err error) { failed <- err }, 2)
	rec := NewRecorder(a, TraceHeader{Cols: 80, Rows: 24}, 1<<20)
	for i := 0; i < 10; i++ {
		rec.Output([]byte("x")) // returns at once even though nothing drains
	}
	rec.End(nil)
}

func TestAsyncWriterDeliversInOrderAndCloses(t *testing.T) {
	var out bufCloser
	closed := make(chan struct{})
	a := NewAsyncWriter(func() (io.WriteCloser, error) { return &closeSignal{&out, closed}, nil }, func(error) {}, 8)
	a.Write([]byte("a"))
	a.Write([]byte("b"))
	a.Close()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("the destination was not closed")
	}
	if out.String() != "ab" {
		t.Fatalf("got %q", out.String())
	}
}

type closeSignal struct {
	*bufCloser
	c chan struct{}
}

func (s *closeSignal) Close() error { close(s.c); return nil }
