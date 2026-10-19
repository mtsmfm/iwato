package dav

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path"
	"regexp"
	"strings"
	"sync"

	"github.com/pkg/sftp"
	"golang.org/x/net/webdav"
)

// sftpFS is a webdav.FileSystem on one sandbox's SFTP root (the home, or
// the workspace with OpenShell's SFTP server).
// One is made per request: it remembers the file infos the request has
// already read, so a listing (PROPFIND, Depth 1) costs one stat and one
// readdir instead of a few round trips per entry.
type sftpFS struct {
	pool *Pool
	name string

	mu    sync.Mutex
	infos map[string]os.FileInfo
}

func newFS(p *Pool, name string) *sftpFS {
	return &sftpFS{pool: p, name: name, infos: map[string]os.FileInfo{}}
}

// rel turns webdav's slash-rooted name into a path relative to the SFTP
// root. OpenShell reads an absolute path that starts with its root
// ("/sandbox/…") as that root's, so relative paths are the unambiguous form.
func rel(name string) string {
	name = path.Clean("/" + name)
	if name == "/" {
		return "."
	}
	return name[1:]
}

// tempPattern matches the names of the files Iwato writes while a PUT is in
// progress; listings leave them out.
var tempPattern = regexp.MustCompile(`^\..+\.iwato-(?:old-)?[0-9a-f]{12}$`)

func tempName(p string, old bool) string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	kind := ""
	if old {
		kind = "old-"
	}
	return path.Join(path.Dir(p), "."+path.Base(p)+".iwato-"+kind+hex.EncodeToString(b))
}

func (f *sftpFS) cached(p string) (os.FileInfo, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fi, ok := f.infos[p]
	return fi, ok
}

func (f *sftpFS) remember(p string, fi os.FileInfo) {
	f.mu.Lock()
	f.infos[p] = fi
	f.mu.Unlock()
}

// changed forgets what the request read: it changed the tree.
func (f *sftpFS) changed() {
	f.mu.Lock()
	f.infos = map[string]os.FileInfo{}
	f.mu.Unlock()
}

func (f *sftpFS) stat(ctx context.Context, p string) (os.FileInfo, error) {
	if fi, ok := f.cached(p); ok {
		return fi, nil
	}
	l, err := f.pool.acquire(ctx, f.name, "stat "+p)
	if err != nil {
		return nil, err
	}
	defer l.release()
	fi, err := l.c.Stat(p)
	if err != nil {
		return nil, err
	}
	info := wrapInfo(fi, p)
	f.remember(p, info)
	return info, nil
}

func (f *sftpFS) Stat(ctx context.Context, name string) (os.FileInfo, error) {
	return f.stat(ctx, rel(name))
}

func (f *sftpFS) Mkdir(ctx context.Context, name string, perm os.FileMode) error {
	p := rel(name)
	if p == "." {
		return os.ErrExist
	}
	l, err := f.pool.acquire(ctx, f.name, "mkdir "+p)
	if err != nil {
		return err
	}
	defer l.release()
	f.changed()
	if err := l.c.Mkdir(p); err != nil {
		// SFTP v3 has no "exists" status; say so when it does.
		if _, serr := l.c.Lstat(p); serr == nil {
			return os.ErrExist
		}
		return err
	}
	return nil
}

func (f *sftpFS) RemoveAll(ctx context.Context, name string) error {
	p := rel(name)
	if p == "." {
		return os.ErrPermission
	}
	l, err := f.pool.acquire(ctx, f.name, "delete "+p)
	if err != nil {
		return err
	}
	defer l.release()
	f.changed()
	return removeAll(l.c, p)
}

// removeAll removes p and what is under it. Unlike sftp.Client.RemoveAll it
// does not follow a symbolic link to a directory: it removes the link.
func removeAll(c *sftp.Client, p string) error {
	fi, err := c.Lstat(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if !fi.IsDir() {
		return c.Remove(p)
	}
	entries, err := c.ReadDir(p)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := removeAll(c, path.Join(p, e.Name())); err != nil {
			return err
		}
	}
	return c.RemoveDirectory(p)
}

func (f *sftpFS) Rename(ctx context.Context, oldName, newName string) error {
	from, to := rel(oldName), rel(newName)
	if from == "." || to == "." {
		return os.ErrPermission
	}
	l, err := f.pool.acquire(ctx, f.name, "rename "+from+" -> "+to)
	if err != nil {
		return err
	}
	defer l.release()
	f.changed()
	// webdav removes an existing destination first (Overwrite: T), so a
	// rename that does not replace (OpenShell's) is enough.
	return l.c.Rename(from, to)
}

