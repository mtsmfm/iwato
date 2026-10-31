package main

import (
	"bufio"
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"mime"
	"net"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/gorilla/websocket"

	"iwato/internal/dav"
	"iwato/internal/openshell/client"
	"iwato/internal/termsync"
)

// reject writes a JSON error and explains the rejection in the log (iwato.log,
// the page's log view, the console with -verbose) so the owner can diagnose
// "認証が必要です" without a browser debugger. Never pass tokens, cookies, or
// CSRF values to it.
func reject(w http.ResponseWriter, r *http.Request, code int, message, reason string) {
	log.Printf("rejected %s %s: %s (%s)", r.Method, r.URL.Path, message, reason)
	fail(w, code, message)
}

//go:embed web/* web/vendor/*
var webFiles embed.FS

// The manifest extension is not in every OS MIME table, and nosniff is set.
func init() { _ = mime.AddExtensionType(".webmanifest", "application/manifest+json") }

type authSession struct {
	csrf    string
	expires time.Time
	// created, used and agent (the browser's User-Agent) are for the
	// settings' list of logins; empty in sessions from older versions.
	created, used time.Time
	agent         string
	// ended closes when the session ends (sign-out, revoke, reset), which
	// closes its terminals.
	ended chan struct{}
}
type Server struct {
	config Config
	rt     Runtime
	// fake: serve -fake, the fake backend of the browser checks (fake_hooks.go).
	fake                                bool
	host, origin, bootstrap, cookieName string
	bootstrapUntil                      time.Time
	mu                                  sync.Mutex
	sessions                            map[string]authSession
	ops                                 map[string]*sandboxOp     // start, stop or delete in progress, by sandbox
	policyChanges                       map[string][]PolicyChange // revisions made in Iwato, until a while after loaded
	connections                         map[string]int
	ctx                                 context.Context
	// publicOrigin is the optional https origin of `tailscale serve` in front
	// of the loopback listener (for phones on the tailnet).
	publicHost, publicOrigin string
	sessionTTL               time.Duration
	push                     *PushStore
	configPath               string      // iwato.json (empty: in-memory only)
	sessionPath              string      // sessions.json (empty: in-memory only)
	configMu                 sync.Mutex  // serializes sandbox list updates
	providerMu               sync.Mutex  // serializes provider changes and their checks
	policyLocks              policyLocks // one per sandbox: Iwato's policy writes (policy_lock.go)
	oauth                    oauthFlows  // the Atlassian OAuth flow (atlassian_flow.go)
	xai, codex               deviceFlows // the device code sign-ins (device_flow.go)
	claude                   claudeFlows // the Claude sign-in (claude_oauth.go)
	checks                   *checkState // the startup checks (checks.go); nil in tests
	images                   *imageManager
	retryImages              func()
	imageCleanup             func()
	imageUpdates             *imageUpdates // 「標準イメージを更新」 (image_update.go)
	update                   *selfUpdate   // updating Iwato itself (update.go); nil without a channel
	logs                     logRing
	presence                 presence  // pages open and in use (presence.go)
	notifier                 *notifier // nil without Web Push (main.go)
	files                    *dav.Pool // the sandboxes' files over WebDAV (files.go)
	davKeys                  *davKeys
	editorLists              editorLists      // the editor's quick open lists (editor.go)
	ingress                  *ingressManager  // sandbox ports exposed (ingress.go)
	resets                   resetJob         // 「すべて消す」 (reset.go)
	autoStop                 autoStops        // stopping once the terminals end (autostop.go)
	resources                *resourceMonitor // CPU, memory, GPU and home sizes (resources.go)
	hostChecks               *hostChecker     // the approval's host checks (host_check.go)
	hostChecksOnce           sync.Once

	// flowCtx is the OAuth flows' life until a reset (oauthLife).
	flowCtx context.Context
	flowEnd context.CancelFunc
}

