package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"maps"
	"net/http"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/pkg/sftp"

	"iwato/internal/dav"
)

// The editor tab (エディタ, web/editor.js): the page opens a text file of the
// sandbox's files (the SFTP root: the home, FilesRoot), edits it and saves
// it, through the same SFTP pool as the files' WebDAV. The page's session
// has full access, as with the terminal. Routes:
//
//	GET  /api/sandboxes/NAME/editor/root                        where the files are (FilesRoot) and whether ~/work is there
//	GET  /api/sandboxes/NAME/editor/files?dir=DIR[&refresh=1]  the files under DIR (quick open)
//	GET  /api/sandboxes/NAME/editor/dir?path=DIR                 one directory's entries (the tree)
//	GET  /api/sandboxes/NAME/editor/file?path=PATH              a file's text and version
//	GET  /api/sandboxes/NAME/editor/raw?path=PATH               an image's bytes (the preview)
//	GET  /api/sandboxes/NAME/editor/media?path=PATH             audio or video, streamed (Range)
//	GET  /api/sandboxes/NAME/editor/resolve?path=P&term=T       a path printed in terminal T, as a file under the root
//	POST /api/sandboxes/NAME/editor/file                         save {path, content, base, force}
//
// Paths are relative to the SFTP root (resolve's is as the terminal printed
// it). A version is the content's SHA-256: a save names the version it was
// edited from (base) and is refused (409, "changed") when the file is no
// longer that, unless forced.
//
// The raw route serves only images a browser shows (editorImageType, by
// the content, not the name), under a CSP that sandboxes it and nosniff:
// it never serves the sandbox's HTML or script as this origin's page. The
// media route does the same with the type from the name's extension
// (editorMediaTypes): a name it does not know is refused.

const (
	maxEditorBytes = 5 << 20
	// The quick open list: at most maxEditorFiles entries, maxEditorList
	// bytes of ripgrep's output, kept editorListTTL per folder.
	maxEditorFiles = 50000
	maxEditorList  = 16 << 20
	editorListTTL  = 30 * time.Second
	// The SFTP walk (without ripgrep) reads at most this many directories.
	maxEditorDirs = 20000
	// The tree shows at most this many entries of a directory.
	maxEditorDirEntries = 2000
	// The image preview reads at most this much.
	maxEditorImageBytes = 20 << 20
)

var (
	errEditorPath       = errCode("editor_path", nil, "パスはホームからの相対パスで指定してください (/ で始まるパスや .. は使えません)")
	errEditorBinary     = errCode("editor_binary", nil, "テキストではない (バイナリの) ファイルは開けません")
	errEditorLarge      = errCode("editor_too_large", map[string]any{"max": maxEditorBytes >> 20}, fmt.Sprintf("ファイルが大きすぎます (%d MB まで)", maxEditorBytes>>20))
	errEditorChanged    = errCode("editor_changed", nil, "ファイルはほかで変更されています")
	errEditorNotFile    = errCode("editor_not_file", nil, "ファイルではありません")
	errEditorImage      = errCode("editor_not_image", nil, "テキストでも画像でもない (バイナリの) ファイルは開けません")
	errEditorMedia      = errCode("editor_not_media", nil, "音声・動画として開ける種類のファイルではありません")
	errEditorImageLarge = errCode("editor_image_too_large", map[string]any{"max": maxEditorImageBytes >> 20}, fmt.Sprintf("画像が大きすぎます (%d MB まで)", maxEditorImageBytes>>20))
	// errNoRipgrep: the sandbox has no rg; the listing walks SFTP instead.
	errNoRipgrep = errors.New("no ripgrep in the sandbox")
	// errEditorOutside: a terminal's path is not under the root.
	errEditorOutside = errors.New("outside the editor's root")
	// errEditorNoDir: the folder to list does not exist.
	errEditorNoDir = errCode("editor_no_dir", nil, "フォルダーがありません")
)

// editorSkipDirs are the directories the SFTP walk does not enter: heavy
// trees nobody edits by hand (ripgrep skips most through .gitignore).
var editorSkipDirs = map[string]bool{".git": true, "node_modules": true, ".cache": true, ".rustup": true, ".cargo": true, ".npm": true, "target": true, ".local": true}