func (f *sftpFS) OpenFile(ctx context.Context, name string, flag int, perm os.FileMode) (webdav.File, error) {
	p := rel(name)
	if flag&(os.O_CREATE|os.O_TRUNC) != 0 && flag&(os.O_WRONLY|os.O_RDWR) != 0 {
		return f.create(ctx, p, flag)
	}
	fi, err := f.stat(ctx, p)
	if err != nil {
		return nil, err
	}
	if fi.IsDir() {
		return &dirFile{fs: f, p: p, info: fi}, nil
	}
	return &readFile{fs: f, ctx: context.WithoutCancel(ctx), p: p, info: fi}, nil
}

// create starts a PUT (or a COPY's destination): the content goes to a
// temporary name next to the file and replaces it only once complete, so
// an interrupted upload leaves the old file as it was.
func (f *sftpFS) create(ctx context.Context, p string, flag int) (webdav.File, error) {
	if p == "." {
		return nil, os.ErrPermission
	}
	old, err := f.stat(ctx, p)
	switch {
	case err == nil && old.IsDir():
		return nil, &fs.PathError{Op: "open", Path: p, Err: errors.New("is a directory")}
	case err == nil && flag&os.O_EXCL != 0:
		return nil, os.ErrExist
	case err != nil && !errors.Is(err, os.ErrNotExist):
		return nil, err
	case err != nil:
		old = nil
	}
	l, err := f.pool.acquire(ctx, f.name, "put "+p)
	if err != nil {
		return nil, err
	}
	f.changed()
	tmp := tempName(p, false)
	file, err := l.c.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
	if err != nil {
		l.release()
		return nil, err
	}
	return &putFile{fs: f, l: l, p: p, tmp: tmp, file: file, old: old}, nil
}

// putFile is a file being written under a temporary name.
type putFile struct {
	fs     *sftpFS
	l      *lease
	p, tmp string
	file   *sftp.File
	old    os.FileInfo
	err    error
	done   bool
}

func (w *putFile) Write(b []byte) (int, error) {
	n, err := w.file.Write(b)
	if err != nil && w.err == nil {
		w.err = err
	}
	return n, err
}

// ReadFrom takes the request body (webdav copies it with io.Copy): a body
// that ends early (the client went away) is seen here, and the upload is
// then dropped instead of replacing the file.
func (w *putFile) ReadFrom(r io.Reader) (int64, error) {
	n, err := w.file.ReadFrom(r)
	if err != nil && w.err == nil {
		w.err = err
	}
	return n, err
}

func (w *putFile) Read([]byte) (int, error)                         { return 0, os.ErrPermission }
func (w *putFile) Seek(int64, int) (int64, error)                   { return 0, os.ErrPermission }
func (w *putFile) Readdir(int) ([]fs.FileInfo, error)               { return nil, os.ErrInvalid }
func (w *putFile) DeadProps() (map[xml.Name]webdav.Property, error) { return nil, nil }
func (w *putFile) Patch(patches []webdav.Proppatch) ([]webdav.Propstat, error) {
	// A COPY carries the source's properties over.
	return w.fs.pool.props.patch(w.fs.name, w.p, patches), nil
}

func (w *putFile) Stat() (fs.FileInfo, error) {
	fi, err := w.file.Stat()
	if err != nil {
		return nil, err
	}
	return wrapInfo(renamed{fi, path.Base(w.p)}, w.p), nil
}

func (w *putFile) Close() error {
	if w.done {
		return nil
	}
	w.done = true
	defer w.l.release()
	c := w.l.c
	cerr := w.file.Close()
	if w.err == nil {
		w.err = cerr
	}
	if w.err != nil {
		_ = c.Remove(w.tmp)
		return w.err
	}
	return replace(c, w.tmp, w.p, w.old)
}

// replace puts the complete temporary file tmp in p's place, with the mode
// of the file it replaces (old; nil when p is new). The temporary file is
// gone afterwards, either way.
func replace(c *sftp.Client, tmp, p string, old os.FileInfo) error {
	if old != nil {
		_ = c.Chmod(tmp, old.Mode().Perm())
	}
	if err := c.Rename(tmp, p); err == nil {
		return nil
	} else if old == nil {
		if _, serr := c.Lstat(p); serr != nil {
			_ = c.Remove(tmp)
			return err
		}
	}
	// OpenShell's rename does not replace a file: move the old one aside,
	// put the new one in its place, then remove the old one (or put it
	// back when that fails).
	aside := tempName(p, true)
	if err := c.Rename(p, aside); err != nil {
		_ = c.Remove(tmp)
		return err
	}
	if err := c.Rename(tmp, p); err != nil {
		_ = c.Rename(aside, p)
		_ = c.Remove(tmp)
		return err
	}
	_ = c.Remove(aside)
	return nil
}