func newServer(c Config, rt Runtime, fake bool, host string) *Server {
	s := &Server{config: c, rt: rt, fake: fake && fakeHooks != nil, host: host, origin: "http://" + host, bootstrap: randomHex(32), bootstrapUntil: time.Now().Add(10 * time.Minute), cookieName: "iwato_" + randomHex(8), sessions: map[string]authSession{}, ops: map[string]*sandboxOp{}, connections: map[string]int{}, ctx: context.Background(), sessionTTL: 8 * time.Hour, davKeys: &davKeys{}}
	s.files = s.newFilesPool()
	s.ingress = newIngressManager(s, "")
	s.resources = newResourceMonitor(s, "")
	s.watchTerminalEnds()
	return s
}
func equal(a, b string) bool {
	return len(a) == len(b) && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
func jsonResponse(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, code int, message string) {
	jsonResponse(w, code, map[string]string{"error": message})
}
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	return decodeLimit(w, r, v, 16<<10)
}

func decodeLimit(w http.ResponseWriter, r *http.Request, v any, limit int64) error {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	defer r.Body.Close()
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		return e
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}
func (s *Server) session(r *http.Request) (authSession, bool) {
	_, a, ok := s.currentSession(r)
	return a, ok
}

// currentSession is the request's session and its hash; it records its use.
func (s *Server) currentSession(r *http.Request) (string, authSession, bool) {
	c, e := r.Cookie(s.cookieName)
	if e != nil {
		return "", authSession{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	h := sessionHash(c.Value)
	a, ok := s.sessions[h]
	if !ok {
		return "", authSession{}, false
	}
	now := time.Now()
	if now.After(a.expires) {
		s.endSessionLocked(h)
		_ = s.saveSessionsLocked()
		return "", authSession{}, false
	}
	if now.Sub(a.used) >= sessionUseStep {
		a.used = now
		s.sessions[h] = a
		_ = s.saveSessionsLocked()
	}
	return h, a, true
}
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/session", s.handleSession)
	mux.HandleFunc("/api/sessions", s.handleSessions)
	mux.HandleFunc("/api/sessions/", s.handleSessions)
	mux.HandleFunc("/api/sandboxes", s.handleList)
	mux.HandleFunc("/api/sandboxes/", s.handleSandbox)
	mux.HandleFunc("/api/pair", s.handlePair)
	mux.HandleFunc("/api/images/retry", s.handleImagesRetry)
	mux.HandleFunc("/api/images", s.handleImages)
	mux.HandleFunc("/api/images/update", s.handleImages)
	mux.HandleFunc("/api/update", s.handleUpdate)
	mux.HandleFunc("/api/update/", s.handleUpdate)
	mux.HandleFunc("/api/log", s.handleLog)
	mux.HandleFunc("/api/checks", s.handleChecks)
	mux.HandleFunc("/api/volumes", s.handleVolumes)
	mux.HandleFunc("/api/volumes/", s.handleVolumes)
	mux.HandleFunc("/api/presets", s.handlePresets)
	mux.HandleFunc("/api/presets/user", s.handleUserPresets)
	mux.HandleFunc("/api/presets/user/", s.handleUserPresets)
	mux.HandleFunc("/api/providers", s.handleProviders)
	mux.HandleFunc("/api/providers/", s.handleProviders)
	mux.HandleFunc("/api/provider-profiles", s.handleProviderProfile)
	mux.HandleFunc("/api/provider-profiles/", s.handleProviderProfile)
	mux.HandleFunc("/api/oauth/atlassian", s.handleOAuth)
	mux.HandleFunc("/api/oauth/atlassian/", s.handleOAuth)
	mux.HandleFunc("/api/oauth/xai/", s.handleXAIOAuth)
	mux.HandleFunc("/api/oauth/codex/", s.handleCodexOAuth)
	mux.HandleFunc("/api/oauth/claude/", s.handleClaudeOAuth)
	if s.fake {
		fakeHooks.routes(s, mux)
	}
	mux.HandleFunc("/api/ingress", s.handleIngress)
	mux.HandleFunc("/api/ingress/", s.handleIngress)
	mux.HandleFunc("/api/files", s.handleFiles)
	mux.HandleFunc("/api/files/", s.handleFiles)
	mux.HandleFunc("/api/push", s.handlePush)
	mux.HandleFunc("/api/push/", s.handlePush)
	mux.HandleFunc("/api/presence", s.handlePresence)
	mux.HandleFunc("/api/reset", s.handleReset)
	mux.HandleFunc("/api/timezone", s.handleTimezone)
	mux.HandleFunc("/api/hostcheck", s.handleHostCheck)
	mux.HandleFunc("/api/resources", s.handleResources)
	mux.HandleFunc("/api/resources/", s.handleResources)
	mux.HandleFunc("/ws/", s.handleWS)
	// Turns on the page's debug option (?font) in debug builds only.
	mux.HandleFunc("/debug.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		if debugBuild() {
			fmt.Fprint(w, "window.IwatoDebug = true;\n")
		}
	})
	files, _ := fs.Sub(webFiles, "web")
	static := http.FileServer(http.FS(files))
	mux.Handle("/", static)
	// Font file names carry their version, so a browser keeps them instead of
	// downloading megabytes on every page load. Missing files stay uncached.
	mux.Handle("/vendor/fonts/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := fs.Stat(files, strings.TrimPrefix(r.URL.Path, "/")); err == nil {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		static.ServeHTTP(w, r)
	}))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; connect-src 'self'; img-src 'self' data: blob:; worker-src 'self'; manifest-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
		// WebDAV clients send no Origin: /dav/ checks the Host and the
		// rest itself (files.go). Windows also asks OPTIONS of the root.
		if davPath(r.URL.Path) || (r.Method == http.MethodOptions && r.URL.Path == "/" && r.Header.Get("Origin") == "") {
			s.handleDAV(w, r)
			return
		}
		// Reject DNS rebinding and cross-origin requests before any routing/authentication.
		if r.Host != s.host && (s.publicHost == "" || r.Host != s.publicHost) {
			reject(w, r, 403, "invalid host", "Host="+r.Host+" expected="+s.host)
			return
		}
		if o := r.Header.Get("Origin"); o != "" && !s.allowedOrigin(o) {
			reject(w, r, 403, "invalid origin", "Origin="+o+" expected="+s.origin)
			return
		}
		// Opening the page from another app (a phone's QR scanner, a chat link) is a
		// cross-site top-level navigation; only that is allowed in from elsewhere.
		// The page has no effect by itself, frames are refused (CSP), the cookie
		// is SameSite=Strict and every API and WebSocket stays same-origin only.
		if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" && !documentNavigation(r) {
			reject(w, r, 403, "cross-site request blocked", "Sec-Fetch-Site="+site)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && !s.allowedOrigin(r.Header.Get("Origin")) {
			reject(w, r, 403, "same-origin request required", "Origin header missing on "+r.Method)
			return
		}
		if needsEngine(r) {
			if err := s.checks.engineErr(); err != nil {
				if s.require(w, r, false) {
					failErr(w, 409, err)
				}
				return
			}
		}
		mux.ServeHTTP(w, r)
	})
}
func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		a, ok := s.session(r)
		if !ok {
			reason := "no session cookie; the URL was opened without #token, the session expired or sessions.json was deleted"
			if _, e := r.Cookie(s.cookieName); e == nil {
				reason = "session cookie unknown or expired"
			}
			rejectCode(w, r, 401, "session_missing", "セッションがありません。Iwato が表示した URL を開き直してください。", reason)
			return
		}
		jsonResponse(w, 200, map[string]string{"csrf": a.csrf})
	case http.MethodPost:
		var body struct {
			Token string `json:"token"`
		}
		if decode(w, r, &body) != nil {
			reject(w, r, 400, "invalid login request", "body is not {\"token\":...}")
			return
		}
		s.mu.Lock()
		if s.bootstrap == "" || time.Now().After(s.bootstrapUntil) || !equal(body.Token, s.bootstrap) {
			reason := "token does not match this process"
			if s.bootstrap == "" {
				reason = "launch token already used once (browser preload/prerender or a second tab?)"
			} else if time.Now().After(s.bootstrapUntil) {
				reason = "launch token expired (10 minutes)"
			}
			s.mu.Unlock()
			rejectCode(w, r, 401, "launch_token_invalid", "起動 URL が無効か使用済みです。Iwato を起動し直してください。", reason)
			return
		}
		s.bootstrap = ""
		key := randomHex(32)
		now := time.Now()
		a := authSession{csrf: randomHex(32), expires: now.Add(s.sessionTTL), created: now, used: now, agent: userAgent(r), ended: make(chan struct{})}
		s.sessions[sessionHash(key)] = a
		if e := s.saveSessionsLocked(); e != nil {
			noticef("session store: %v", english(e))
		}
		s.mu.Unlock()
		secure := strings.HasPrefix(r.Header.Get("Origin"), "https://")
		http.SetCookie(w, &http.Cookie{Name: s.cookieName, Value: key, Path: "/", HttpOnly: true, Secure: secure, SameSite: http.SameSiteStrictMode, MaxAge: int(s.sessionTTL / time.Second)})
		jsonResponse(w, 200, map[string]string{"csrf": a.csrf})
	default:
		w.Header().Set("Allow", "GET, POST")
		fail(w, 405, "method not allowed")
	}
}
func (s *Server) require(w http.ResponseWriter, r *http.Request, csrf bool) bool {
	a, ok := s.session(r)
	if !ok {
		failCode(w, 401, "session_expired", nil, "セッション切れ。Iwato を起動し直し、起動 URL を開いてください。")
		return false
	}
	if csrf && !equal(r.Header.Get("X-CSRF-Token"), a.csrf) {
		fail(w, 403, "invalid CSRF token")
		return false
	}
	return true
}
func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		s.handleCreateSandbox(w, r)
		return
	}
	if r.Method != http.MethodGet {
		fail(w, 405, "method not allowed")
		return
	}
	if !s.require(w, r, false) {
		return
	}
	ctx, cancel := timeout(r.Context(), 15*time.Second)
	defer cancel()
	sandboxes := s.sandboxList()
	list := make([]Status, len(sandboxes))
	var wg sync.WaitGroup
	for i, sb := range sandboxes {
		wg.Add(1)
		go func(i int, sb Sandbox) {
			defer wg.Done()
			list[i] = s.rt.Status(ctx, sb)
			// A start in progress shows as one to every page, until it ends;
			// so does a recreate.
			switch s.opKind(sb.Name) {
			case "start":
				if list[i].State != "running" {
					list[i].State = "starting"
				}
			case "recreate":
				if list[i].State != "running" {
					list[i].State, list[i].Detail = "starting", "作り直しています…"
				}
			}
			list[i].GPU = sb.GPU
			list[i].Stopped = s.notifier.stopped(sb.Name)
			list[i].RestartPending = list[i].State == "running" && s.restartPending(sb)
			s.autoStopStatus(&list[i], sb)
			list[i].Pending = s.pendingInList(ctx, sb, list[i].State)
		}(i, sb)
	}
	wg.Wait()
	s.ingress.observe(list)
	s.resources.observe(list)
	images, _ := s.imagesReady()
	body := map[string]any{"fake": s.fake, "sandboxes": list, "images": images, "server": currentBuild()}
	if s.update != nil {
		b := s.update.currentBuild()
		body["server"] = b
		if m := s.update.available(); m != nil && assetName(runtime.GOOS, runtime.GOARCH) != "" {
			body["update"] = UpdateBuild{Version: m.Version, Commit: m.Commit}
		}
	}
	if down := s.checks.engine(); down != nil {
		body["engine"] = down
	} else {
		body["gpu"] = s.gpuAvailable(ctx)
	}
	jsonResponse(w, 200, body)
}

