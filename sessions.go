package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// Browser sessions survive a restart of serve, so a paired phone stays signed
// in until its session expires. Only a SHA-256 of each session cookie is kept
// (with its CSRF token, expiry, when it was made and last used, and the
// browser's User-Agent): the file alone cannot sign anyone in. Deleting it
// signs every device out.
//
// The settings list the logins (GET /api/sessions); the owner signs this
// browser out (POST /api/sessions/logout), another one
// (POST /api/sessions/ID/revoke) or every other one
// (POST /api/sessions/revoke-others). A session that ends closes its
// terminals' WebSockets.

type savedSession struct {
	Hash    string    `json:"hash"`
	CSRF    string    `json:"csrf"`
	Expires time.Time `json:"expires"`
	Created time.Time `json:"created,omitzero"`
	Used    time.Time `json:"used,omitzero"`
	Agent   string    `json:"agent,omitempty"`
}

// sessionUseStep is how often a session's last use is recorded.
const sessionUseStep = time.Minute

// userAgent is the browser's User-Agent as kept with its session.
func userAgent(r *http.Request) string {
	ua := strings.Map(func(c rune) rune {
		if c < 0x20 || c == 0x7f {
			return -1
		}
		return c
	}, r.UserAgent())
	for len(ua) > 300 {
		_, size := utf8.DecodeLastRuneInString(ua)
		ua = ua[:len(ua)-size]
	}
	return ua
}

type sessionFile struct {
	CookieName string         `json:"cookieName"`
	Sessions   []savedSession `json:"sessions"`
}

var cookieNamePattern = regexp.MustCompile(`^iwato_[a-f0-9]{16}$`)
var hex64 = regexp.MustCompile(`^[a-f0-9]{64}$`)

func sessionHash(key string) string {
	h := sha256.Sum256([]byte(key))
	return hex.EncodeToString(h[:])
}

// loadSessions restores the cookie name and unexpired sessions. A missing
// file is not an error; a malformed one is, so that it is not overwritten.
func (s *Server) loadSessions(path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessionPath = path
	b, e := os.ReadFile(path)
	if errors.Is(e, os.ErrNotExist) {
		return s.saveSessionsLocked()
	}
	if e != nil {
		return e
	}
	var f sessionFile
	if e = json.Unmarshal(b, &f); e != nil || !cookieNamePattern.MatchString(f.CookieName) {
		return fmt.Errorf("%s is not a valid session file (delete it to sign every device out)", path)
	}
	s.cookieName = f.CookieName
	now := time.Now()
	for _, v := range f.Sessions {
		if hex64.MatchString(v.Hash) && hex64.MatchString(v.CSRF) && now.Before(v.Expires) {
			s.sessions[v.Hash] = authSession{csrf: v.CSRF, expires: v.Expires, created: v.Created, used: v.Used, agent: v.Agent, ended: make(chan struct{})}
		}
	}
	return s.saveSessionsLocked()
}

// saveSessionsLocked writes the store atomically; s.mu must be held. Without
// a path (fake, tests) sessions live in memory only.
func (s *Server) saveSessionsLocked() error {
	if s.sessionPath == "" {
		return nil
	}
	f := sessionFile{CookieName: s.cookieName, Sessions: []savedSession{}}
	now := time.Now()
	for h, a := range s.sessions {
		if now.Before(a.expires) {
			f.Sessions = append(f.Sessions, savedSession{Hash: h, CSRF: a.csrf, Expires: a.expires, Created: a.created, Used: a.used, Agent: a.agent})
		}
	}
	b, _ := json.MarshalIndent(f, "", "  ")
	tmp, e := os.CreateTemp(filepath.Dir(s.sessionPath), ".sessions-*")
	if e != nil {
		return e
	}
	defer os.Remove(tmp.Name())
	if _, e = tmp.Write(b); e == nil {
		e = tmp.Chmod(0o600)
	}
	if e2 := tmp.Close(); e == nil {
		e = e2
	}
	if e != nil {
		return e
	}
	return os.Rename(tmp.Name(), s.sessionPath)
}

