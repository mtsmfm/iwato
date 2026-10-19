package main

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"iwato/internal/openshell/wslc"
)

// check is one of the startup checks the page shows (the settings, and a
// banner while the engine is unusable). Level: ok, info, warn (the owner
// should act) or fail (Iwato cannot work). Name, Detail and Hint are keys
// the page translates (web/i18n.js: checks.name.*, checks.detail.*,
// checks.hint.*), with Params filling their {slots} in both. Params hold
// values only (versions, paths, the engine's error), never secrets.
type check struct {
	Level  string         `json:"level"`
	Name   string         `json:"name"`
	Detail string         `json:"detail"`
	Hint   string         `json:"hint,omitempty"`
	Params map[string]any `json:"params,omitempty"`
}

// engineNames are the display names of the engine checks (the first
// check), for the coded error of operations refused without an engine.
var engineNames = map[string]string{"wslc": "WSLc", "docker": "Docker"}

// diagnose checks the engine (WSLc, or a Docker-compatible one:
// diagnoseDocker) and the configuration. The first check is the engine;
// the rest follow only when it is usable. It only reads.
func diagnose(ctx context.Context, e *Engine, configPath string, sandboxes int) []check {
	var out []check
	if e.docker {
		out = diagnoseDocker(ctx, e)
		if len(out) > 0 && out[0].Level == "fail" {
			return out
		}
	} else if ver, err := e.run(ctx, "--version"); err != nil {
		return []check{{Level: "fail", Name: "wslc", Detail: "wslc_missing", Hint: "wslc_install", Params: map[string]any{"error": cliLine(err)}}}
	} else {
		out = append(out, check{Level: "ok", Name: "wslc", Detail: "version", Params: map[string]any{"version": firstLine(string(ver))}})
	}
	path, _ := filepath.Abs(configPath)
	out = append(out,
		check{Level: "ok", Name: "config", Detail: "config", Params: map[string]any{"path": path, "count": sandboxes}},
		check{Level: "ok", Name: "openshell", Detail: "version", Params: map[string]any{"version": wslc.OpenShellVersion}})
	return out
}

// engineDown is the first check when it failed: the engine is unusable.
func engineDown(checks []check) *check {
	if len(checks) > 0 && checks[0].Level == "fail" {
		c := checks[0]
		return &c
	}
	return nil
}

// checkState keeps the last result of the startup checks. While the
// engine is unusable serve waits for it (waitEngine): the page shows the
// failed check, operations that need the engine are refused with
// engine_unavailable, and the checks run again every few seconds (and
// whenever the page asks). Once the engine passes, the preparation
// starts.
type checkState struct {
	run   func(ctx context.Context) []check
	runMu sync.Mutex // one diagnosis at a time

	mu    sync.Mutex
	last  []check
	at    time.Time
	ready chan struct{} // closed when the engine first passes
	once  sync.Once
}

func newCheckState(run func(context.Context) []check) *checkState {
	return &checkState{run: run, ready: make(chan struct{})}
}

// refresh runs the checks now.
func (c *checkState) refresh(ctx context.Context) ([]check, time.Time) {
	c.runMu.Lock()
	defer c.runMu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	checks := c.run(ctx)
	at := time.Now()
	c.mu.Lock()
	c.last, c.at = checks, at
	c.mu.Unlock()
	if engineDown(checks) == nil {
		c.once.Do(func() { close(c.ready) })
	}
	return checks, at
}

// engine is the failed engine check, or nil when the engine is usable (or
// there are no checks: tests).
func (c *checkState) engine() *check {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return engineDown(c.last)
}

// engineErr is the error of operations refused while the engine is
// unusable.
func (c *checkState) engineErr() error {
	if c == nil {
		return nil
	}
	down := c.engine()
	if down == nil {
		return nil
	}
	name := engineNames[down.Name]
	if name == "" {
		name = down.Name
	}
	return errCodeEn("engine_unavailable", map[string]any{"engine": name}, name+" を使えません。画面上部の案内を見てください", name+" is not usable (see the startup checks)", nil)
}

// waitEngine runs the checks again every while the engine is unusable,
// until ctx ends.
func (c *checkState) waitEngine(ctx context.Context, every time.Duration) {
	if c == nil {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		}
		if c.engine() != nil {
			c.refresh(ctx)
		}
	}
}

// whenReady runs f once the engine has passed (at once if it has), unless
// ctx ends first.
func (c *checkState) whenReady(ctx context.Context, f func()) {
	if c == nil {
		f()
		return
	}
	select {
	case <-c.ready:
		f()
	case <-ctx.Done():
	}
}

// pageCheck marks a run of the checks the page asked for (GET /api/checks).
type pageCheck struct{}

// handleChecks runs the checks again and returns them (GET /api/checks:
// the settings, and the banner's 「再確認」).
func (s *Server) handleChecks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		fail(w, 405, "method not allowed")
		return
	}
	if !s.require(w, r, false) {
		return
	}
	if s.checks == nil {
		jsonResponse(w, 200, map[string]any{"checks": []check{}})
		return
	}
	// Not canceled with the request: a page that goes away must not leave
	// a failed result behind.
	checks, at := s.checks.refresh(context.WithValue(context.WithoutCancel(r.Context()), pageCheck{}, true))
	jsonResponse(w, 200, map[string]any{"checks": checks, "at": at.UTC().Format(time.RFC3339)})
}

// needsEngine tells the requests refused while the engine is unusable:
// those that reach the engine or the gateway. The list, the checks, the
// session, the log and the settings stay available.
func needsEngine(r *http.Request) bool {
	p := r.URL.Path
	switch {
	case strings.HasPrefix(p, "/api/sandboxes/"), strings.HasPrefix(p, "/ws/"),
		p == "/api/volumes", strings.HasPrefix(p, "/api/volumes/"),
		p == "/api/providers", strings.HasPrefix(p, "/api/providers/"):
		return true
	case r.Method == http.MethodGet || r.Method == http.MethodHead:
		return false
	}
	return p == "/api/sandboxes" || p == "/api/images/update" || p == "/api/reset" ||
		p == "/api/provider-profiles" || strings.HasPrefix(p, "/api/provider-profiles/")
}

// cliLine is an engine error for a check's params: its first line, without
// what safeCLIError hides.
func cliLine(err error) string {
	return strings.TrimSuffix(firstLine(safeCLIError(err)), ":")
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(s)
}
