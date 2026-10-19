package dav

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"strings"

	"golang.org/x/net/webdav"
)

// Options says how one request reaches a sandbox's files.
type Options struct {
	// Prefix is the URL path of the sandbox's root ("/dav/dev").
	Prefix string
	// ReadOnly refuses every method that changes something (LOCK
	// included: Finder then mounts the folder read-only).
	ReadOnly bool
}

// StoppedFile is the only file a stopped sandbox's folder shows.
const StoppedFile = "停止中です.txt"

func isWrite(method string) bool {
	switch method {
	case "PUT", "DELETE", "MKCOL", "MOVE", "COPY", "PROPPATCH", "LOCK", "UNLOCK":
		return true
	}
	return false
}

func logError(r *http.Request, err error) {
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Printf("files: %s %s: %v", r.Method, r.URL.Path, err)
	}
}

// prepare checks what webdav would take as is: a PROPFIND of the whole tree
// (Depth: infinity) is refused, a missing Depth means 1 (clients send it;
// the default of infinity would walk everything), and a read-only
// credential changes nothing.
func prepare(w http.ResponseWriter, r *http.Request, readOnly bool) bool {
	if readOnly && isWrite(r.Method) {
		http.Error(w, "読み取り専用です", http.StatusForbidden)
		return false
	}
	if r.Method == "PROPFIND" {
		switch strings.ToLower(strings.TrimSpace(r.Header.Get("Depth"))) {
		case "":
			r.Header.Set("Depth", "1")
		case "infinity":
			w.Header().Set("Content-Type", "application/xml; charset=utf-8")
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `<?xml version="1.0" encoding="utf-8"?><D:error xmlns:D="DAV:"><D:propfind-finite-depth/></D:error>`)
			return false
		}
	}
	// webdav copies the body with io.Copy; reading it through Read alone
	// lets the destination see an early end (putFile.ReadFrom).
	if r.Body != nil {
		r.Body = struct{ io.ReadCloser }{r.Body}
	}
	return true
}

// ServeSandbox serves one request on the sandbox's files. A stopped sandbox
// shows a folder with StoppedFile only, and refuses changes with 503.
func (p *Pool) ServeSandbox(w http.ResponseWriter, r *http.Request, name string, o Options) {
	if !prepare(w, r, o.ReadOnly) {
		return
	}
	if err := p.probe(r.Context(), name); err != nil {
		if errors.Is(err, ErrStopped) {
			if isWrite(r.Method) {
				w.Header().Set("Retry-After", "10")
				http.Error(w, "サンドボックスは停止しています", http.StatusServiceUnavailable)
				return
			}
			serveMem(w, r, o.Prefix, stoppedFS(r.Context(), name))
			return
		}
		log.Printf("files: %s: %v", name, err)
		http.Error(w, "サンドボックスに接続できません", http.StatusBadGateway)
		return
	}
	h := &webdav.Handler{Prefix: o.Prefix, FileSystem: newFS(p, name), LockSystem: p.lockSystem(name), Logger: logError}
	h.ServeHTTP(w, r)
}

// probe opens the sandbox's transport (when it is not open) and a channel.
func (p *Pool) probe(ctx context.Context, name string) error {
	l, err := p.acquire(ctx, name, "open")
	if err != nil {
		return err
	}
	l.release()
	return nil
}

func (p *Pool) lockSystem(name string) webdav.LockSystem {
	p.mu.Lock()
	defer p.mu.Unlock()
	ls := p.locks[name]
	if ls == nil {
		ls = webdav.NewMemLS()
		p.locks[name] = ls
	}
	return ls
}

// Forget drops what Iwato holds for a deleted sandbox: its transport, locks
// and properties.
func (p *Pool) Forget(name string) {
	p.Close(name)
	p.mu.Lock()
	delete(p.locks, name)
	p.mu.Unlock()
	p.props.forget(name)
}

// ServeList serves the folder that lists the sandboxes a credential sees
// (read-only).
func ServeList(w http.ResponseWriter, r *http.Request, prefix string, names []string) {
	if isWrite(r.Method) {
		http.Error(w, "サンドボックスの一覧は変更できません", http.StatusForbidden)
		return
	}
	if !prepare(w, r, true) {
		return
	}
	fs := webdav.NewMemFS()
	for _, name := range names {
		_ = fs.Mkdir(r.Context(), "/"+name, 0755)
	}
	serveMem(w, r, prefix, fs)
}

func stoppedFS(ctx context.Context, name string) webdav.FileSystem {
	fs := webdav.NewMemFS()
	f, err := fs.OpenFile(ctx, "/"+StoppedFile, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0644)
	if err == nil {
		_, _ = io.WriteString(f, "サンドボックス "+name+" は停止しています。\r\nIwato の画面で起動すると、ここに /sandbox のファイルが表示されます。\r\n")
		f.Close()
	}
	return fs
}

func serveMem(w http.ResponseWriter, r *http.Request, prefix string, fs webdav.FileSystem) {
	h := &webdav.Handler{Prefix: prefix, FileSystem: fs, LockSystem: webdav.NewMemLS(), Logger: logError}
	h.ServeHTTP(w, r)
}
