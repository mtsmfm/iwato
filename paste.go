package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/pkg/sftp"

	"iwato/internal/dav"
)

// Pasting an image into the terminal (POST /api/sandboxes/NAME/paste-image,
// the image's bytes as the body): Iwato writes it into the sandbox through
// SFTP (the same pool as the files' WebDAV), under the files' root's
// pasteDir (the home's, or the workspace's without the daemon's SFTP
// server), and answers its absolute path, which the page pastes into the
// terminal as text. Claude Code takes an image file's path in its prompt as
// that image (as when a file is dropped on a local terminal).

const (
	// pasteDir is under the SFTP root (the home, FilesRoot).
	pasteDir      = ".cache/iwato/paste"
	maxPasteBytes = 20 << 20
	// The pasted images kept: the newest pasteKeep, none older than
	// pasteMaxAge (best effort, on each paste).
	pasteKeep   = 50
	pasteMaxAge = 7 * 24 * time.Hour
)

// pasteTypes are the image types taken, with their file extensions.
var pasteTypes = map[string]string{"image/png": ".png", "image/jpeg": ".jpg", "image/gif": ".gif", "image/webp": ".webp"}

// pasteName is the name of a pasted image (pasteImage).
var pasteName = regexp.MustCompile(`^(\d{8}T\d{6}Z)-[0-9a-f]{8}\.(png|jpg|gif|webp)$`)

const pasteTimeLayout = "20060102T150405Z"

var errPasteType = errors.New("PNG・JPEG・GIF・WebP の画像だけ貼り付けられます")

// pasteTypeTaken reports whether a request's Content-Type is one of
// pasteTypes.
func pasteTypeTaken(contentType string) bool {
	t, _, _ := strings.Cut(contentType, ";")
	_, ok := pasteTypes[strings.ToLower(strings.TrimSpace(t))]
	return ok
}

// pasteImageType is the extension for the image's bytes, which must be one
// of pasteTypes too (http.DetectContentType knows each): the bytes, not the
// declared type, decide it.
func pasteImageType(data []byte) (string, error) {
	ext, ok := pasteTypes[http.DetectContentType(data)]
	if !ok {
		return "", errPasteType
	}
	return ext, nil
}

// pasteImage writes the image into the sandbox and returns its absolute
// path in the sandbox.
func pasteImage(ctx context.Context, pool *dav.Pool, rt filesRootRuntime, w Sandbox, ext string, data []byte, now time.Time) (string, error) {
	name := now.UTC().Format(pasteTimeLayout) + "-" + randomHex(4) + ext
	rel := pasteDir + "/" + name
	err := pool.Do(ctx, w.Name, "paste "+rel, func(c *sftp.Client) error {
		if err := c.MkdirAll(pasteDir); err != nil {
			return fmt.Errorf("mkdir %s: %w", pasteDir, err)
		}
		_ = c.Chmod(pasteDir, 0700)
		f, err := c.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
		if err != nil {
			return err
		}
		_ = f.Chmod(0600)
		_, err = f.Write(data)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			_ = c.Remove(rel)
			return err
		}
		prunePasted(c, name, now)
		return nil
	})
	if err != nil {
		return "", err
	}
	root, err := rt.FilesRoot(ctx, w)
	if err != nil {
		return "", err
	}
	return path.Join(root.Path, rel), nil
}

// prunePasted removes the pasted images beyond the newest pasteKeep and
// those older than pasteMaxAge, never the one just written (keep).
func prunePasted(c *sftp.Client, keep string, now time.Time) {
	entries, err := c.ReadDir(pasteDir)
	if err != nil {
		return
	}
	var names []string
	for _, e := range entries {
		if e.Mode().IsRegular() && pasteName.MatchString(e.Name()) {
			names = append(names, e.Name())
		}
	}
	// Newest first: the names start with the time.
	slices.Sort(names)
	slices.Reverse(names)
	for i, name := range names {
		if name == keep {
			continue
		}
		t, err := time.Parse(pasteTimeLayout, pasteName.FindStringSubmatch(name)[1])
		if i < pasteKeep && err == nil && now.Sub(t) < pasteMaxAge {
			continue
		}
		if err := c.Remove(pasteDir + "/" + name); err != nil {
			log.Printf("paste: remove %s: %v", name, english(err))
		}
	}
}

// handlePasteImage serves POST /api/sandboxes/NAME/paste-image: the body is
// the image, its Content-Type one of pasteTypes. The answer is {"path"}.
func (s *Server) handlePasteImage(w http.ResponseWriter, r *http.Request, parts []string) {
	if r.Method != http.MethodPost {
		fail(w, 405, "method not allowed")
		return
	}
	sb, ok := s.sandboxFor(w, parts, 2)
	if !ok || !s.require(w, r, true) {
		return
	}
	rt, hasRT := s.rt.(filesRootRuntime)
	if s.files == nil || !hasRT {
		failCode(w, 501, "paste_unavailable", nil, "画像の貼り付けは使えません")
		return
	}
	if !pasteTypeTaken(r.Header.Get("Content-Type")) {
		failCode(w, 415, "paste_type", nil, errPasteType.Error())
		return
	}
	// An upload from a phone may take longer than the server's ReadTimeout.
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(5 * time.Minute))
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxPasteBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			failCode(w, 413, "paste_too_large", map[string]any{"max": maxPasteBytes >> 20}, fmt.Sprintf("画像が大きすぎます (%d MB まで)", maxPasteBytes>>20))
			return
		}
		failCode(w, 400, "paste_unreadable", nil, "画像を受け取れませんでした")
		return
	}
	ext, err := pasteImageType(data)
	if err != nil {
		failCode(w, 415, "paste_type", nil, err.Error())
		return
	}
	ctx, cancel := timeout(r.Context(), time.Minute)
	defer cancel()
	p, err := pasteImage(ctx, s.files, rt, sb, ext, data, time.Now())
	if err != nil {
		if errors.Is(err, dav.ErrStopped) {
			failCode(w, 409, "sandbox_stopped", nil, "サンドボックスは停止しています")
			return
		}
		log.Printf("paste %s: %v", sb.Name, english(err))
		failCode(w, 502, "paste_write_failed", map[string]any{"detail": safeCLIError(err)}, "サンドボックスに画像を書き込めませんでした: "+safeCLIError(err))
		return
	}
	jsonResponse(w, 200, map[string]string{"path": p})
}