// editorSkipGlobs are ripgrep's arguments that skip the same directories
// (the home has no .gitignore for its caches).
func editorSkipGlobs() []string {
	globs := []string{"-g", "!**/go/pkg"}
	for _, d := range slices.Sorted(maps.Keys(editorSkipDirs)) {
		globs = append(globs, "-g", "!"+d)
	}
	return globs
}

// editorPath checks a path relative to the SFTP root and cleans it. dir
// allows the root itself ("" or ".").
func editorPath(p string, dir bool) (string, error) {
	if strings.ContainsRune(p, 0) || strings.HasPrefix(p, "/") || strings.HasPrefix(p, "~") {
		return "", errEditorPath
	}
	if p == "" {
		if dir {
			return ".", nil
		}
		return "", errEditorPath
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return "", errEditorPath
		}
	}
	c := path.Clean(p)
	if c == "." && !dir {
		return "", errEditorPath
	}
	return c, nil
}

// editorVersion is a content's version: its SHA-256.
func editorVersion(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// editorText checks that data is text the editor takes: UTF-8 without NUL.
func editorText(data []byte) error {
	if slices.Contains(data, 0) || !utf8.Valid(data) {
		return errEditorBinary
	}
	return nil
}

// readEditorFile reads a text file (following a link, as an editor does).
func readEditorFile(c *sftp.Client, p string) ([]byte, error) {
	fi, err := c.Stat(p)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, errEditorNotFile
	}
	if fi.Size() > maxEditorBytes {
		return nil, errEditorLarge
	}
	f, err := c.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxEditorBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxEditorBytes {
		return nil, errEditorLarge
	}
	return data, editorText(data)
}

// saveEditorFile writes data to p unless the file changed since the page
// read version base (any version when force). A regular file is replaced
// atomically, keeping its mode (dav.WriteFile); a link is written through
// in place, so that it stays a link. The answer is the new version.
func saveEditorFile(c *sftp.Client, p string, data []byte, base string, force bool) (string, error) {
	link, err := c.Lstat(p)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	var fi os.FileInfo
	if err == nil {
		if fi, err = c.Stat(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
	}
	if fi != nil && !fi.Mode().IsRegular() {
		return "", errEditorNotFile
	}
	if !force {
		if fi == nil {
			// Removed (or never there) since the page read it.
			return "", errEditorChanged
		}
		cur, err := readEditorFile(c, p)
		if err != nil && !errors.Is(err, errEditorBinary) {
			if errors.Is(err, errEditorLarge) {
				return "", errEditorChanged
			}
			return "", err
		}
		if editorVersion(cur) != base {
			return "", errEditorChanged
		}
	}
	if link != nil && link.Mode()&fs.ModeSymlink != 0 && fi != nil {
		f, err := c.OpenFile(p, os.O_WRONLY|os.O_TRUNC)
		if err != nil {
			return "", err
		}
		_, err = f.Write(data)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return "", err
		}
	} else if err := dav.WriteFile(c, p, data, fi); err != nil {
		return "", err
	}
	return editorVersion(data), nil
}

// fileListRuntime lists the files under dir (relative to the files' root:
// the home, or the workspace) in the sandbox, as the sandbox user, with
// ripgrep: errNoRipgrep when the image has none.
type fileListRuntime interface {
	ListFiles(ctx context.Context, w Sandbox, dir string) (files []string, truncated bool, err error)
}

// editorList is a folder's listing, kept a while (editorListTTL).
type editorList struct {
	Files     []string `json:"files"`
	Truncated bool     `json:"truncated"`
	Via       string   `json:"via"` // "rg" or "sftp"
	at        time.Time
}

type editorLists struct {
	mu sync.Mutex
	m  map[string]*editorList
}

func (l *editorLists) get(key string) *editorList {
	l.mu.Lock()
	defer l.mu.Unlock()
	v := l.m[key]
	if v == nil || time.Since(v.at) > editorListTTL {
		return nil
	}
	return v
}

func (l *editorLists) put(key string, v *editorList) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.m == nil || len(l.m) >= 16 {
		l.m = map[string]*editorList{}
	}
	l.m[key] = v
}

