package main

import (
	"maps"
	"net/http"
	"regexp"
	"sync"
	"time"
)

// Pushes go out only while no device has Iwato open and in use (as Slack's
// do): the owner who is using it sees the page title and the marks there.
// Each page reports itself (POST /api/presence, web/app.js): in use when the
// owner touches it (at most every presenceGrace) or shows it, not in use
// when it is hidden. A page counts as in use for presenceActive after its
// last report that it was, plus presenceGrace for the touches it has not
// reported yet; a page that vanished (a closed tab) expires by itself.
const (
	presenceActive = 5 * time.Minute
	presenceGrace  = 30 * time.Second
	presencePages  = 64 // pages kept; the oldest report goes first
)

var presencePagePattern = regexp.MustCompile(`^[0-9a-f]{16,64}$`)

type presence struct {
	mu    sync.Mutex
	now   func() time.Time     // nil: time.Now
	pages map[string]time.Time // page id → its last report of being in use
	uses  uint64               // reports of being in use so far (used)
}

func (p *presence) clock() time.Time {
	if p.now != nil {
		return p.now()
	}
	return time.Now()
}

func (p *presence) report(page string, active bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.clock()
	if p.pages == nil {
		p.pages = map[string]time.Time{}
	}
	maps.DeleteFunc(p.pages, func(_ string, at time.Time) bool { return now.Sub(at) >= presenceActive+presenceGrace })
	if !active {
		delete(p.pages, page)
		return
	}
	if _, ok := p.pages[page]; !ok && len(p.pages) >= presencePages {
		oldest := ""
		for id, at := range p.pages {
			if oldest == "" || at.Before(p.pages[oldest]) {
				oldest = id
			}
		}
		delete(p.pages, oldest)
	}
	p.pages[page] = now
	p.uses++
}

// used counts the reports of being in use so far: a change between two
// reads tells that a page was in use in between, though perhaps no more.
func (p *presence) used() uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.uses
}

// active reports whether some page is open and in use.
func (p *presence) active() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.clock()
	for _, at := range p.pages {
		if now.Sub(at) < presenceActive+presenceGrace {
			return true
		}
	}
	return false
}

func (s *Server) handlePresence(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		fail(w, 405, "method not allowed")
		return
	}
	if !s.require(w, r, true) {
		return
	}
	var body struct {
		Page   string `json:"page"`
		Active bool   `json:"active"`
	}
	if decode(w, r, &body) != nil || !presencePagePattern.MatchString(body.Page) {
		fail(w, 400, "invalid presence report")
		return
	}
	s.presence.report(body.Page, body.Active)
	jsonResponse(w, 200, map[string]any{"ok": true})
}