// pendingInList reads a running sandbox's pending proposals, the agent's
// only (as the badges count them). A sandbox that is not running gets no
// new ones: nil, and the page keeps what it last read.
func (s *Server) pendingInList(ctx context.Context, sb Sandbox, state string) *[]PendingProposal {
	rt, ok := s.rt.(pendingRuntime)
	if !ok || state != "running" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	all, err := rt.PendingProposals(ctx, sb)
	if err != nil {
		return nil
	}
	agent := []PendingProposal{}
	for _, p := range all {
		if p.Source == "agent" {
			agent = append(agent, p)
		}
	}
	return &agent
}

// sandboxOp is a start, stop or delete in progress on one sandbox. Its
// context ends when it is canceled (a stop or delete cancels a start).
type sandboxOp struct {
	kind   string
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	err    error
}

// opBusy is the answer to an operation while another one runs.
var opBusy = map[string]string{"start": "起動処理が進行中です", "stop": "停止処理が進行中です", "delete": "削除処理が進行中です", "recreate": "作り直しの処理が進行中です"}

// opStuck is the answer when a start in progress would not stop.
const opStuck = "起動処理を止められませんでした"

// busyCode is the page's error code for beginOp's busy text ("" when it
// has none).
func busyCode(busy string) string {
	if busy == opStuck {
		return "busy_start_stuck"
	}
	for kind, text := range opBusy {
		if busy == text {
			return "busy_" + kind
		}
	}
	return ""
}