// walkEditorFiles lists the regular files under dir over SFTP, relative to
// dir, without entering editorSkipDirs (or go/pkg) or following links.
func walkEditorFiles(ctx context.Context, c *sftp.Client, dir string) ([]string, bool, error) {
	fi, err := c.Stat(dir)
	if err != nil || !fi.IsDir() {
		return nil, false, errEditorNoDir
	}
	var files []string
	queue := []string{""}
	for read := 0; len(queue) > 0; read++ {
		if read >= maxEditorDirs {
			return files, true, nil
		}
		if err := ctx.Err(); err != nil {
			return files, true, nil
		}
		sub := queue[0]
		queue = queue[1:]
		entries, err := c.ReadDir(path.Join(dir, sub))
		if err != nil {
			continue
		}
		for _, e := range entries {
			name := path.Join(sub, e.Name())
			switch {
			case e.IsDir():
				if !editorSkipDirs[e.Name()] && name != "go/pkg" && !strings.HasSuffix(name, "/go/pkg") {
					queue = append(queue, name)
				}
			case e.Mode().IsRegular():
				if len(files) >= maxEditorFiles {
					return files, true, nil
				}
				files = append(files, name)
			}
		}
	}
	return files, false, nil
}

// editorEntry is a directory's entry in the tree. A link's To is what it
// leads to ("dir", "file", or "" when it leads nowhere or elsewhere): the
// tree does not expand a link, and opens it only as a file.
type editorEntry struct {
	Name  string `json:"name"`
	Kind  string `json:"kind"` // "dir", "file", "symlink" or "other"
	To    string `json:"to,omitempty"`
	Size  int64  `json:"size,omitempty"`
	Heavy bool   `json:"heavy,omitempty"` // one of editorSkipDirs: not expanded by itself
}

// editorDir is one directory's listing for the tree.
type editorDir struct {
	Path      string        `json:"path"`
	Entries   []editorEntry `json:"entries"`
	Total     int           `json:"total"`
	Truncated bool          `json:"truncated"`
}

func editorKind(m fs.FileMode) string {
	switch {
	case m.IsDir():
		return "dir"
	case m.IsRegular():
		return "file"
	case m&fs.ModeSymlink != 0:
		return "symlink"
	}
	return "other"
}

// readEditorDir lists the directory dir: directories first, then by name
// (ignoring case), at most limit entries. A link is not followed beyond a
// Stat to say what it leads to.
func readEditorDir(c *sftp.Client, dir string, limit int) (*editorDir, error) {
	fi, err := c.Stat(dir)
	if err != nil {
		if errors.Is(err, fs.ErrPermission) {
			return nil, err
		}
		return nil, errEditorNoDir
	}
	if !fi.IsDir() {
		return nil, errEditorNoDir
	}
	infos, err := c.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	entries := make([]editorEntry, 0, len(infos))
	for _, e := range infos {
		if e.Name() == "." || e.Name() == ".." || e.Name() == "" || strings.Contains(e.Name(), "/") {
			continue
		}
		v := editorEntry{Name: e.Name(), Kind: editorKind(e.Mode())}
		switch v.Kind {
		case "dir":
			v.Heavy = editorSkipDirs[v.Name]
		case "file":
			v.Size = e.Size()
		}
		entries = append(entries, v)
	}
	slices.SortFunc(entries, func(a, b editorEntry) int {
		if (a.Kind == "dir") != (b.Kind == "dir") {
			if a.Kind == "dir" {
				return -1
			}
			return 1
		}
		if c := strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)); c != 0 {
			return c
		}
		return strings.Compare(a.Name, b.Name)
	})
	out := &editorDir{Path: dir, Total: len(entries)}
	if len(entries) > limit {
		entries, out.Truncated = entries[:limit], true
	}
	for i := range entries {
		if entries[i].Kind != "symlink" {
			continue
		}
		if to, err := c.Stat(path.Join(dir, entries[i].Name)); err == nil {
			if k := editorKind(to.Mode()); k == "dir" || k == "file" {
				entries[i].To = k
				if k == "file" {
					entries[i].Size = to.Size()
				}
			}
		}
	}
	out.Entries = entries
	return out, nil
}

