package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"iwato/internal/dav"
)

// Files (ファイル): a sandbox's home /home/dev as a network drive, through
// the daemon's SFTP server (docs/adr/0005); without it, the workspace
// through OpenShell's SFTP.
// Iwato serves WebDAV at /dav/ and turns it into SFTP on the sandbox
// (internal/dav).
//
//	/dav/                 the sandboxes the credential sees (read-only)
//	/dav/<name>/…         sandbox <name>'s home (or workspace)/…
//	/dav/<token>/<name>/… the same with a secret URL (this PC only)
//
// Credentials, made in the page: app passwords (Basic authentication; the
// user name is the password's ID) for the tailnet's HTTPS (tailscale
// serve) and this PC, and secret URLs for this PC's http://127.0.0.1,
// where Windows does not send Basic authentication. Each has a scope (one
// sandbox or all) and may be read-only. Iwato keeps only their SHA-256.

// filesRuntime opens a sandbox's files (OpenShell and Fake).
type filesRuntime interface {
	DialFiles(context.Context, Sandbox) (dav.Transport, error)
}

// FilesRoot is where a sandbox's files (the SFTP root) are in the sandbox:
// Path, absolute; Home when that is the home (otherwise the workspace, in
// a sandbox without the daemon's SFTP server; recreating gives the home).
type FilesRoot struct {
	Path string `json:"path"`
	Home bool   `json:"home"`
}

// filesRootRuntime tells a running sandbox's FilesRoot (OpenShell and Fake).
type filesRootRuntime interface {
	FilesRoot(context.Context, Sandbox) (FilesRoot, error)
}

const (
	davKindPassword = "password"
	davKindURL      = "url"
	maxDavKeys      = 50
)

type davKey struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
	// Hash is the hex SHA-256 of the secret (32 random bytes: no slow
	// hash needed).
	Hash string `json:"hash,omitempty"`
	// Sandbox is the one sandbox the key opens; empty for all.
	Sandbox    string    `json:"sandbox,omitempty"`
	ReadOnly   bool      `json:"readOnly"`
	CreatedAt  time.Time `json:"createdAt"`
	LastUsed   time.Time `json:"lastUsed,omitzero"`
	LastClient string    `json:"lastClient,omitempty"`
}

// davKeys are the credentials, in dav-keys.json next to the configuration
// (in memory only when path is empty: the fake, tests).
type davKeys struct {
	mu    sync.Mutex
	path  string
	keys  []davKey
	saved time.Time
}

func loadDavKeys(path string) (*davKeys, error) {
	k := &davKeys{path: path}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return k, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &k.keys); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return k, nil
}

func (k *davKeys) saveLocked() error {
	k.saved = time.Now()
	if k.path == "" {
		return nil
	}
	b, err := json.MarshalIndent(k.keys, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(k.path), 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(k.path), ".dav-keys-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(append(b, '\n')); err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp.Name(), k.path)
}

var davEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// davSecret is 32 random bytes in lowercase base32 (52 letters and
// digits: longer than any sandbox name, which tells a secret URL apart).
func davSecret() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return strings.ToLower(davEncoding.EncodeToString(b))
}

func davHash(secret string) string {
	h := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(h[:])
}

func validDavName(name string) error {
	if name == "" || utf8.RuneCountInString(name) > 40 {
		return errCode("files_name_length", nil, "名前は 1〜40 文字にしてください")
	}
	if strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return errCode("files_name_control", nil, "名前に制御文字は使えません")
	}
	return nil
}

func (k *davKeys) create(name, kind, sandbox string, readOnly bool) (davKey, string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if len(k.keys) >= maxDavKeys {
		return davKey{}, "", errCode("files_too_many", map[string]any{"max": maxDavKeys}, fmt.Sprintf("%d 個までです。使わないものを取り消してください", maxDavKeys))
	}
	var id string
	for {
		id = "iwato-" + davSecret()[:6]
		if !slices.ContainsFunc(k.keys, func(x davKey) bool { return x.ID == id }) {
			break
		}
	}
	secret := davSecret()
	key := davKey{ID: id, Name: name, Kind: kind, Hash: davHash(secret), Sandbox: sandbox, ReadOnly: readOnly, CreatedAt: time.Now().UTC()}
	k.keys = append(k.keys, key)
	if err := k.saveLocked(); err != nil {
		k.keys = k.keys[:len(k.keys)-1]
		return davKey{}, "", err
	}
	key.Hash = ""
	return key, secret, nil
}

// list is the credentials without their hashes.
func (k *davKeys) list() []davKey {
	k.mu.Lock()
	defer k.mu.Unlock()
	out := make([]davKey, len(k.keys))
	for i, key := range k.keys {
		key.Hash = ""
		out[i] = key
	}
	return out
}

