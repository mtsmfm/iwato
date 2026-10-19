package dav

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pkg/sftp"
)

type env struct {
	t       *testing.T
	mem     *MemServer
	pool    *Pool
	srv     *httptest.Server
	stopped atomic.Bool
	dials   atomic.Int32
	freeze  atomic.Bool // the server stops answering
}

// newEnv serves sandbox "dev" at /dav/dev (read-write) and /ro/dev
// (read-only) from an SFTP server on memory.
func newEnv(t *testing.T) *env {
	e := &env{t: t, mem: NewMemServer()}
	e.pool = NewPool(func(ctx context.Context, name string) (Transport, error) {
		e.dials.Add(1)
		if e.stopped.Load() {
			return nil, ErrStopped
		}
		return &freezable{Transport: e.mem.Transport(), freeze: &e.freeze, closed: make(chan struct{})}, nil
	})
	mux := http.NewServeMux()
	mux.HandleFunc("/dav/dev/", func(w http.ResponseWriter, r *http.Request) {
		e.pool.ServeSandbox(w, r, "dev", Options{Prefix: "/dav/dev"})
	})
	mux.HandleFunc("/ro/dev/", func(w http.ResponseWriter, r *http.Request) {
		e.pool.ServeSandbox(w, r, "dev", Options{Prefix: "/ro/dev", ReadOnly: true})
	})
	e.srv = httptest.NewServer(mux)
	t.Cleanup(func() { e.srv.Close(); e.pool.CloseAll() })
	must(t, e.mem.WriteFile("README.md", []byte("# hello\n")))
	must(t, e.mem.WriteFile("src/main.go", []byte("package main\n")))
	return e
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func (e *env) do(method, path string, body io.Reader, header ...string) (*http.Response, string) {
	e.t.Helper()
	req, err := http.NewRequest(method, e.srv.URL+path, body)
	must(e.t, err)
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	res, err := http.DefaultClient.Do(req)
	must(e.t, err)
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	return res, string(b)
}

func (e *env) expect(want int, method, path string, body io.Reader, header ...string) string {
	e.t.Helper()
	res, text := e.do(method, path, body, header...)
	if res.StatusCode != want {
		e.t.Fatalf("%s %s: %d, want %d: %s", method, path, res.StatusCode, want, text)
	}
	return text
}

// read reads a file straight from the SFTP server.
func (e *env) read(p string) (string, error) {
	tr := e.mem.Transport()
	defer tr.Close()
	c, err := NewClient(tr)
	if err != nil {
		return "", err
	}
	f, err := c.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	b, err := io.ReadAll(f)
	return string(b), err
}

func (e *env) names(dir string) []string {
	tr := e.mem.Transport()
	defer tr.Close()
	c, err := NewClient(tr)
	must(e.t, err)
	list, err := c.ReadDir(dir)
	must(e.t, err)
	var out []string
	for _, fi := range list {
		out = append(out, fi.Name())
	}
	return out
}

func TestPropfind(t *testing.T) {
	e := newEnv(t)
	body := e.expect(207, "PROPFIND", "/dav/dev/", nil, "Depth", "1")
	for _, want := range []string{"<D:href>/dav/dev/</D:href>", "/dav/dev/README.md", "/dav/dev/src/", "text/markdown"} {
		if !strings.Contains(body, want) {
			t.Errorf("listing lacks %q:\n%s", want, body)
		}
	}
	// A PUT in progress leaves its temporary file out of listings.
	must(t, e.mem.WriteFile(".a.txt.iwato-0123456789ab", []byte("x")))
	if body := e.expect(207, "PROPFIND", "/dav/dev/", nil, "Depth", "1"); strings.Contains(body, "iwato-0123") {
		t.Errorf("temporary file listed:\n%s", body)
	}
	// No Depth means 1, not the whole tree.
	if body := e.expect(207, "PROPFIND", "/dav/dev/", nil); strings.Contains(body, "main.go") {
		t.Errorf("PROPFIND without Depth walked the tree:\n%s", body)
	}
	if body := e.expect(403, "PROPFIND", "/dav/dev/", nil, "Depth", "infinity"); !strings.Contains(body, "propfind-finite-depth") {
		t.Errorf("Depth: infinity answer: %s", body)
	}
	e.expect(404, "PROPFIND", "/dav/dev/missing", nil, "Depth", "0")
}

func TestGetRange(t *testing.T) {
	e := newEnv(t)
	data := make([]byte, 1500000)
	for i := range data {
		data[i] = byte(i * 7)
	}
	must(t, e.mem.WriteFile("big.bin", data))
	if got := e.expect(200, "GET", "/dav/dev/big.bin", nil); got != string(data) {
		t.Fatalf("GET: %d bytes, want %d", len(got), len(data))
	}
	res, got := e.do("GET", "/dav/dev/big.bin", nil, "Range", "bytes=1200000-1200009")
	if res.StatusCode != 206 || got != string(data[1200000:1200010]) {
		t.Fatalf("Range: %d %q", res.StatusCode, got)
	}
	res, got = e.do("GET", "/dav/dev/big.bin", nil, "Range", "bytes=-5")
	if res.StatusCode != 206 || got != string(data[len(data)-5:]) {
		t.Fatalf("suffix Range: %d %q", res.StatusCode, got)
	}
	if res, _ := e.do("HEAD", "/dav/dev/README.md", nil); res.Header.Get("Content-Type") != "text/markdown; charset=utf-8" && !strings.HasPrefix(res.Header.Get("Content-Type"), "text/markdown") {
		t.Errorf("Content-Type: %q", res.Header.Get("Content-Type"))
	}
}

func TestPut(t *testing.T) {
	e := newEnv(t)
	e.expect(201, "PUT", "/dav/dev/new.txt", strings.NewReader("one"))
	if s, _ := e.read("new.txt"); s != "one" {
		t.Fatalf("new file: %q", s)
	}
	// Replacing: OpenShell's rename does not overwrite.
	e.expect(201, "PUT", "/dav/dev/new.txt", strings.NewReader("two, longer"))
	if s, _ := e.read("new.txt"); s != "two, longer" {
		t.Fatalf("replaced file: %q", s)
	}
	e.expect(201, "PUT", "/dav/dev/src/日本語 ファイル.txt", strings.NewReader("ja"))
	if s, _ := e.read("src/日本語 ファイル.txt"); s != "ja" {
		t.Fatalf("Japanese name: %q", s)
	}
	for _, n := range append(e.names("."), e.names("src")...) {
		if strings.Contains(n, ".iwato-") {
			t.Errorf("temporary file left: %s", n)
		}
	}
	// No parent: 409.
	e.expect(409, "PUT", "/dav/dev/nodir/a.txt", strings.NewReader("x"))
	// Finder's files are files like any other.
	e.expect(201, "PUT", "/dav/dev/._new.txt", strings.NewReader("apple"))
	e.expect(201, "PUT", "/dav/dev/.DS_Store", strings.NewReader("ds"))
	if s, _ := e.read(".DS_Store"); s != "ds" {
		t.Fatalf(".DS_Store: %q", s)
	}
}

type failingReader struct{ n int }

func (f *failingReader) Read(b []byte) (int, error) {
	if f.n <= 0 {
		return 0, errors.New("client went away")
	}
	n := min(len(b), f.n)
	for i := range b[:n] {
		b[i] = 'x'
	}
	f.n -= n
	return n, nil
}

func TestPutInterrupted(t *testing.T) {
	e := newEnv(t)
	must(t, e.mem.WriteFile("keep.txt", []byte("original")))
	req := httptest.NewRequest("PUT", "/dav/dev/keep.txt", &failingReader{n: 100000})
	rec := httptest.NewRecorder()
	e.pool.ServeSandbox(rec, req, "dev", Options{Prefix: "/dav/dev"})
	if rec.Code < 400 {
		t.Fatalf("interrupted PUT answered %d", rec.Code)
	}
	if s, _ := e.read("keep.txt"); s != "original" {
		t.Fatalf("interrupted PUT changed the file: %q", s)
	}
	for _, n := range e.names(".") {
		if strings.Contains(n, ".iwato-") {
			t.Errorf("temporary file left: %s", n)
		}
	}
}

func TestMkcolMoveCopyDelete(t *testing.T) {
	e := newEnv(t)
	e.expect(201, "MKCOL", "/dav/dev/docs", nil)
	e.expect(405, "MKCOL", "/dav/dev/docs", nil)
	e.expect(201, "PUT", "/dav/dev/docs/a.txt", strings.NewReader("A"))
	e.expect(201, "PUT", "/dav/dev/docs/b.txt", strings.NewReader("B"))
	dest := func(p string) string { return e.srv.URL + p }

	// MOVE onto an existing file: refused without Overwrite: T.
	e.expect(412, "MOVE", "/dav/dev/docs/a.txt", nil, "Destination", dest("/dav/dev/docs/b.txt"), "Overwrite", "F")
	e.expect(204, "MOVE", "/dav/dev/docs/a.txt", nil, "Destination", dest("/dav/dev/docs/b.txt"), "Overwrite", "T")
	if s, _ := e.read("docs/b.txt"); s != "A" {
		t.Fatalf("moved over: %q", s)
	}
	if _, err := e.read("docs/a.txt"); err == nil {
		t.Fatal("source still there after MOVE")
	}
	e.expect(201, "MOVE", "/dav/dev/docs/b.txt", nil, "Destination", dest("/dav/dev/c.txt"))

	// COPY a file and a directory (its contents too).
	e.expect(201, "COPY", "/dav/dev/c.txt", nil, "Destination", dest("/dav/dev/docs/c2.txt"))
	if s, _ := e.read("docs/c2.txt"); s != "A" {
		t.Fatalf("copied file: %q", s)
	}
	e.expect(201, "COPY", "/dav/dev/docs", nil, "Destination", dest("/dav/dev/docs-copy"))
	if s, _ := e.read("docs-copy/c2.txt"); s != "A" {
		t.Fatalf("copied directory: %q", s)
	}
	// MOVE a directory.
	e.expect(201, "MOVE", "/dav/dev/docs-copy", nil, "Destination", dest("/dav/dev/src/docs"))
	if s, _ := e.read("src/docs/c2.txt"); s != "A" {
		t.Fatalf("moved directory: %q", s)
	}

	// DELETE a file and a directory with its contents.
	e.expect(204, "DELETE", "/dav/dev/c.txt", nil)
	e.expect(204, "DELETE", "/dav/dev/src", nil)
	e.expect(404, "DELETE", "/dav/dev/src", nil)
	if names := strings.Join(e.names("."), " "); strings.Contains(names, "src") || strings.Contains(names, "c.txt") {
		t.Fatalf("left after DELETE: %s", names)
	}
	// The root is not removable.
	if res, _ := e.do("DELETE", "/dav/dev/", nil); res.StatusCode < 400 {
		t.Fatalf("DELETE of the root: %d", res.StatusCode)
	}
}

const lockBody = `<?xml version="1.0" encoding="utf-8"?><D:lockinfo xmlns:D="DAV:"><D:lockscope><D:exclusive/></D:lockscope><D:locktype><D:write/></D:locktype><D:owner>test</D:owner></D:lockinfo>`

func TestLock(t *testing.T) {
	e := newEnv(t)
	res, body := e.do("LOCK", "/dav/dev/README.md", strings.NewReader(lockBody), "Timeout", "Second-600")
	if res.StatusCode != 200 {
		t.Fatalf("LOCK: %d %s", res.StatusCode, body)
	}
	token := res.Header.Get("Lock-Token")
	if token == "" {
		t.Fatal("no Lock-Token")
	}
	e.expect(423, "PUT", "/dav/dev/README.md", strings.NewReader("other"))
	e.expect(201, "PUT", "/dav/dev/README.md", strings.NewReader("locked write"), "If", "("+token+")")
	e.expect(204, "UNLOCK", "/dav/dev/README.md", nil, "Lock-Token", token)
	e.expect(201, "PUT", "/dav/dev/README.md", strings.NewReader("after"))
	// LOCK on a new name makes an empty file (Office, Finder).
	res, _ = e.do("LOCK", "/dav/dev/new.docx", strings.NewReader(lockBody))
	if res.StatusCode != 201 {
		t.Fatalf("LOCK of a new name: %d", res.StatusCode)
	}
	if s, err := e.read("new.docx"); err != nil || s != "" {
		t.Fatalf("LOCK of a new name: %q %v", s, err)
	}
}

func TestReadOnly(t *testing.T) {
	e := newEnv(t)
	e.expect(207, "PROPFIND", "/ro/dev/", nil, "Depth", "1")
	if got := e.expect(200, "GET", "/ro/dev/README.md", nil); got != "# hello\n" {
		t.Fatalf("GET: %q", got)
	}
	for _, m := range []string{"PUT", "DELETE", "MKCOL", "MOVE", "COPY", "PROPPATCH", "LOCK", "UNLOCK"} {
		e.expect(403, m, "/ro/dev/README.md", strings.NewReader("x"), "Destination", e.srv.URL+"/ro/dev/x")
	}
	if s, _ := e.read("README.md"); s != "# hello\n" {
		t.Fatalf("read-only changed a file: %q", s)
	}
}

func TestStopped(t *testing.T) {
	e := newEnv(t)
	e.stopped.Store(true)
	body := e.expect(207, "PROPFIND", "/dav/dev/", nil, "Depth", "1")
	if !strings.Contains(body, "%E5%81%9C%E6%AD%A2%E4%B8%AD%E3%81%A7%E3%81%99.txt") || strings.Contains(body, "README") {
		t.Fatalf("stopped listing:\n%s", body)
	}
	if got := e.expect(200, "GET", "/dav/dev/"+StoppedFile, nil); !strings.Contains(got, "停止しています") {
		t.Fatalf("stopped file: %q", got)
	}
	for _, m := range []string{"PUT", "MKCOL", "DELETE", "LOCK"} {
		e.expect(503, m, "/dav/dev/a.txt", strings.NewReader("x"))
	}
	// Known as stopped for a moment: no dial per request.
	n := e.dials.Load()
	e.expect(207, "PROPFIND", "/dav/dev/", nil, "Depth", "1")
	if e.dials.Load() != n {
		t.Error("dialed again right away")
	}
	// Started: Close (Iwato's endOp) forgets it, the files show again.
	e.stopped.Store(false)
	e.pool.Close("dev")
	if body := e.expect(207, "PROPFIND", "/dav/dev/", nil, "Depth", "1"); !strings.Contains(body, "README.md") {
		t.Fatalf("started listing:\n%s", body)
	}
}

func TestProppatch(t *testing.T) {
	e := newEnv(t)
	patch := `<?xml version="1.0" encoding="utf-8"?><D:propertyupdate xmlns:D="DAV:" xmlns:Z="urn:schemas-microsoft-com:"><D:set><D:prop>` +
		`<Z:Win32LastModifiedTime>Wed, 01 Jan 2025 10:00:00 GMT</Z:Win32LastModifiedTime><Z:Win32FileAttributes>00000020</Z:Win32FileAttributes></D:prop></D:set></D:propertyupdate>`
	body := e.expect(207, "PROPPATCH", "/dav/dev/README.md", strings.NewReader(patch))
	if !strings.Contains(body, "200 OK") {
		t.Fatalf("PROPPATCH: %s", body)
	}
	body = e.expect(207, "PROPFIND", "/dav/dev/README.md", nil, "Depth", "0")
	if !strings.Contains(body, "00000020") || !strings.Contains(body, "Wed, 01 Jan 2025 10:00:00 GMT") {
		t.Fatalf("PROPFIND after PROPPATCH: %s", body)
	}
	// Folders take properties too.
	e.expect(207, "PROPPATCH", "/dav/dev/src", strings.NewReader(patch))
}

func TestIdleClose(t *testing.T) {
	e := newEnv(t)
	e.pool.Idle = 50 * time.Millisecond
	e.expect(207, "PROPFIND", "/dav/dev/", nil, "Depth", "1")
	if !e.pool.Open("dev") {
		t.Fatal("no transport after a request")
	}
	deadline := time.Now().Add(3 * time.Second)
	for e.pool.Open("dev") {
		if time.Now().After(deadline) {
			t.Fatal("idle transport not closed")
		}
		time.Sleep(20 * time.Millisecond)
	}
	// And it comes back.
	e.expect(200, "GET", "/dav/dev/README.md", nil)
}

func TestBrokenTransportRedials(t *testing.T) {
	e := newEnv(t)
	e.expect(207, "PROPFIND", "/dav/dev/", nil, "Depth", "1")
	// The relay breaks under the pool (the sandbox restarted).
	e.pool.mu.Lock()
	cn := e.pool.conns["dev"]
	e.pool.mu.Unlock()
	_ = cn.t.Close()
	time.Sleep(50 * time.Millisecond)
	e.expect(200, "GET", "/dav/dev/README.md", nil)
}

// freezable is a transport whose server stops answering while freeze is
// set: what it sends is held until the transport closes.
type freezable struct {
	Transport
	freeze *atomic.Bool
	closed chan struct{}
	once   sync.Once
}

func (f *freezable) OpenSFTP() (io.Reader, io.WriteCloser, error) {
	r, w, err := f.Transport.OpenSFTP()
	return frozenReader{r, f}, w, err
}

func (f *freezable) Close() error {
	f.once.Do(func() { close(f.closed) })
	return f.Transport.Close()
}

type frozenReader struct {
	r io.Reader
	f *freezable
}

func (r frozenReader) Read(b []byte) (int, error) {
	n, err := r.r.Read(b)
	if r.f.freeze.Load() {
		<-r.f.closed
		return 0, io.ErrClosedPipe
	}
	return n, err
}

// A server that stops answering fails the requests waiting on it after
// Stall (instead of hanging them forever), says what was in flight, and
// the next request works on a new transport.
func TestStalledChannel(t *testing.T) {
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	e := newEnv(t)
	e.pool.Stall = 200 * time.Millisecond
	e.expect(200, "GET", "/dav/dev/README.md", nil)
	e.freeze.Store(true)
	start := time.Now()
	done := make(chan int, 1)
	go func() {
		res, _ := e.do("GET", "/dav/dev/src/main.go", nil)
		done <- res.StatusCode
	}()
	select {
	case code := <-done:
		if code < 400 {
			t.Fatalf("stalled GET: %d", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("stalled request still waits")
	}
	if d := time.Since(start); d < e.pool.Stall {
		t.Fatalf("failed after %s, before the stall timeout", d)
	}
	if !strings.Contains(logs.String(), "files: dev: SFTP channel") || !strings.Contains(logs.String(), "src/main.go") {
		t.Fatalf("log: %q", logs.String())
	}
	e.freeze.Store(false)
	if got := e.expect(200, "GET", "/dav/dev/src/main.go", nil); got != "package main\n" {
		t.Fatalf("after the stall: %q", got)
	}
	if n := e.dials.Load(); n != 2 {
		t.Fatalf("%d dials, want 2", n)
	}
}

func TestConcurrentChannels(t *testing.T) {
	e := newEnv(t)
	must(t, e.mem.WriteFile("big.bin", make([]byte, 1<<20)))
	done := make(chan error, 8)
	for range 8 {
		go func() {
			res, err := http.Get(e.srv.URL + "/dav/dev/big.bin")
			if err == nil {
				_, err = io.Copy(io.Discard, res.Body)
				res.Body.Close()
			}
			done <- err
		}()
	}
	for range 8 {
		must(t, <-done)
	}
	e.pool.mu.Lock()
	n := len(e.pool.conns["dev"].channels)
	e.pool.mu.Unlock()
	if n < 1 || n > maxChannels {
		t.Fatalf("%d channels", n)
	}
}

// A real directory behind pkg/sftp's own server: relative paths resolve in
// the root, as on OpenShell, and symbolic links are removed, not followed.
func TestDirectoryServer(t *testing.T) {
	root := t.TempDir()
	must(t, os.MkdirAll(filepath.Join(root, "keep"), 0755))
	must(t, os.WriteFile(filepath.Join(root, "keep", "x.txt"), []byte("x"), 0644))
	must(t, os.Symlink(filepath.Join(root, "keep"), filepath.Join(root, "link")))
	pool := NewPool(func(ctx context.Context, name string) (Transport, error) { return &dirTransport{root: root}, nil })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pool.ServeSandbox(w, r, "dev", Options{Prefix: "/dav/dev"})
	}))
	defer srv.Close()
	defer pool.CloseAll()
	req := func(method, p string, body string) int {
		r, _ := http.NewRequest(method, srv.URL+p, strings.NewReader(body))
		r.Header.Set("Depth", "1")
		res, err := http.DefaultClient.Do(r)
		must(t, err)
		res.Body.Close()
		return res.StatusCode
	}
	if c := req("PUT", "/dav/dev/a.txt", "hello"); c != 201 {
		t.Fatalf("PUT: %d", c)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "a.txt")); string(b) != "hello" {
		t.Fatalf("file on disk: %q", b)
	}
	if c := req("PROPFIND", "/dav/dev/link/", ""); c != 207 {
		t.Fatalf("PROPFIND through a link: %d", c)
	}
	if c := req("DELETE", "/dav/dev/link", ""); c != 204 {
		t.Fatalf("DELETE link: %d", c)
	}
	if _, err := os.Stat(filepath.Join(root, "keep", "x.txt")); err != nil {
		t.Fatalf("deleting a link removed its target's files: %v", err)
	}
}

type dirTransport struct{ root string }

func (d *dirTransport) OpenSFTP() (io.Reader, io.WriteCloser, error) {
	a, b := net.Pipe()
	srv, err := sftp.NewServer(a, sftp.WithServerWorkingDirectory(d.root))
	if err != nil {
		return nil, nil, err
	}
	go func() { _ = srv.Serve(); srv.Close() }()
	return b, b, nil
}
func (d *dirTransport) Close() error { return nil }