// failBusy answers 409 with beginOp's busy text, coded for the page.
func failBusy(w http.ResponseWriter, busy string) {
	if code := busyCode(busy); code != "" {
		failCode(w, 409, code, nil, busy)
		return
	}
	fail(w, 409, busy)
}

// opTimeout bounds an operation: a recreate deletes (up to 2 minutes),
// starts (up to 5) and puts the rules back.
func opTimeout(kind string) time.Duration {
	if kind == "recreate" {
		return 10 * time.Minute
	}
	return 5 * time.Minute
}

func (s *Server) opKind(name string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if op := s.ops[name]; op != nil {
		return op.kind
	}
	return ""
}

// beginOp claims the sandbox for kind. A start finds a start in progress
// as joined (the caller waits for its result). A stop or delete cancels a
// start in progress first, so the owner need not wait it out, and reports
// interrupted. busy is set when another operation holds the sandbox.
func (s *Server) beginOp(name, kind string) (op, joined *sandboxOp, interrupted bool, busy string) {
	for {
		s.mu.Lock()
		cur := s.ops[name]
		if cur == nil {
			ctx, cancel := timeout(s.ctx, opTimeout(kind))
			op = &sandboxOp{kind: kind, ctx: ctx, cancel: cancel, done: make(chan struct{})}
			s.ops[name] = op
			s.mu.Unlock()
			// A check armed before this operation does not apply after it.
			s.autoStop.cancel(name)
			return op, nil, interrupted, ""
		}
		s.mu.Unlock()
		switch {
		case kind == "start" && cur.kind == "start":
			return nil, cur, false, ""
		case kind == "start" || cur.kind != "start":
			return nil, nil, false, opBusy[cur.kind]
		}
		cur.cancel()
		interrupted = true
		select {
		case <-cur.done:
		case <-time.After(30 * time.Second):
			return nil, nil, true, opStuck
		}
	}
}