// WriteFile replaces the file p (relative to the SFTP root) with data the
// way a PUT does: the data goes to a temporary name next to it, which takes
// p's place (and its mode) only once complete. old is p's current info
// (nil when p is new).
func WriteFile(c *sftp.Client, p string, data []byte, old os.FileInfo) error {
	tmp := tempName(p, false)
	f, err := c.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = c.Remove(tmp)
		return err
	}
	return replace(c, tmp, p, old)
}

// renamed is a file info under another name (the temporary file's).
type renamed struct {
	fs.FileInfo
	name string
}

func (r renamed) Name() string { return r.name }

// readFile opens the SFTP file only when read: a listing opens every entry
// (webdav reads their properties through OpenFile) without reading it.
// Reads go through a large buffer, which pkg/sftp fills with several
// requests in flight instead of one round trip per 32 KiB.
type readFile struct {
	fs   *sftpFS
	ctx  context.Context
	p    string
	info os.FileInfo
	l    *lease
	file *sftp.File
	br   *bufio.Reader
	off  int64
}

const readBuffer = 1 << 20

func (r *readFile) open() error {
	if r.file != nil {
		return nil
	}
	l, err := r.fs.pool.acquire(r.ctx, r.fs.name, "get "+r.p)
	if err != nil {
		return err
	}
	file, err := l.c.Open(r.p)
	if err != nil {
		l.release()
		return err
	}
	if r.off != 0 {
		if _, err := file.Seek(r.off, io.SeekStart); err != nil {
			file.Close()
			l.release()
			return err
		}
	}
	r.l, r.file = l, file
	r.br = bufio.NewReaderSize(file, readBuffer)
	return nil
}

func (r *readFile) Read(b []byte) (int, error) {
	if err := r.open(); err != nil {
		return 0, err
	}
	n, err := r.br.Read(b)
	r.off += int64(n)
	return n, err
}

// Seek moves without opening the file (http.ServeContent seeks to the end
// for the size first).
func (r *readFile) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
	case io.SeekCurrent:
		offset += r.off
	case io.SeekEnd:
		offset += r.info.Size()
	default:
		return 0, os.ErrInvalid
	}
	if offset < 0 {
		return 0, os.ErrInvalid
	}
	if r.file != nil && offset != r.off {
		if _, err := r.file.Seek(offset, io.SeekStart); err != nil {
			return 0, err
		}
		r.br.Reset(r.file)
	}
	r.off = offset
	return offset, nil
}

func (r *readFile) Write([]byte) (int, error)          { return 0, os.ErrPermission }
func (r *readFile) Readdir(int) ([]fs.FileInfo, error) { return nil, os.ErrInvalid }
func (r *readFile) Stat() (fs.FileInfo, error)         { return r.info, nil }
func (r *readFile) DeadProps() (map[xml.Name]webdav.Property, error) {
	return r.fs.pool.props.get(r.fs.name, r.p), nil
}
func (r *readFile) Patch(patches []webdav.Proppatch) ([]webdav.Propstat, error) {
	return r.fs.patch(r.ctx, r.p, patches)
}

func (r *readFile) Close() error {
	if r.file == nil {
		return nil
	}
	err := r.file.Close()
	r.file = nil
	r.l.release()
	return err
}

// dirFile is a directory: Readdir lists it.
type dirFile struct {
	fs      *sftpFS
	p       string
	info    os.FileInfo
	entries []fs.FileInfo
	read    bool
}

func (d *dirFile) Readdir(count int) ([]fs.FileInfo, error) {
	if !d.read {
		d.read = true
		l, err := d.fs.pool.acquire(context.Background(), d.fs.name, "list "+d.p)
		if err != nil {
			return nil, err
		}
		list, err := l.c.ReadDir(d.p)
		if err != nil {
			l.release()
			return nil, err
		}
		for _, fi := range list {
			if tempPattern.MatchString(fi.Name()) {
				continue
			}
			p := path.Join(d.p, fi.Name())
			// ReadDir does not follow links: show what a link points to.
			if fi.Mode()&fs.ModeSymlink != 0 {
				if target, err := l.c.Stat(p); err == nil {
					fi = renamed{target, fi.Name()}
				}
			}
			info := wrapInfo(fi, p)
			d.fs.remember(p, info)
			d.entries = append(d.entries, info)
		}
		l.release()
	}
	if count <= 0 {
		rest := d.entries
		d.entries = nil
		return rest, nil
	}
	if len(d.entries) == 0 {
		return nil, io.EOF
	}
	n := min(count, len(d.entries))
	part := d.entries[:n]
	d.entries = d.entries[n:]
	return part, nil
}