// editorImageTypes and ICO, AVIF and SVG (editorImageType) are the images
// the preview serves. Anything else is refused: the route must not hand
// the browser HTML, script or a type it might render as a page.
var editorImageTypes = map[string]bool{"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true, "image/bmp": true}

// editorImageType tells an image's type from its content ("" when it is
// not one the preview serves). The name is not trusted.
func editorImageType(data []byte) string {
	if t := http.DetectContentType(data); editorImageTypes[t] {
		return t
	}
	switch {
	case len(data) >= 6 && string(data[:4]) == "\x00\x00\x01\x00" && (data[4] != 0 || data[5] != 0):
		return "image/x-icon"
	case isAVIF(data):
		return "image/avif"
	case isSVG(data):
		return "image/svg+xml"
	}
	return ""
}

// isAVIF: an ISO BMFF ftyp box naming the avif (or avis) brand.
func isAVIF(data []byte) bool {
	if len(data) < 16 || string(data[4:8]) != "ftyp" {
		return false
	}
	size := int(binary.BigEndian.Uint32(data[:4]))
	if size < 16 || size > len(data) {
		size = min(len(data), 256)
	}
	for i := 8; i+4 <= size; i += 4 {
		if i == 12 {
			continue // the minor version
		}
		if b := string(data[i : i+4]); b == "avif" || b == "avis" {
			return true
		}
	}
	return false
}

// isSVG: an svg root element, after an optional BOM, XML declaration or
// processing instructions, comments and a doctype without an internal
// subset (entities are not taken).
func isSVG(data []byte) bool {
	s := bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	for {
		s = bytes.TrimLeft(s, " \t\r\n")
		var stop string
		switch {
		case bytes.HasPrefix(s, []byte("<?")):
			stop = "?>"
		case bytes.HasPrefix(s, []byte("<!--")):
			stop = "-->"
		case bytes.HasPrefix(s, []byte("<!DOCTYPE")):
			stop = ">"
		default:
			return len(s) > 4 && string(s[:4]) == "<svg" && strings.IndexByte(" \t\r\n>/", s[4]) >= 0
		}
		i := bytes.Index(s[2:], []byte(stop))
		if i < 0 {
			return false
		}
		end := 2 + i + len(stop)
		if stop == ">" && bytes.IndexByte(s[:end], '[') >= 0 {
			return false
		}
		s = s[end:]
	}
}

// editorMediaTypes are the audio and video the editor plays, by extension.
var editorMediaTypes = map[string]string{
	".mp3": "audio/mpeg", ".wav": "audio/wav", ".ogg": "audio/ogg", ".oga": "audio/ogg", ".opus": "audio/ogg",
	".m4a": "audio/mp4", ".aac": "audio/aac", ".flac": "audio/flac", ".weba": "audio/webm",
	".mp4": "video/mp4", ".m4v": "video/mp4", ".webm": "video/webm", ".ogv": "video/ogg", ".mov": "video/quicktime", ".mkv": "video/x-matroska",
}

// readEditorImage reads an image (following a link) and tells its type.
func readEditorImage(c *sftp.Client, p string) ([]byte, string, error) {
	fi, err := c.Stat(p)
	if err != nil {
		return nil, "", err
	}
	if !fi.Mode().IsRegular() {
		return nil, "", errEditorNotFile
	}
	if fi.Size() > maxEditorImageBytes {
		return nil, "", errEditorImageLarge
	}
	f, err := c.Open(p)
	if err != nil {
		return nil, "", err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxEditorImageBytes+1))
	if err != nil {
		return nil, "", err
	}
	if len(data) > maxEditorImageBytes {
		return nil, "", errEditorImageLarge
	}
	t := editorImageType(data)
	if t == "" {
		return nil, "", errEditorImage
	}
	return data, t, nil
}

// editorFail maps an error to the page's answer.
func editorFail(w http.ResponseWriter, name, what string, err error) {
	switch {
	case errors.Is(err, dav.ErrStopped):
		failCode(w, 409, "editor_stopped", nil, "サンドボックスは停止しています。起動すると開けます")
	case errors.Is(err, errEditorChanged):
		jsonResponse(w, 409, map[string]any{"error": err.Error(), "code": "editor_changed", "changed": true})
	case errors.Is(err, errEditorPath), errors.Is(err, errEditorNotFile), errors.Is(err, errEditorNoDir):
		failErr(w, 400, err)
	case errors.Is(err, errEditorBinary), errors.Is(err, errEditorImage), errors.Is(err, errEditorMedia):
		failErr(w, 415, err)
	case errors.Is(err, errEditorLarge), errors.Is(err, errEditorImageLarge):
		failErr(w, 413, err)
	case errors.Is(err, fs.ErrNotExist):
		failCode(w, 404, "editor_missing", nil, "ファイルがありません")
	case errors.Is(err, fs.ErrPermission):
		failCode(w, 403, "editor_denied", nil, "権限がありません")
	default:
		log.Printf("editor %s: %s: %v", name, what, english(err))
		failCode(w, 502, "editor_failed", map[string]any{"detail": safeCLIError(err)}, "サンドボックスのファイルを扱えませんでした: "+safeCLIError(err))
	}
}