// endOp records the operation's result for those who joined it.
func (s *Server) endOp(name string, op *sandboxOp, err error) {
	op.err = err
	op.cancel()
	s.mu.Lock()
	if s.ops[name] == op {
		delete(s.ops, name)
	}
	s.mu.Unlock()
	s.filesChanged(name)
	s.ingress.kick()
	close(op.done)
}

// handleStart creates or starts the sandbox and waits until it is ready.
// A second start while one is in progress waits for that one's result.
func (s *Server) handleStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		fail(w, 405, "method not allowed")
		return
	}
	if !s.require(w, r, true) || !s.requireImages(w) {
		return
	}
	tail := strings.TrimPrefix(r.URL.Path, "/api/sandboxes/")
	name, ok := strings.CutSuffix(tail, "/start")
	sb, found := s.lookupSandbox(name)
	if !ok || !found {
		fail(w, 404, "unknown sandbox")
		return
	}
	op, joined, _, busy := s.beginOp(name, "start")
	if busy != "" {
		failBusy(w, busy)
		return
	}
	if joined != nil {
		select {
		case <-joined.done:
		case <-r.Context().Done():
			return
		}
		if joined.err != nil {
			fail(w, 500, safeCLIError(joined.err))
			return
		}
		jsonResponse(w, 200, map[string]bool{"ok": true})
		return
	}
	// Keep a bounded create/start alive across browser navigation/disconnect.
	ctx := op.ctx
	var err error
	defer func() { s.endOp(name, op, err) }()
	// A sandbox that was not running starts with its current providers.
	wasRunning := !sb.ProvidersChangedAt.IsZero() && s.rt.Status(ctx, sb).State == "running"
	// A recreate that stopped before the create: this start makes it.
	fresh := false
	if ex, ok := s.rt.(recreateRuntime); ok && sb.Carry != nil {
		made, eerr := ex.Exists(ctx, sb)
		fresh = eerr == nil && !made
	}
	if err = s.rt.Start(ctx, sb); err != nil {
		fail(w, 500, safeCLIError(err))
		return
	}
	if !sb.ProvidersChangedAt.IsZero() && !wasRunning {
		s.clearProvidersChanged(sb.Name)
	}
	// After a recreate that stopped short: its rules go in now.
	var failed []CarryFailure
	if sb.Carry != nil {
		var cerr error
		if failed, cerr = s.applyCarried(ctx, sb.Name, fresh); cerr != nil {
			log.Printf("sandbox %s: carried rules: %v", sb.Name, english(cerr))
		}
	}
	s.cleanupImages()
	jsonResponse(w, 200, map[string]any{"ok": true, "failed": failed})
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		fail(w, 405, "method not allowed")
		return
	}
	a, ok := s.session(r)
	if !ok {
		fail(w, 401, "unauthenticated")
		return
	}
	if !s.allowedOrigin(r.Header.Get("Origin")) || !equal(r.URL.Query().Get("csrf"), a.csrf) {
		fail(w, 403, "invalid websocket origin/token")
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/ws/")
	limit := 4
	sb, found := s.lookupSandbox(name)
	if !found {
		fail(w, 404, "unknown sandbox")
		return
	}
	ref := TermRef{Name: r.URL.Query().Get("term"), New: r.URL.Query().Get("new") == "1"}
	if ref.Name == "" {
		ref.Name = mainTerm
	}
	if !validTermName(ref.Name) {
		fail(w, 400, "invalid terminal name")
		return
	}
	s.mu.Lock()
	if s.connections[name] >= limit {
		s.mu.Unlock()
		fail(w, 429, "too many terminals for this sandbox")
		return
	}
	s.connections[name]++
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.connections[name]--; s.mu.Unlock() }()
	u := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return s.allowedOrigin(r.Header.Get("Origin")) }, ReadBufferSize: 4096, WriteBufferSize: 4096, HandshakeTimeout: 10 * time.Second, EnableCompression: true}
	conn, e := u.Upgrade(w, r, nil)
	if e != nil {
		return
	}
	defer conn.Close()
	s.serveTerm(conn, name, a.expires, a.ended, func(ctx context.Context, cols, rows int) (Terminal, error) {
		// A terminal is wanted: a pending automatic stop (autostop.go) is
		// off, and one already decided ends before the attach.
		s.autoStop.cancel(name)
		return s.rt.Attach(ctx, sb, ref, cols, rows)
	})
}