func (d *dirFile) Read([]byte) (int, error)       { return 0, os.ErrInvalid }
func (d *dirFile) Write([]byte) (int, error)      { return 0, os.ErrInvalid }
func (d *dirFile) Seek(int64, int) (int64, error) { return 0, nil }
func (d *dirFile) Stat() (fs.FileInfo, error)     { return d.info, nil }
func (d *dirFile) Close() error                   { return nil }
func (d *dirFile) DeadProps() (map[xml.Name]webdav.Property, error) {
	return d.fs.pool.props.get(d.fs.name, d.p), nil
}
func (d *dirFile) Patch(patches []webdav.Proppatch) ([]webdav.Propstat, error) {
	return d.fs.patch(context.Background(), d.p, patches)
}

// info is a file info that gives its content type from its name, so a
// listing does not open every file to sniff it.
type info struct {
	fs.FileInfo
	name string
}

func wrapInfo(fi fs.FileInfo, p string) fs.FileInfo {
	name := fi.Name()
	if p == "." {
		name = ""
	}
	return info{fi, name}
}

func (i info) Name() string { return i.name }

func (i info) ContentType(context.Context) (string, error) {
	return contentType(i.name), nil
}

func contentType(name string) string {
	if t := mime.TypeByExtension(strings.ToLower(path.Ext(name))); t != "" {
		return t
	}
	return "application/octet-stream"
}

// win32Modified is the property Windows sets on a file it copied (its
// modification time); it goes to the file's mtime as well.
var win32Modified = xml.Name{Space: "urn:schemas-microsoft-com:", Local: "Win32LastModifiedTime"}

// patch keeps dead properties in memory (Windows and macOS set some and
// count a refusal as a failed copy). Win32LastModifiedTime also sets the
// file's modification time.
func (f *sftpFS) patch(ctx context.Context, p string, patches []webdav.Proppatch) ([]webdav.Propstat, error) {
	for _, pp := range patches {
		if pp.Remove {
			continue
		}
		for _, prop := range pp.Props {
			if prop.XMLName != win32Modified {
				continue
			}
			t, err := http.ParseTime(strings.TrimSpace(string(prop.InnerXML)))
			if err != nil {
				continue
			}
			if l, err := f.pool.acquire(ctx, f.name, "set the time of "+p); err == nil {
				if err := l.c.Chtimes(p, t, t); err == nil {
					f.changed()
				}
				l.release()
			}
		}
	}
	return f.pool.props.patch(f.name, p, patches), nil
}

// propStore holds dead properties in memory (lost when Iwato restarts),
// bounded in size.
type propStore struct {
	mu    sync.Mutex
	props map[string]map[xml.Name]webdav.Property
}

const maxPropEntries = 10000

func newPropStore() *propStore {
	return &propStore{props: map[string]map[xml.Name]webdav.Property{}}
}

func (s *propStore) get(sandbox, p string) map[xml.Name]webdav.Property {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.props[sandbox+"\x00"+p]
	if m == nil {
		return nil
	}
	out := make(map[xml.Name]webdav.Property, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func (s *propStore) patch(sandbox, p string, patches []webdav.Proppatch) []webdav.Propstat {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := sandbox + "\x00" + p
	m := s.props[key]
	if m == nil {
		if len(s.props) >= maxPropEntries {
			for k := range s.props {
				delete(s.props, k)
				if len(s.props) < maxPropEntries*9/10 {
					break
				}
			}
		}
		m = map[xml.Name]webdav.Property{}
		s.props[key] = m
	}
	st := webdav.Propstat{Status: http.StatusOK}
	for _, pp := range patches {
		for _, prop := range pp.Props {
			st.Props = append(st.Props, webdav.Property{XMLName: prop.XMLName})
			if pp.Remove {
				delete(m, prop.XMLName)
			} else if len(m) < 64 && len(prop.InnerXML) <= 4096 {
				m[prop.XMLName] = prop
			}
		}
	}
	if len(m) == 0 {
		delete(s.props, key)
	}
	return []webdav.Propstat{st}
}

// forget drops a sandbox's properties.
func (s *propStore) forget(sandbox string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k := range s.props {
		if strings.HasPrefix(k, sandbox+"\x00") {
			delete(s.props, k)
		}
	}
}