// handleEditor serves the editor's routes (parts: NAME, "editor", what).
func (s *Server) handleEditor(w http.ResponseWriter, r *http.Request, parts []string) {
	if len(parts) != 3 || (parts[2] != "files" && parts[2] != "file" && parts[2] != "dir" && parts[2] != "raw" && parts[2] != "media" && parts[2] != "root" && parts[2] != "resolve") {
		fail(w, 404, "unknown sandbox route")
		return
	}
	save := parts[2] == "file" && r.Method == http.MethodPost
	if r.Method != http.MethodGet && !save {
		fail(w, 405, "method not allowed")
		return
	}
	sb, ok := s.sandboxFor(w, parts, 3)
	if !ok || !s.require(w, r, save) {
		return
	}
	if s.files == nil {
		failCode(w, 501, "editor_unavailable", nil, "エディタは使えません")
		return
	}
	switch {
	case parts[2] == "root":
		s.editorRoot(w, r, sb)
	case parts[2] == "resolve":
		s.editorResolve(w, r, sb)
	case parts[2] == "files":
		s.editorFiles(w, r, sb)
	case parts[2] == "dir":
		s.editorDir(w, r, sb)
	case parts[2] == "raw":
		s.editorRaw(w, r, sb)
	case parts[2] == "media":
		s.editorMedia(w, r, sb)
	case save:
		s.editorSave(w, r, sb)
	default:
		s.editorRead(w, r, sb)
	}
}

// editorRoot answers the sandbox's FilesRoot, and work: whether the root
// has workDir (the editor's folder when the page has none saved). Home is
// false for a sandbox whose daemon has no SFTP server (the page says that
// recreating it gives the home); the files tab asks too.
func (s *Server) editorRoot(w http.ResponseWriter, r *http.Request, sb Sandbox) {
	rt, ok := s.rt.(filesRootRuntime)
	if !ok {
		failCode(w, 501, "editor_unavailable", nil, "エディタは使えません")
		return
	}
	ctx, cancel := timeout(r.Context(), time.Minute)
	defer cancel()
	if s.rt.Status(ctx, sb).State != "running" {
		editorFail(w, sb.Name, "root", dav.ErrStopped)
		return
	}
	root, err := rt.FilesRoot(ctx, sb)
	if err != nil {
		editorFail(w, sb.Name, "root", err)
		return
	}
	v := struct {
		FilesRoot
		Work bool `json:"work"`
	}{FilesRoot: root}
	err = s.files.Do(ctx, sb.Name, "editor root", func(c *sftp.Client) error {
		fi, err := c.Stat(workDir)
		v.Work = err == nil && fi.IsDir()
		return nil
	})
	if err != nil {
		editorFail(w, sb.Name, "root", err)
		return
	}
	jsonResponse(w, 200, v)
}

// termDirRuntime tells a terminal's working directory ("" when unknown).
type termDirRuntime interface {
	TermDir(ctx context.Context, w Sandbox, term string) string
}

// terminalPath turns a path printed in a terminal into one relative to
// root: ~/ is the home, and a relative path is taken from dir (the
// terminal's working directory, else where terminals start).
func terminalPath(p, dir, root string) (string, error) {
	switch {
	case p == "~" || strings.HasPrefix(p, "~/"):
		p = homeDir + p[1:]
	case !strings.HasPrefix(p, "/"):
		if !strings.HasPrefix(dir, "/") {
			dir = homeDir + "/" + workDir
		}
		p = dir + "/" + p
	}
	rel, ok := strings.CutPrefix(path.Clean(p), strings.TrimSuffix(root, "/")+"/")
	if !ok {
		return "", errEditorOutside
	}
	return editorPath(rel, false)
}