// serveTerm relays the terminal (internal/termsync) between the browser and
// the session: each WebSocket binary message is a termsync message (its
// kind byte, then the payload). The browser's hello comes first and sizes
// the attach. A text message ends the connection.
func (s *Server) serveTerm(conn *websocket.Conn, name string, expires time.Time, signedOut <-chan struct{}, attach func(context.Context, int, int) (Terminal, error)) {
	closeWith := func(code int, text string) {
		_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(code, text), time.Now().Add(time.Second))
	}
	conn.SetReadLimit(termsync.MaxMessage + 1)
	_ = conn.SetReadDeadline(time.Now().Add(20 * time.Second))
	kind, hello, e := conn.ReadMessage()
	if e != nil || kind != websocket.BinaryMessage || len(hello) == 0 || hello[0] != termsync.KindHello {
		closeWith(websocket.ClosePolicyViolation, "hello expected")
		return
	}
	cols, rows, e := termsync.ParseHello(hello[1:])
	if e != nil {
		closeWith(websocket.ClosePolicyViolation, "unsupported hello")
		return
	}
	if cols == 0 {
		// A page whose terminal is not laid out: a session the attach
		// starts gets a usual size until the page sends its own.
		cols, rows = 80, 24
	}
	ctx, cancel := timeout(s.ctx, 20*time.Second)
	term, e := attach(ctx, cols, rows)
	cancel()
	if e != nil {
		log.Printf("terminal %s: cannot attach: %v", name, english(e))
		// The reason goes to the page in the close frame.
		closeWith(4500, closeReason(e))
		return
	}
	// ended closes the page's connection: normally when the process ended,
	// with the reason (as a failed attach) when the session broke, and as
	// one to retry when only the connection to the sandbox ended (the page
	// attaches again, which reconnects the main process).
	ended := func(t Terminal) {
		if p, ok := t.(interface{ Err() error }); ok {
			if e := p.Err(); errors.Is(e, client.ErrRelayLost) {
				closeWith(websocket.CloseTryAgainLater, "connection to the sandbox lost")
				return
			} else if e != nil && !errors.Is(e, io.EOF) {
				log.Printf("terminal %s: %v", name, english(e))
				closeWith(4500, closeReason(e))
				return
			}
		}
		closeWith(websocket.CloseNormalClosure, "terminal exited")
	}
	done := make(chan struct{})
	defer func() { close(done); _ = term.Close() }()
	var writeMu sync.Mutex
	toTerm := func(kind byte, p []byte) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return termsync.WriteMessage(term, kind, p)
	}
	if e = toTerm(termsync.KindHello, hello[1:]); e != nil {
		return
	}
	output := make(chan []byte, 8)
	terminalErr := make(chan error, 1)
	go func() {
		r := bufio.NewReaderSize(term, 64<<10)
		for {
			kind, p, e := termsync.ReadMessage(r)
			if e != nil {
				terminalErr <- e
				return
			}
			select {
			case output <- append([]byte{kind}, p...):
			case <-done:
				return
			}
		}
	}()
	_ = conn.SetReadDeadline(time.Now().Add(70 * time.Second))
	conn.SetPongHandler(func(string) error { return conn.SetReadDeadline(time.Now().Add(70 * time.Second)) })
	readErr := make(chan error, 1)
	// Pongs go out from the loop below (one writer), ahead of the session.
	pongs := make(chan []byte, 1)
	go func() {
		for {
			kind, p, e := conn.ReadMessage()
			if e != nil {
				readErr <- e
				return
			}
			if kind != websocket.BinaryMessage || len(p) == 0 {
				readErr <- errBadMessage
				return
			}
			switch p[0] {
			case termsync.KindInput, termsync.KindResize, termsync.KindAck, termsync.KindHistory:
			case termsync.KindPing:
				if len(p) > 9 {
					readErr <- errBadMessage
					return
				}
				select {
				case pongs <- append([]byte{termsync.KindPong}, p[1:]...):
				default:
				}
				continue
			default:
				readErr <- errBadMessage
				return
			}
			if e = toTerm(p[0], p[1:]); e != nil {
				readErr <- e
				return
			}
		}
	}()
	// A process can exit with its output still open (ConPTY on Windows):
	// the session says so instead of ending its output.
	var exited <-chan struct{}
	if p, ok := term.(interface{ Exited() <-chan struct{} }); ok {
		exited = p.Exited()
	}
	// flush sends the output already read, and with wait the output that
	// comes until then.
	flush := func(wait <-chan time.Time) {
		for {
			var p []byte
			if wait == nil {
				select {
				case p = <-output:
				default:
					return
				}
			} else {
				select {
				case p = <-output:
				case <-wait:
					return
				}
			}
			_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if conn.WriteMessage(websocket.BinaryMessage, p) != nil {
				return
			}
		}
	}
	tick := time.NewTicker(25 * time.Second)
	defer tick.Stop()
	for {
		select {
		case p := <-output:
			_ = conn.SetWriteDeadline(time.Now().Add(15 * time.Second))
			if conn.WriteMessage(websocket.BinaryMessage, p) != nil {
				return
			}
		case p := <-pongs:
			_ = conn.SetWriteDeadline(time.Now().Add(15 * time.Second))
			if conn.WriteMessage(websocket.BinaryMessage, p) != nil {
				return
			}
		case <-tick.C:
			if time.Now().After(expires) {
				closeWith(4401, "session expired")
				return
			}
			if conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(10*time.Second)) != nil {
				return
			}
		case e := <-readErr:
			// Only a page that breaks the protocol is refused; a read that
			// failed (no pong while a phone slept) is one the page retries.
			if errors.Is(e, errBadMessage) {
				closeWith(websocket.ClosePolicyViolation, "connection/input closed")
			} else {
				closeWith(websocket.CloseInternalServerErr, "connection/input closed")
			}
			return
		case e := <-terminalErr:
			log.Printf("terminal %s: the session ended (%v)", name, english(e))
			// Output read before the end still goes out.
			flush(nil)
			ended(term)
			return
		case <-exited:
			// The process's last output (e.g. a login result) goes out first.
			flush(time.After(300 * time.Millisecond))
			ended(term)
			return
		case <-signedOut:
			closeWith(4401, "signed out")
			return
		case <-s.ctx.Done():
			return
		}
	}
}