func (k *davKeys) revoke(id string) (davKey, bool, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	i := slices.IndexFunc(k.keys, func(x davKey) bool { return x.ID == id })
	if i < 0 {
		return davKey{}, false, nil
	}
	key := k.keys[i]
	k.keys = slices.Delete(k.keys, i, i+1)
	if err := k.saveLocked(); err != nil {
		k.keys = slices.Insert(k.keys, i, key)
		return davKey{}, true, err
	}
	return key, true, nil
}

// find is the key of kind whose secret hashes to hash (and whose ID is id
// when given). Every key is compared, in constant time.
func (k *davKeys) find(kind, id, secret string) (davKey, bool) {
	hash := []byte(davHash(secret))
	k.mu.Lock()
	defer k.mu.Unlock()
	var found davKey
	ok := false
	for _, key := range k.keys {
		match := subtle.ConstantTimeCompare([]byte(key.Hash), hash) == 1
		if match && key.Kind == kind && (id == "" || key.ID == id) {
			found, ok = key, true
		}
	}
	return found, ok
}

// used records the key's last use; the file is written at most once a
// minute for it.
func (k *davKeys) used(id, client string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	i := slices.IndexFunc(k.keys, func(x davKey) bool { return x.ID == id })
	if i < 0 {
		return
	}
	if len(client) > 120 {
		client = client[:120]
	}
	k.keys[i].LastUsed = time.Now().UTC()
	k.keys[i].LastClient = strings.ToValidUTF8(client, "")
	if time.Since(k.saved) > time.Minute {
		_ = k.saveLocked()
	}
}

// davDelay slows down wrong credentials.
var davDelay = 500 * time.Millisecond