// endSessionLocked removes a session and closes its terminals; s.mu must
// be held.
func (s *Server) endSessionLocked(h string) {
	if a, ok := s.sessions[h]; ok {
		if a.ended != nil {
			close(a.ended)
		}
		delete(s.sessions, h)
	}
}

// sessionID names a session for the page: part of its hash, which is no
// credential.
func sessionID(h string) string { return h[:16] }

type sessionView struct {
	ID      string    `json:"id"`
	Agent   string    `json:"agent"`
	Created time.Time `json:"created,omitzero"`
	Used    time.Time `json:"used,omitzero"`
	Expires time.Time `json:"expires"`
	Current bool      `json:"current"`
}

func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	op := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/sessions"), "/")
	if r.Method == http.MethodGet && op == "" {
		current, _, ok := s.currentSession(r)
		if !ok {
			failCode(w, 401, "session_expired", nil, "セッション切れ。Iwato を起動し直し、起動 URL を開いてください。")
			return
		}
		s.mu.Lock()
		list := []sessionView{}
		now := time.Now()
		for h, a := range s.sessions {
			if now.Before(a.expires) {
				list = append(list, sessionView{ID: sessionID(h), Agent: a.agent, Created: a.created, Used: a.used, Expires: a.expires, Current: h == current})
			}
		}
		s.mu.Unlock()
		sort.Slice(list, func(i, j int) bool {
			if list[i].Current != list[j].Current {
				return list[i].Current
			}
			return list[i].Used.After(list[j].Used)
		})
		jsonResponse(w, 200, map[string]any{"sessions": list})
		return
	}
	if r.Method != http.MethodPost {
		fail(w, 405, "method not allowed")
		return
	}
	if !s.require(w, r, true) {
		return
	}
	current, _, ok := s.currentSession(r)
	if !ok {
		failCode(w, 401, "session_expired", nil, "セッション切れ。Iwato を起動し直し、起動 URL を開いてください。")
		return
	}
	id, revoke := strings.CutSuffix(op, "/revoke")
	switch {
	case op == "logout":
		s.endSessions(func(h string) bool { return h == current })
		s.clearCookie(w, r)
		jsonResponse(w, 200, map[string]any{"ok": true})
	case op == "revoke-others":
		n := s.endSessions(func(h string) bool { return h != current })
		jsonResponse(w, 200, map[string]any{"ok": true, "revoked": n})
	case revoke && sessionIDPattern.MatchString(id):
		n := s.endSessions(func(h string) bool { return sessionID(h) == id })
		if n == 0 {
			failCode(w, 404, "session_unknown", nil, "このログインはもうありません")
			return
		}
		if sessionID(current) == id {
			s.clearCookie(w, r)
		}
		jsonResponse(w, 200, map[string]any{"ok": true, "revoked": n})
	default:
		fail(w, 404, "unknown route")
	}
}

var sessionIDPattern = regexp.MustCompile(`^[a-f0-9]{16}$`)

// endSessions ends the sessions whose hash matches and saves the store.
func (s *Server) endSessions(match func(string) bool) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for h := range s.sessions {
		if match(h) {
			s.endSessionLocked(h)
			n++
		}
	}
	if n > 0 {
		if e := s.saveSessionsLocked(); e != nil {
			noticef("session store: %v", english(e))
		}
	}
	return n
}

// clearCookie tells the browser to forget its session cookie.
func (s *Server) clearCookie(w http.ResponseWriter, r *http.Request) {
	secure := strings.HasPrefix(r.Header.Get("Origin"), "https://")
	http.SetCookie(w, &http.Cookie{Name: s.cookieName, Value: "", Path: "/", HttpOnly: true, Secure: secure, SameSite: http.SameSiteStrictMode, MaxAge: -1})
}