// errBadMessage is a message from the page that the protocol does not have.
var errBadMessage = errors.New("unknown message")

// closeReason is an error for a WebSocket close frame (at most 123 bytes).
func closeReason(e error) string {
	reason := e.Error()
	for len(reason) > 120 {
		_, size := utf8.DecodeLastRuneInString(reason)
		reason = reason[:len(reason)-size]
	}
	return reason
}

// defaultPort is where Iwato serves its page unless -listen says otherwise;
// when it is in use, the next free one up to portTries on. 17600-17669 is
// unassigned at IANA and stays below OpenShell's gateway (17670) and the
// WSLc driver (17671).
const (
	defaultPort = 17600
	portTries   = 70
)

func listenLocal(addr string) (net.Listener, error) {
	if addr == "" {
		var err error
		for p := defaultPort; p < defaultPort+portTries; p++ {
			var ln net.Listener
			if ln, err = net.Listen("tcp4", "127.0.0.1:"+strconv.Itoa(p)); err == nil {
				if p != defaultPort {
					noticef("port %d is in use; serving on %d", defaultPort, p)
				}
				return ln, nil
			}
		}
		return nil, fmt.Errorf("no free port from %d to %d: %w", defaultPort, defaultPort+portTries-1, err)
	}
	host, _, e := net.SplitHostPort(addr)
	if e != nil || host != "127.0.0.1" {
		return nil, fmt.Errorf("listen must be 127.0.0.1:PORT; remote management binding is deliberately unsupported")
	}
	return net.Listen("tcp4", addr)
}

// listenLocalWait retries addr for up to wait (a restarted exe takes the
// port as the old process lets it go).
func listenLocalWait(addr string, wait time.Duration) (net.Listener, error) {
	deadline := time.Now().Add(wait)
	for {
		ln, err := listenLocal(addr)
		if err == nil || time.Now().After(deadline) {
			return ln, err
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// documentNavigation is a top-level GET of the page itself (not an API call,
// WebSocket, subresource or frame).
func documentNavigation(r *http.Request) bool {
	return (r.Method == http.MethodGet || r.Method == http.MethodHead) &&
		r.Header.Get("Sec-Fetch-Mode") == "navigate" && r.Header.Get("Sec-Fetch-Dest") == "document" &&
		!strings.HasPrefix(r.URL.Path, "/api/") && !strings.HasPrefix(r.URL.Path, "/ws/")
}