func davChallenge(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Basic realm="Iwato", charset="UTF-8"`)
	http.Error(w, "認証が必要です", http.StatusUnauthorized)
}

// davOptions answers OPTIONS without credentials (Windows asks before it
// authenticates).
func davOptions(w http.ResponseWriter) {
	w.Header().Set("DAV", "1, 2")
	w.Header().Set("MS-Author-Via", "DAV")
	w.Header().Set("Allow", "OPTIONS, GET, HEAD, POST, PUT, DELETE, MKCOL, COPY, MOVE, LOCK, UNLOCK, PROPFIND, PROPPATCH")
	w.WriteHeader(http.StatusOK)
}

// localHost reports whether host is the Host header of this PC's own requests.
func (s *Server) localHost(host string) bool {
	if host == s.host {
		return true
	}
	_, port, err := net.SplitHostPort(s.host)
	return err == nil && host == "localhost:"+port
}

// loopbackDirect is a request from this PC that no proxy relayed:
// tailscale serve also connects from 127.0.0.1, but says it forwarded.
func loopbackDirect(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	ip := net.ParseIP(host)
	if err != nil || ip == nil || !ip.IsLoopback() {
		return false
	}
	for _, h := range []string{"X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "Forwarded", "Tailscale-User-Login", "Tailscale-Headers-Info"} {
		if r.Header.Get(h) != "" {
			return false
		}
	}
	return true
}

func davPath(p string) bool { return p == "/dav" || strings.HasPrefix(p, "/dav/") }

// handleDAV serves /dav/. It comes before the page's checks that need an
// Origin (WebDAV clients send none, nor cookies): the Host is checked here
// (DNS rebinding), and requests from web pages elsewhere are refused.
func (s *Server) handleDAV(w http.ResponseWriter, r *http.Request) {
	local := s.localHost(r.Host)
	if !local && (s.publicHost == "" || r.Host != s.publicHost) {
		reject(w, r, 403, "invalid host", "Host="+r.Host+" expected="+s.host)
		return
	}
	if o := r.Header.Get("Origin"); o != "" && !s.allowedOrigin(o) {
		reject(w, r, 403, "invalid origin", "Origin="+o)
		return
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		reject(w, r, 403, "cross-site request blocked", "Sec-Fetch-Site="+site)
		return
	}
	// Files from the sandbox must not run as this origin's pages.
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; img-src 'self' data:; media-src 'self'; sandbox")
	if r.Method == http.MethodOptions {
		davOptions(w)
		return
	}
	prefix := "/dav"
	rest := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/dav"), "/")
	first, after, _ := strings.Cut(rest, "/")
	var key davKey
	if len(first) > 32 {
		// A secret URL: this PC only, never through tailscale serve.
		if !local || !loopbackDirect(r) {
			rejectCode(w, r, 403, "secret_url_local", "秘密の URL はこの PC の http://127.0.0.1 からだけ使えます", "secret URL from "+r.Host)
			return
		}
		var ok bool
		if key, ok = s.davKeys.find(davKindURL, "", first); !ok {
			time.Sleep(davDelay)
			http.Error(w, "この URL は無効です (取り消されたか、まちがっています)", http.StatusNotFound)
			return
		}
		prefix += "/" + first
		first, _, _ = strings.Cut(after, "/")
	} else {
		user, pass, ok := r.BasicAuth()
		if !ok {
			// Windows looks at the "share" (/dav) before the folder
			// asked for, without credentials on http: show it empty.
			if local && first == "" && r.Method == "PROPFIND" {
				dav.ServeList(w, r, prefix, nil)
				return
			}
			davChallenge(w)
			return
		}
		if key, ok = s.davKeys.find(davKindPassword, user, pass); !ok {
			time.Sleep(davDelay)
			fmt.Fprintf(os.Stderr, "rejected %s %s: wrong app password for %q\n", r.Method, r.URL.Path, strings.ToValidUTF8(user, "?"))
			davChallenge(w)
			return
		}
	}
	s.davKeys.used(key.ID, r.UserAgent())
	// The server reads a request within 15 seconds (http.Server's
	// ReadTimeout); an upload takes as long as it takes.
	_ = http.NewResponseController(w).SetReadDeadline(time.Time{})
	if first == "" {
		var names []string
		for _, sb := range s.sandboxList() {
			if key.Sandbox == "" || key.Sandbox == sb.Name {
				names = append(names, sb.Name)
			}
		}
		dav.ServeList(w, r, prefix, names)
		return
	}
	if _, found := s.lookupSandbox(first); !found || (key.Sandbox != "" && key.Sandbox != first) {
		http.Error(w, "サンドボックスがありません", http.StatusNotFound)
		return
	}
	if s.files == nil {
		http.Error(w, "ファイルは使えません", http.StatusNotImplemented)
		return
	}
	s.files.ServeSandbox(w, r, first, dav.Options{Prefix: prefix + "/" + first, ReadOnly: key.ReadOnly})
}

// handleFiles is the page's API for the credentials:
//
//	GET  /api/files                    the credentials and the origins
//	POST /api/files/keys               make one (its secret is shown once)
//	POST /api/files/keys/<id>/revoke   revoke one
func (s *Server) handleFiles(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/files"), "/"), "/")
	switch {
	case r.Method == http.MethodGet && len(parts) == 1 && parts[0] == "":
		if !s.require(w, r, false) {
			return
		}
		jsonResponse(w, 200, map[string]any{"keys": s.davKeys.list(), "publicOrigin": s.publicOrigin, "localOrigin": s.origin, "available": s.files != nil})
	case r.Method == http.MethodPost && len(parts) == 1 && parts[0] == "keys":
		if !s.require(w, r, true) {
			return
		}
		var body struct {
			Name     string `json:"name"`
			Kind     string `json:"kind"`
			Sandbox  string `json:"sandbox"`
			ReadOnly bool   `json:"readOnly"`
		}
		if decode(w, r, &body) != nil {
			fail(w, 400, "invalid request")
			return
		}
		body.Name = strings.TrimSpace(body.Name)
		if err := validDavName(body.Name); err != nil {
			failErr(w, 400, err)
			return
		}
		if body.Kind != davKindPassword && body.Kind != davKindURL {
			fail(w, 400, "kind must be password or url")
			return
		}
		if body.Sandbox != "" {
			if _, ok := s.lookupSandbox(body.Sandbox); !ok {
				fail(w, 404, "unknown sandbox")
				return
			}
		}
		key, secret, err := s.davKeys.create(body.Name, body.Kind, body.Sandbox, body.ReadOnly)
		if err != nil {
			failErr(w, 409, err)
			return
		}
		jsonResponse(w, 200, map[string]any{"key": key, "secret": secret})
	case r.Method == http.MethodPost && len(parts) == 3 && parts[0] == "keys" && parts[2] == "revoke":
		if !s.require(w, r, true) {
			return
		}
		_, found, err := s.davKeys.revoke(parts[1])
		if err != nil {
			failErr(w, 500, err)
			return
		}
		if !found {
			fail(w, 404, "unknown key")
			return
		}
		// Downloads in progress with it end too.
		if s.files != nil {
			s.files.CloseAll()
		}
		jsonResponse(w, 200, map[string]bool{"ok": true})
	default:
		fail(w, 405, "method/operation not allowed")
	}
}

// newFilesPool serves the runtime's sandboxes' files, if it has them.
func (s *Server) newFilesPool() *dav.Pool {
	fr, ok := s.rt.(filesRuntime)
	if !ok {
		return nil
	}
	return dav.NewPool(func(ctx context.Context, name string) (dav.Transport, error) {
		sb, found := s.lookupSandbox(name)
		if !found {
			return nil, os.ErrNotExist
		}
		return fr.DialFiles(ctx, sb)
	})
}

// filesChanged closes the sandbox's file connection after it started,
// stopped or was deleted (the next request opens it again).
func (s *Server) filesChanged(name string) {
	if s.files != nil {
		s.files.Close(name)
	}
}