// editorResolve answers {path} for a path printed in a terminal when it is
// a file under the root.
func (s *Server) editorResolve(w http.ResponseWriter, r *http.Request, sb Sandbox) {
	q := r.URL.Query()
	p := q.Get("path")
	if p == "" || len(p) > 4096 || strings.ContainsRune(p, 0) {
		failErr(w, 400, errEditorPath)
		return
	}
	rt, ok := s.rt.(filesRootRuntime)
	if !ok {
		failCode(w, 501, "editor_unavailable", nil, "エディタは使えません")
		return
	}
	ctx, cancel := timeout(r.Context(), time.Minute)
	defer cancel()
	if s.rt.Status(ctx, sb).State != "running" {
		editorFail(w, sb.Name, "resolve", dav.ErrStopped)
		return
	}
	root, err := rt.FilesRoot(ctx, sb)
	if err != nil {
		editorFail(w, sb.Name, "resolve", err)
		return
	}
	dir := ""
	if td, ok := s.rt.(termDirRuntime); ok && !strings.HasPrefix(p, "/") && !strings.HasPrefix(p, "~") && validTermName(q.Get("term")) {
		dctx, dcancel := context.WithTimeout(ctx, 2*time.Second)
		dir = td.TermDir(dctx, sb, q.Get("term"))
		dcancel()
	}
	rel, err := terminalPath(p, dir, root.Path)
	if errors.Is(err, errEditorOutside) {
		failCode(w, 400, "editor_outside", map[string]any{"root": root.Path}, root.Path+" の外のファイルはエディタで開けません")
		return
	}
	if err == nil {
		err = s.files.Do(ctx, sb.Name, "editor resolve "+rel, func(c *sftp.Client) error {
			fi, err := c.Stat(rel)
			if err == nil && fi.IsDir() {
				err = errEditorNotFile
			}
			return err
		})
	}
	if err != nil {
		editorFail(w, sb.Name, "resolve "+rel, err)
		return
	}
	jsonResponse(w, 200, map[string]any{"path": rel})
}

func (s *Server) editorFiles(w http.ResponseWriter, r *http.Request, sb Sandbox) {
	dir, err := editorPath(r.URL.Query().Get("dir"), true)
	if err != nil {
		failErr(w, 400, err)
		return
	}
	key := sb.Name + "\x00" + dir
	if r.URL.Query().Get("refresh") == "" {
		if v := s.editorLists.get(key); v != nil {
			jsonResponse(w, 200, v)
			return
		}
	}
	ctx, cancel := timeout(r.Context(), time.Minute)
	defer cancel()
	v := &editorList{Via: "rg", at: time.Now()}
	rt, hasRT := s.rt.(fileListRuntime)
	err = errNoRipgrep
	if hasRT {
		if s.rt.Status(ctx, sb).State != "running" {
			editorFail(w, sb.Name, "list "+dir, dav.ErrStopped)
			return
		}
		v.Files, v.Truncated, err = rt.ListFiles(ctx, sb, dir)
	}
	if errors.Is(err, errNoRipgrep) {
		v.Via = "sftp"
		err = s.files.Do(ctx, sb.Name, "editor list "+dir, func(c *sftp.Client) error {
			var e error
			v.Files, v.Truncated, e = walkEditorFiles(ctx, c, dir)
			return e
		})
	}
	if err != nil {
		editorFail(w, sb.Name, "list "+dir, err)
		return
	}
	slices.Sort(v.Files)
	if v.Files == nil {
		v.Files = []string{}
	}
	s.editorLists.put(key, v)
	jsonResponse(w, 200, v)
}

func (s *Server) editorDir(w http.ResponseWriter, r *http.Request, sb Sandbox) {
	dir, err := editorPath(r.URL.Query().Get("path"), true)
	if err != nil {
		failErr(w, 400, err)
		return
	}
	ctx, cancel := timeout(r.Context(), time.Minute)
	defer cancel()
	var v *editorDir
	err = s.files.Do(ctx, sb.Name, "editor dir "+dir, func(c *sftp.Client) error {
		var e error
		v, e = readEditorDir(c, dir, maxEditorDirEntries)
		return e
	})
	if err != nil {
		editorFail(w, sb.Name, "dir "+dir, err)
		return
	}
	jsonResponse(w, 200, v)
}

