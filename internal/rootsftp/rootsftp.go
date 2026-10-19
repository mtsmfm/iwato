// Package rootsftp is an SFTP server confined to one directory (the
// sandbox user's home): `iwato-entrypoint sftp` serves it on its stdin and
// stdout, in a TTY-less exec that Iwato starts as the sandbox user, for
// the files tab (WebDAV), the editor and image paste.
//
// Every path is resolved through an os.Root, so that neither `..` nor a
// symbolic link (absolute or relative, at any component) reaches outside
// the directory: such a request fails. The client's "/" is the directory.
// A link is listed and read (Readlink) as it is; only following it out of
// the directory is refused.
//
// Semantics follow OpenShell's SFTP server where Iwato depends on them:
// Rename refuses an existing destination (SFTP v3), posix-rename@openssh.com
// replaces it.
package rootsftp

import (
	"errors"
	"io"
	"os"
	"path"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/pkg/sftp"
)

// Serve serves SFTP for dir on r and w until the client closes r.
func Serve(dir string, r io.Reader, w io.WriteCloser) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	h := &handler{root: root}
	s := sftp.NewRequestServer(pipe{r, w}, sftp.Handlers{FileGet: h, FilePut: h, FileCmd: h, FileList: h})
	defer s.Close()
	if err := s.Serve(); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

type pipe struct {
	io.Reader
	io.WriteCloser
}

type handler struct{ root *os.Root }

// rel is a request's path (absolute, already cleaned by pkg/sftp) relative
// to the root.
func rel(p string) string {
	p = strings.TrimPrefix(path.Clean("/"+p), "/")
	if p == "" {
		return "."
	}
	return p
}

func (h *handler) Fileread(r *sftp.Request) (io.ReaderAt, error) {
	return h.root.OpenFile(rel(r.Filepath), os.O_RDONLY, 0)
}

func (h *handler) Filewrite(r *sftp.Request) (io.WriterAt, error) { return h.open(r) }

func (h *handler) OpenFile(r *sftp.Request) (sftp.WriterAtReaderAt, error) { return h.open(r) }

func (h *handler) open(r *sftp.Request) (sftp.WriterAtReaderAt, error) {
	pf := r.Pflags()
	var flag int
	switch {
	case pf.Read && pf.Write:
		flag = os.O_RDWR
	case pf.Write:
		flag = os.O_WRONLY
	default:
		flag = os.O_RDONLY
	}
	if pf.Creat {
		flag |= os.O_CREATE
	}
	if pf.Trunc {
		flag |= os.O_TRUNC
	}
	if pf.Excl {
		flag |= os.O_EXCL
	}
	perm := os.FileMode(0o644)
	if r.AttrFlags().Permissions {
		perm = r.Attributes().FileMode().Perm()
	}
	// Not O_APPEND: WriteAt refuses such a file; appendFile writes at the
	// end instead.
	f, err := h.root.OpenFile(rel(r.Filepath), flag, perm)
	if err != nil {
		return nil, err
	}
	if pf.Append {
		return &appendFile{File: f}, nil
	}
	return f, nil
}

// appendFile writes at the end whatever the offset (O_APPEND's meaning).
type appendFile struct {
	*os.File
	mu sync.Mutex
}

func (a *appendFile) WriteAt(b []byte, _ int64) (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, err := a.Seek(0, io.SeekEnd); err != nil {
		return 0, err
	}
	return a.Write(b)
}

func (h *handler) Filecmd(r *sftp.Request) error {
	p := rel(r.Filepath)
	switch r.Method {
	case "Setstat":
		return h.setstat(r, p)
	case "Rename":
		// SFTP v3 (and OpenShell's server): an existing destination is an
		// error.
		if _, err := h.root.Lstat(rel(r.Target)); err == nil {
			return os.ErrExist
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return h.root.Rename(p, rel(r.Target))
	case "Rmdir":
		fi, err := h.root.Lstat(p)
		if err != nil {
			return err
		}
		if !fi.IsDir() {
			return syscall.ENOTDIR
		}
		return h.root.Remove(p)
	case "Remove":
		// unlink: a directory is Rmdir's (the client tries that next).
		fi, err := h.root.Lstat(p)
		if err != nil {
			return err
		}
		if fi.IsDir() {
			return syscall.EISDIR
		}
		return h.root.Remove(p)
	case "Mkdir":
		perm := os.FileMode(0o755)
		if r.AttrFlags().Permissions {
			perm = r.Attributes().FileMode().Perm()
		}
		return h.root.Mkdir(p, perm)
	case "Link":
		return h.root.Link(p, rel(r.Target))
	case "Symlink":
		// r.Filepath is the link's target as the client wrote it, r.Target
		// the link.
		return h.root.Symlink(r.Filepath, rel(r.Target))
	}
	return sftp.ErrSSHFxOpUnsupported
}

func (h *handler) PosixRename(r *sftp.Request) error {
	return h.root.Rename(rel(r.Filepath), rel(r.Target))
}

func (h *handler) setstat(r *sftp.Request, p string) error {
	flags, attrs := r.AttrFlags(), r.Attributes()
	if flags.Size {
		f, err := h.root.OpenFile(p, os.O_WRONLY, 0)
		if err != nil {
			return err
		}
		err = f.Truncate(int64(attrs.Size))
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
	}
	if flags.Permissions {
		if err := h.root.Chmod(p, attrs.FileMode().Perm()); err != nil {
			return err
		}
	}
	if flags.Acmodtime {
		if err := h.root.Chtimes(p, attrs.AccessTime(), attrs.ModTime()); err != nil {
			return err
		}
	}
	if flags.UidGid {
		if err := h.root.Chown(p, int(attrs.UID), int(attrs.GID)); err != nil {
			return err
		}
	}
	return nil
}

func (h *handler) Filelist(r *sftp.Request) (sftp.ListerAt, error) {
	p := rel(r.Filepath)
	switch r.Method {
	case "List":
		d, err := h.root.Open(p)
		if err != nil {
			return nil, err
		}
		defer d.Close()
		list, err := d.Readdir(-1)
		if err != nil {
			return nil, err
		}
		return lister(list), nil
	case "Stat":
		fi, err := h.root.Stat(p)
		if err != nil {
			return nil, err
		}
		return lister{fi}, nil
	case "Readlink":
		target, err := h.Readlink(r.Filepath)
		if err != nil {
			return nil, err
		}
		return lister{linkInfo(target)}, nil
	}
	return nil, sftp.ErrSSHFxOpUnsupported
}

func (h *handler) Lstat(r *sftp.Request) (sftp.ListerAt, error) {
	fi, err := h.root.Lstat(rel(r.Filepath))
	if err != nil {
		return nil, err
	}
	return lister{fi}, nil
}

// Readlink answers a link's target as it is written.
func (h *handler) Readlink(p string) (string, error) { return h.root.Readlink(rel(p)) }

type lister []os.FileInfo

func (l lister) ListAt(out []os.FileInfo, off int64) (int, error) {
	if off >= int64(len(l)) {
		return 0, io.EOF
	}
	n := copy(out, l[off:])
	if n < len(out) {
		return n, io.EOF
	}
	return n, nil
}

// linkInfo carries a Readlink's answer (only its name is sent).
type linkInfo string

func (l linkInfo) Name() string     { return string(l) }
func (linkInfo) Size() int64        { return 0 }
func (linkInfo) Mode() os.FileMode  { return os.ModeSymlink }
func (linkInfo) ModTime() time.Time { return time.Time{} }
func (linkInfo) IsDir() bool        { return false }
func (linkInfo) Sys() any           { return nil }
