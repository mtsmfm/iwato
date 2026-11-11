package main

import (
	"net/http"
	"sync"
	"sync/atomic"
)

// autostarter starts Iwato when the owner signs in (Windows: the Run key,
// autostart_windows.go). Server.autostart is nil where there is none.
type autostarter interface {
	Enabled() (bool, error)
	Set(on bool) error
}

// autostartChanged tells the tray's check mark (app_windows.go).
var autostartChanged atomic.Pointer[func(bool)]

func notifyAutostart(on bool) {
	if f := autostartChanged.Load(); f != nil {
		(*f)(on)
	}
}

// handleAutostart: GET says whether Iwato starts at sign-in, POST
// {"enabled": bool} turns it on or off.
func (s *Server) handleAutostart(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if !s.require(w, r, false) {
			return
		}
		if s.autostart == nil {
			jsonResponse(w, 200, map[string]bool{"supported": false})
			return
		}
		on, err := s.autostart.Enabled()
		if err != nil {
			failCode(w, 500, "autostart_failed", map[string]any{"detail": err.Error()}, "自動起動の設定を読めません: "+err.Error())
			return
		}
		jsonResponse(w, 200, map[string]bool{"supported": true, "enabled": on})
	case http.MethodPost:
		if !s.require(w, r, true) {
			return
		}
		var body struct {
			Enabled bool `json:"enabled"`
		}
		if decode(w, r, &body) != nil {
			fail(w, 400, "invalid request")
			return
		}
		if s.autostart == nil {
			fail(w, 404, "no autostart here")
			return
		}
		if err := s.autostart.Set(body.Enabled); err != nil {
			failCode(w, 500, "autostart_failed", map[string]any{"detail": err.Error()}, "自動起動の設定を変えられません: "+err.Error())
			return
		}
		notifyAutostart(body.Enabled)
		jsonResponse(w, 200, map[string]bool{"ok": true})
	default:
		fail(w, 405, "method not allowed")
	}
}

// memoryAutostart keeps the setting in memory (the fake backend, tests).
type memoryAutostart struct {
	mu sync.Mutex
	on bool
}

func (m *memoryAutostart) Enabled() (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.on, nil
}

func (m *memoryAutostart) Set(on bool) error {
	m.mu.Lock()
	m.on = on
	m.mu.Unlock()
	return nil
}