func (s *Server) editorRead(w http.ResponseWriter, r *http.Request, sb Sandbox) {
	p, err := editorPath(r.URL.Query().Get("path"), false)
	if err != nil {
		failErr(w, 400, err)
		return
	}
	ctx, cancel := timeout(r.Context(), time.Minute)
	defer cancel()
	var data []byte
	err = s.files.Do(ctx, sb.Name, "editor read "+p, func(c *sftp.Client) error {
		var e error
		data, e = readEditorFile(c, p)
		return e
	})
	if err != nil {
		editorFail(w, sb.Name, "read "+p, err)
		return
	}
	jsonResponse(w, 200, map[string]any{"path": p, "content": string(data), "version": editorVersion(data)})
}

// editorRaw serves an image for the preview. Opened by itself (a new tab),
// the CSP sandboxes it: an SVG runs no script and loads nothing.
func (s *Server) editorRaw(w http.ResponseWriter, r *http.Request, sb Sandbox) {
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	h.Set("Cache-Control", "no-store")
	p, err := editorPath(r.URL.Query().Get("path"), false)
	if err != nil {
		failErr(w, 400, err)
		return
	}
	ctx, cancel := timeout(r.Context(), time.Minute)
	defer cancel()
	var data []byte
	var typ string
	err = s.files.Do(ctx, sb.Name, "editor raw "+p, func(c *sftp.Client) error {
		var e error
		data, typ, e = readEditorImage(c, p)
		return e
	})
	if err != nil {
		editorFail(w, sb.Name, "raw "+p, err)
		return
	}
	h.Set("Content-Type", typ)
	h.Set("Content-Disposition", "inline")
	h.Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(200)
	_, _ = w.Write(data)
}

// editorMedia streams audio or video with Range (seeking) under the raw
// route's headers. The response holds an SFTP lease while it is read, as a
// WebDAV GET does, with no time limit (playback may pause).
func (s *Server) editorMedia(w http.ResponseWriter, r *http.Request, sb Sandbox) {
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	h.Set("Cache-Control", "no-store")
	p, err := editorPath(r.URL.Query().Get("path"), false)
	if err != nil {
		failErr(w, 400, err)
		return
	}
	typ := editorMediaTypes[strings.ToLower(path.Ext(p))]
	if typ == "" {
		failErr(w, 415, errEditorMedia)
		return
	}
	ctx, cancel := timeout(r.Context(), time.Minute)
	defer cancel()
	f, fi, err := s.files.OpenRead(ctx, sb.Name, p)
	if err == nil && !fi.Mode().IsRegular() {
		f.Close()
		err = errEditorNotFile
	}
	if err != nil {
		editorFail(w, sb.Name, "media "+p, err)
		return
	}
	defer f.Close()
	h.Set("Content-Type", typ)
	h.Set("Content-Disposition", "inline")
	http.ServeContent(w, r, "", fi.ModTime(), f)
}

func (s *Server) editorSave(w http.ResponseWriter, r *http.Request, sb Sandbox) {
	// A phone's upload may take longer than the server's ReadTimeout.
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(5 * time.Minute))
	var body struct {
		Path    string `json:"path"`
		Content string `json:"content"`
		Base    string `json:"base"`
		Force   bool   `json:"force"`
	}
	if decodeLimit(w, r, &body, 6*maxEditorBytes+4096) != nil { // JSON escapes
		failCode(w, 400, "editor_bad_body", nil, "保存する内容を受け取れませんでした")
		return
	}
	p, err := editorPath(body.Path, false)
	if err != nil {
		failErr(w, 400, err)
		return
	}
	data := []byte(body.Content)
	if len(data) > maxEditorBytes {
		failErr(w, 413, errEditorLarge)
		return
	}
	ctx, cancel := timeout(r.Context(), time.Minute)
	defer cancel()
	var version string
	err = s.files.Do(ctx, sb.Name, "editor save "+p, func(c *sftp.Client) error {
		var e error
		version, e = saveEditorFile(c, p, data, body.Base, body.Force)
		return e
	})
	if err != nil {
		editorFail(w, sb.Name, "save "+p, err)
		return
	}
	jsonResponse(w, 200, map[string]string{"path": p, "version": version})
}
