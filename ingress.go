package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"iwato/internal/ingress"
	"iwato/internal/ingress/local"
	"iwato/internal/ingress/tailscale"
	"iwato/internal/openshell/client"
	"iwato/internal/protect"
)

// ingressRuntime is what the ingress needs of the runtime: connections to
// a sandbox's loopback and the ports it listens on (for the page).
type ingressRuntime interface {
	DialPort(ctx context.Context, sandbox, host string, port uint16) (net.Conn, error)
	ListeningPorts(ctx context.Context, w Sandbox) ([]ingress.ListenPort, error)
	// ForgetPorts drops what the runtime keeps for a sandbox's
	// connections (session tokens) once it stopped or was deleted.
	ForgetPorts(sandbox string)
}

// ingressSecretFile holds the Tailscale secret next to the configuration
// (DPAPI on Windows); IWATO_TS_AUTHKEY overrides it.
const (
	ingressSecretFile = "ingress-secret.bin"
	ingressSecretEnv  = "IWATO_TS_AUTHKEY"
)

// ingressManager runs the ingress inside Iwato: the core (auth, protocol,
// ForwardTcp), the provider made from the configuration and the secret,
// and its Sync with the sandboxes' states.
type ingressManager struct {
	s   *Server
	rt  ingressRuntime
	dir string // the configuration's folder ("": nothing on disk)
	// newProvider makes the tailscale provider (the fake backend's stand-in);
	// newLocal the local one.
	newProvider func(cfg *IngressConfig, secret func() string) ingress.Provider
	newLocal    func() ingress.Provider

	core    *ingress.Server
	backend *ingress.Backend

	// later runs settings changes after the response (tests: at once).
	later func(func())
	// reconfMu serializes reconfigure.
	reconfMu sync.Mutex

	mu          sync.Mutex
	memSecret   string // without a folder (fake, tests)
	provider    ingress.Provider
	providerKey string
	running     map[string]bool
	lastSync    string
	syncErr     string
	kickCh      chan struct{}
	ownerCache  []string
	ownerAt     time.Time
	listeners   map[string]*listenRead // by sandbox
}

// listenRead is a sandbox's last read of its listening ports (done once
// one ended) and the read under way (running, closed when it ends).
type listenRead struct {
	done    bool
	at      time.Time
	ports   []ingress.ListenPort
	err     error
	running chan struct{}
}

// listenTTL is how long a sandbox's listening ports are reused: the
// connections a browser opens at once (and a port scan) share one exec.
const listenTTL = 3 * time.Second

func newIngressManager(s *Server, dir string) *ingressManager {
	m := &ingressManager{s: s, dir: dir, running: map[string]bool{}, listeners: map[string]*listenRead{}, kickCh: make(chan struct{}, 1), later: func(f func()) { go f() }}
	m.rt, _ = s.rt.(ingressRuntime)
	m.backend = &ingress.Backend{
		Dial: func(ctx context.Context, sandbox, host string, port uint16) (net.Conn, error) {
			if m.rt == nil {
				return nil, ingress.ErrStopped
			}
			return m.rt.DialPort(ctx, sandbox, host, port)
		},
		Classify: classifyForward,
		Running:  m.isRunning,
	}
	m.core = &ingress.Server{
		Routes:    ingressRoutes{m},
		Dialer:    m.backend,
		Auth:      ingress.ProviderAuth{Owners: m.owners},
		Logf:      log.Printf,
		Forwarded: m.forwarded,
		AdminURL: func() string {
			if s.publicOrigin != "" {
				return s.publicOrigin + "/"
			}
			return ""
		},
	}
	// Idle keep-alive connections give their places up to new ones.
	m.backend.OnBusy = m.core.CloseIdle
	m.newProvider = func(cfg *IngressConfig, secret func() string) ingress.Provider {
		return tailscale.New(tailscale.Config{
			Dir: filepath.Join(m.dir, "ingress", "tailscale"), Prefix: cfg.prefix(), Tag: cfg.tag(),
			Secret: secret, Logf: log.Printf,
		})
	}
	m.newLocal = func() ingress.Provider {
		_, port, _ := net.SplitHostPort(s.host)
		n, _ := strconv.Atoi(port)
		return local.New(local.Config{
			Port:        n,
			AdminURL:    s.origin + "/",
			Known:       func(name string) bool { _, ok := s.lookupSandbox(name); return ok },
			AdminCookie: func(name string) bool { return name == s.cookieName },
		})
	}
	return m
}

// hostTaker is a provider that routes by Host on Iwato's listener (local).
type hostTaker interface {
	Take(c net.Conn, r *http.Request) bool
}

// take offers a connection of Iwato's listener, whose first request is r,
// to the local provider (local.Split): false keeps it for the admin UI.
func (m *ingressManager) take(c net.Conn, r *http.Request) bool {
	m.mu.Lock()
	p := m.provider
	m.mu.Unlock()
	t, ok := p.(hostTaker)
	return ok && t.Take(c, r)
}

// classifyForward maps the ForwardTcp errors to the ingress's.
func classifyForward(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, client.ErrConnRefused):
		return fmt.Errorf("%w: %v", ingress.ErrRefused, err)
	case errors.Is(err, client.ErrNotReady):
		return fmt.Errorf("%w: %v", ingress.ErrStopped, err)
	case errors.Is(err, client.ErrSandboxBusy):
		return fmt.Errorf("%w: %v", ingress.ErrBusy, err)
	}
	return err
}

func (m *ingressManager) config() *IngressConfig {
	m.s.mu.Lock()
	defer m.s.mu.Unlock()
	return m.s.config.Ingress
}

func (m *ingressManager) owners() []string { return m.config().owners() }

func (m *ingressManager) isRunning(sandbox string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.running[sandbox]
}

// listening returns the sandbox's listening ports, read again when the
// last read is older than listenTTL (one read at a time per sandbox). With
// stale, an older read is returned at once while the new one runs.
func (m *ingressManager) listening(ctx context.Context, sb Sandbox, stale bool) ([]ingress.ListenPort, error) {
	if m.rt == nil {
		return nil, errors.New("no runtime")
	}
	m.mu.Lock()
	r := m.listeners[sb.Name]
	if r == nil {
		r = &listenRead{}
		m.listeners[sb.Name] = r
	}
	fresh := r.done && time.Since(r.at) < listenTTL
	if !fresh && r.running == nil {
		r.running = make(chan struct{})
		go m.readListening(sb, r)
	}
	if fresh || (stale && r.done) {
		defer m.mu.Unlock()
		return r.ports, r.err
	}
	running := r.running
	m.mu.Unlock()
	select {
	case <-running:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return r.ports, r.err
}

// readListening runs one read for listening (not on a caller's ctx:
// others may wait for it).
func (m *ingressManager) readListening(sb Sandbox, r *listenRead) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	ports, err := m.rt.ListeningPorts(ctx, sb)
	cancel()
	m.mu.Lock()
	r.ports, r.err, r.at, r.done = ports, err, time.Now(), true
	running := r.running
	r.running = nil
	m.mu.Unlock()
	close(running)
}

// forwarded admits a tailnet connection to a running sandbox's port only
// while a socket listens there (a stale one, left with OpenShell's broker,
// is swept before each read: ingress.ListenScript). A stopped
// sandbox's ports are admitted (they get the 停止中 page), and so is any
// port when the ports cannot be read (the connection then gets 502 or a cut).
// It runs in tsnet's handler, under the node's lock: it answers from the
// last read (refreshed in the background) and waits for a first read
// only briefly.
func (m *ingressManager) forwarded(t ingress.Target) bool {
	if !m.isRunning(t.Sandbox) {
		return true
	}
	sb, ok := m.s.lookupSandbox(t.Sandbox)
	if !ok {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	ports, err := m.listening(ctx, sb, true)
	if err != nil {
		log.Printf("ingress %s:%d: listening ports: %v (admitted)", t.Sandbox, t.Port, english(err))
		return true
	}
	return ingress.Listed(ports, t.Port)
}

// ingressRoutes reads the routes from the configuration.
type ingressRoutes struct{ m *ingressManager }

func (r ingressRoutes) Options(t ingress.Target) (ingress.PortOptions, bool) {
	sb, ok := r.m.s.lookupSandbox(t.Sandbox)
	if !ok {
		return ingress.PortOptions{}, false
	}
	return portOptions(r.m.config(), sb, t.Port)
}

// secretInfo is the stored secret as the page sees it.
type secretInfo struct {
	Set bool `json:"set"`
	// Source: env (IWATO_TS_AUTHKEY), file or memory.
	Source string `json:"source,omitempty"`
	// Kind: oauth (tskey-client-) or authkey (tskey-auth-).
	Kind string `json:"kind,omitempty"`
	// Hint is the prefix and the last four characters.
	Hint string `json:"hint,omitempty"`
	// Storage is how the file is protected: dpapi or file (plain, 0600).
	Storage string `json:"storage"`
	Error   string `json:"error,omitempty"`
}

func (m *ingressManager) secretPath() string { return filepath.Join(m.dir, ingressSecretFile) }

// secret is the Tailscale secret and where it came from.
func (m *ingressManager) secret() (string, string, error) {
	if v := strings.TrimSpace(os.Getenv(ingressSecretEnv)); v != "" {
		return v, "env", nil
	}
	if m.dir == "" {
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.memSecret == "" {
			return "", "", nil
		}
		return m.memSecret, "memory", nil
	}
	b, err := protect.Load(m.secretPath())
	if errors.Is(err, fs.ErrNotExist) {
		return "", "", nil
	}
	if err != nil {
		return "", "file", err
	}
	return string(b), "file", nil
}

func (m *ingressManager) secretValue() string {
	v, _, _ := m.secret()
	return v
}

func secretKind(v string) string {
	switch {
	case strings.HasPrefix(v, "tskey-client-"):
		return "oauth"
	case strings.HasPrefix(v, "tskey-auth-"):
		return "authkey"
	}
	return ""
}

// validSecret accepts what the settings take: an OAuth client secret or
// an auth key, without options (Iwato adds its own).
func validSecret(v string) error {
	if len(v) > 300 || strings.ContainsAny(v, " \t\r\n?&") || secretKind(v) == "" || len(v) < 20 {
		return errCode("ingress_secret_invalid", nil, "tskey-client-… (OAuth クライアントのシークレット) か tskey-auth-… (auth key) を貼り付けてください")
	}
	return nil
}

func (m *ingressManager) secretInfo() secretInfo {
	v, src, err := m.secret()
	info := secretInfo{Storage: protect.Scheme, Source: src}
	if err != nil {
		info.Error = "保存したシークレットを読めません: " + err.Error()
		return info
	}
	if v == "" {
		return info
	}
	info.Set, info.Kind = true, secretKind(v)
	prefix := "tskey-"
	if info.Kind == "oauth" {
		prefix = "tskey-client-"
	} else if info.Kind == "authkey" {
		prefix = "tskey-auth-"
	}
	tail := v
	if len(tail) > 4 {
		tail = tail[len(tail)-4:]
	}
	info.Hint = prefix + "…" + tail
	return info
}

func (m *ingressManager) setSecret(v string) error {
	if m.dir == "" {
		m.mu.Lock()
		m.memSecret = v
		m.mu.Unlock()
		return nil
	}
	if v == "" {
		return protect.Remove(m.secretPath())
	}
	return protect.Save(m.secretPath(), []byte(v))
}

// overall is the ingress's state for the settings and the Ports tab (「ポート」).
func (m *ingressManager) overall() ingress.Status {
	cfg := m.config()
	switch cfg.provider() {
	case providerOff:
		return ingress.Status{State: "off", Code: "off", Message: "公開していません"}
	case providerLocal:
		m.mu.Lock()
		on := m.provider != nil
		m.mu.Unlock()
		if !on {
			return ingress.Status{State: "starting", Code: "starting", Message: "準備中です"}
		}
		return ingress.Status{State: "on", Code: "local", Message: "この PC のブラウザーで開けます (*.localhost)"}
	}
	info := m.secretInfo()
	if info.Error != "" {
		return ingress.Status{State: "error", Message: info.Error}
	}
	if !info.Set {
		return ingress.Status{State: "needs-secret", Code: "needs_secret", Message: "Tailscale の OAuth クライアントのシークレットがないため、公開していません"}
	}
	if len(cfg.owners()) == 0 {
		return ingress.Status{State: "error", Code: "no_owners", Message: "通すログイン名 (owners) がないため、どの端末からも開けません"}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.syncErr != "" {
		return ingress.Status{State: "error", Message: m.syncErr}
	}
	return ingress.Status{State: "on", Code: "tailscale", Message: "Tailscale で公開しています"}
}

// reconfigure makes, replaces or closes the provider to match the
// configuration and the secret, then syncs.
func (m *ingressManager) reconfigure(ctx context.Context) {
	m.reconfMu.Lock()
	defer m.reconfMu.Unlock()
	cfg := m.config()
	secret := m.secretValue()
	key := ""
	switch cfg.provider() {
	case providerLocal:
		key = providerLocal
	case providerTailscale:
		if secret != "" {
			key = providerTailscale + "|" + cfg.prefix() + "|" + cfg.tag()
		}
	}
	m.mu.Lock()
	old := m.provider
	if key == m.providerKey && (old != nil) == (key != "") {
		m.mu.Unlock()
		m.sync(ctx, true)
		return
	}
	m.provider, m.providerKey, m.lastSync = nil, key, ""
	m.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}
	if key == "" {
		return
	}
	var p ingress.Provider
	if key == providerLocal {
		p = m.newLocal()
	} else {
		p = m.newProvider(cfg, m.secretValue)
	}
	if err := p.Start(ctx, m.core); err != nil {
		m.mu.Lock()
		m.syncErr = err.Error()
		m.mu.Unlock()
		return
	}
	m.mu.Lock()
	m.provider = p
	m.mu.Unlock()
	m.sync(ctx, true)
}

// states reads every sandbox's state from the runtime.
func (m *ingressManager) states(ctx context.Context) []ingress.SandboxState {
	list := m.s.sandboxList()
	out := make([]ingress.SandboxState, len(list))
	var wg sync.WaitGroup
	for i, sb := range list {
		wg.Add(1)
		go func() {
			defer wg.Done()
			st := m.s.rt.Status(ctx, sb)
			out[i] = ingress.SandboxState{Name: sb.Name, Running: st.State == "running", Exposed: sb.Ingress == nil || !sb.Ingress.Disabled}
		}()
	}
	wg.Wait()
	return out
}

// sync gives the provider the sandboxes' states (only when they changed,
// unless force).
func (m *ingressManager) sync(ctx context.Context, force bool) {
	m.mu.Lock()
	p := m.provider
	m.mu.Unlock()
	if p == nil {
		return
	}
	m.apply(ctx, m.states(ctx), force)
}

func (m *ingressManager) apply(ctx context.Context, states []ingress.SandboxState, force bool) {
	var b strings.Builder
	for _, st := range states {
		fmt.Fprintf(&b, "%s:%t:%t;", st.Name, st.Running, st.Exposed)
	}
	m.mu.Lock()
	p := m.provider
	var stopped []string
	next := map[string]bool{}
	for _, st := range states {
		next[st.Name] = st.Running
		if m.running[st.Name] && !st.Running {
			stopped = append(stopped, st.Name)
		}
	}
	m.running = next
	for _, name := range stopped {
		delete(m.listeners, name)
	}
	same := b.String() == m.lastSync
	m.lastSync = b.String()
	m.mu.Unlock()
	for _, name := range stopped {
		m.core.Reset(name)
		if m.rt != nil {
			m.rt.ForgetPorts(name)
		}
	}
	if p == nil || (same && !force) {
		return
	}
	err := p.Sync(ctx, states)
	m.mu.Lock()
	m.syncErr = ""
	if err != nil {
		m.syncErr = err.Error()
		log.Printf("ingress sync: %v", english(err))
	}
	m.mu.Unlock()
}

// observe takes the states the sandbox list just read (no extra calls).
func (m *ingressManager) observe(list []Status) {
	sbs := m.s.sandboxList()
	if len(sbs) != len(list) {
		return
	}
	states := make([]ingress.SandboxState, len(list))
	for i, st := range list {
		states[i] = ingress.SandboxState{Name: sbs[i].Name, Running: st.State == "running", Exposed: sbs[i].Ingress == nil || !sbs[i].Ingress.Disabled}
	}
	go m.apply(context.WithoutCancel(m.s.ctx), states, false)
}

func (m *ingressManager) kick() {
	select {
	case m.kickCh <- struct{}{}:
	default:
	}
}

// loop keeps the provider in step: at start, after each start, stop or
// delete (kick) and every 15 seconds (a sandbox whose shell ended).
func (m *ingressManager) loop(ctx context.Context) {
	m.reconfigure(ctx)
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			m.mu.Lock()
			p := m.provider
			m.provider = nil
			m.mu.Unlock()
			if p != nil {
				_ = p.Close()
			}
			return
		case <-m.kickCh:
			m.sync(ctx, false)
		case <-t.C:
			m.sync(ctx, false)
		}
	}
}

// forget releases a deleted sandbox's name in the background (Logout can
// take a while); a later Sync also forgets nodes of sandboxes that are gone.
func (m *ingressManager) forget(name string) {
	m.core.Reset(name)
	if m.rt != nil {
		m.rt.ForgetPorts(name)
	}
	m.mu.Lock()
	p := m.provider
	delete(m.running, name)
	delete(m.listeners, name)
	m.mu.Unlock()
	if p == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(m.s.ctx), 3*time.Minute)
		defer cancel()
		if err := p.Forget(ctx, name); err != nil {
			noticef("ingress: %v", english(err))
		}
	}()
}

// reset drops the connections, tokens and port reads of a sandbox that
// OpenShell deleted and makes again under the same name (a recreate). Its
// node stays: the name, and so the URLs, do not change.
func (m *ingressManager) reset(name string) {
	m.core.Reset(name)
	if m.rt != nil {
		m.rt.ForgetPorts(name)
	}
	m.mu.Lock()
	delete(m.listeners, name)
	m.mu.Unlock()
}

// nodeView is a sandbox's node for the page.
type nodeView struct {
	Sandbox  string `json:"sandbox"`
	Hostname string `json:"hostname"`
	ingress.Status
}

func (m *ingressManager) nodes() []nodeView {
	m.mu.Lock()
	p := m.provider
	m.mu.Unlock()
	cfg := m.config()
	out := []nodeView{}
	if cfg.provider() != providerTailscale {
		return out
	}
	for _, sb := range m.s.sandboxList() {
		v := nodeView{Sandbox: sb.Name, Hostname: cfg.prefix() + sb.Name, Status: ingress.Status{State: "off"}}
		if sb.Ingress != nil && sb.Ingress.Disabled {
			v.Message, v.Code = "このサンドボックスは公開しない設定です", "disabled"
		} else if p != nil {
			v.Status = p.Status(sb.Name)
		}
		out = append(out, v)
	}
	return out
}

// suggestedOwners are logins to prefill: the one tailscale serve says
// opened the page, else this PC's Tailscale user.
func (m *ingressManager) suggestedOwners(r *http.Request) []string {
	var out []string
	if l := strings.TrimSpace(r.Header.Get("Tailscale-User-Login")); l != "" && !loopbackDirect(r) && ownerLogin.MatchString(l) {
		out = append(out, l)
	}
	for _, l := range m.localLogin() {
		if !slices.Contains(out, l) {
			out = append(out, l)
		}
	}
	return out
}

// localLogin asks this PC's tailscale (status --json) for its user.
func (m *ingressManager) localLogin() []string {
	if m.s.fake {
		return []string{"owner@github"}
	}
	m.mu.Lock()
	if time.Since(m.ownerAt) < 5*time.Minute {
		defer m.mu.Unlock()
		return m.ownerCache
	}
	m.mu.Unlock()
	login := tailscaleSelfLogin()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ownerAt, m.ownerCache = time.Now(), nil
	if login != "" {
		m.ownerCache = []string{login}
	}
	return m.ownerCache
}

func tailscaleSelfLogin() string {
	exe, err := exec.LookPath("tailscale")
	if err != nil && runtime.GOOS == "windows" {
		exe = filepath.Join(os.Getenv("ProgramFiles"), "Tailscale", "tailscale.exe")
	} else if err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, exe, "status", "--json", "--peers=false").Output()
	if err != nil {
		return ""
	}
	return parseSelfLogin(out)
}

// parseSelfLogin reads the login of Self from `tailscale status --json`.
func parseSelfLogin(b []byte) string {
	var st struct {
		Self *struct {
			UserID int64
		}
		User map[string]struct {
			LoginName string
		}
	}
	if json.Unmarshal(b, &st) != nil || st.Self == nil {
		return ""
	}
	u, ok := st.User[fmt.Sprint(st.Self.UserID)]
	if !ok || !ownerLogin.MatchString(u.LoginName) {
		return ""
	}
	return u.LoginName
}

// handleIngress is the settings' API:
//
//	GET  /api/ingress                the settings, the secret's state, the nodes
//	POST /api/ingress                {provider, tag, hostnamePrefix, owners}
//	POST /api/ingress/secret         {secret}
//	POST /api/ingress/secret/delete
func (s *Server) handleIngress(w http.ResponseWriter, r *http.Request) {
	m := s.ingress
	tail := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/ingress"), "/")
	switch {
	case r.Method == http.MethodGet && tail == "":
		if !s.require(w, r, false) {
			return
		}
		cfg := m.config()
		jsonResponse(w, 200, map[string]any{
			"provider": cfg.provider(), "listen": s.host,
			"tag": cfg.tag(), "hostnamePrefix": cfg.prefix(), "owners": nonNil(cfg.owners()),
			"defaults":        map[string]string{"tag": defaultTailscaleTag, "hostnamePrefix": defaultHostnamePrefix},
			"suggestedOwners": nonNil(m.suggestedOwners(r)),
			"secret":          m.secretInfo(), "secretEnv": ingressSecretEnv,
			"status": m.overall(), "nodes": m.nodes(),
		})
	case r.Method == http.MethodPost && tail == "":
		if !s.require(w, r, true) {
			return
		}
		var body struct {
			Provider       string   `json:"provider"`
			Tag            string   `json:"tag"`
			HostnamePrefix string   `json:"hostnamePrefix"`
			Owners         []string `json:"owners"`
		}
		if decode(w, r, &body) != nil {
			fail(w, 400, "invalid request")
			return
		}
		owners := normalizeOwners(body.Owners)
		switch body.Provider {
		case providerLocal, providerTailscale, providerOff:
		default:
			fail(w, 400, "provider: local, tailscale or off")
			return
		}
		if body.Provider == providerTailscale && len(owners) == 0 {
			failCode(w, 400, "ingress_owners_missing", nil, "公開するには、通すログイン名 (owners) を 1 つ以上入れてください")
			return
		}
		prefix := strings.TrimSpace(body.HostnamePrefix)
		next := &IngressConfig{Tailscale: &TailscaleIngressConfig{Tag: strings.TrimSpace(body.Tag), HostnamePrefix: &prefix, Owners: owners}}
		if next.Tailscale.Tag == defaultTailscaleTag {
			next.Tailscale.Tag = ""
		}
		if prefix == defaultHostnamePrefix {
			next.Tailscale.HostnamePrefix = nil
		}
		// local, the default, is not written.
		if body.Provider != providerLocal {
			next.Provider = body.Provider
		}
		err := s.updateConfig(func(c *Config) error {
			if c.Ingress != nil {
				next.Auth, next.Host = c.Ingress.Auth, c.Ingress.Host
			}
			c.Ingress = next
			return nil
		})
		if err != nil {
			failErr(w, 400, err)
			return
		}
		m.later(func() { m.reconfigure(context.WithoutCancel(s.ctx)) })
		jsonResponse(w, 200, map[string]bool{"ok": true})
	case r.Method == http.MethodPost && tail == "secret":
		if !s.require(w, r, true) {
			return
		}
		var body struct {
			Secret string `json:"secret"`
		}
		if decode(w, r, &body) != nil {
			fail(w, 400, "invalid request")
			return
		}
		v := strings.TrimSpace(body.Secret)
		if err := validSecret(v); err != nil {
			failErr(w, 400, err)
			return
		}
		if err := m.setSecret(v); err != nil {
			failCode(w, 500, "ingress_secret_save_failed", map[string]any{"detail": err.Error()}, "保存できませんでした: "+err.Error())
			return
		}
		m.later(func() { m.reconfigure(context.WithoutCancel(s.ctx)) })
		jsonResponse(w, 200, map[string]any{"ok": true, "secret": m.secretInfo()})
	case r.Method == http.MethodPost && tail == "secret/delete":
		if !s.require(w, r, true) {
			return
		}
		if err := m.setSecret(""); err != nil {
			failErr(w, 500, err)
			return
		}
		m.later(func() { m.reconfigure(context.WithoutCancel(s.ctx)) })
		jsonResponse(w, 200, map[string]any{"ok": true, "secret": m.secretInfo()})
	default:
		fail(w, 405, "method/operation not allowed")
	}
}

func nonNil(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

// portView is a listening port for the Ports tab (「ポート」).
type portView struct {
	ingress.ListenPort
	URL string `json:"url,omitempty"`
	// Disabled: closed by the sandbox's configuration.
	Disabled bool `json:"disabled,omitempty"`
	// TCPOnly: mode tcp, which the local provider (Host routing, HTTP
	// only) cannot pass.
	TCPOnly bool `json:"tcpOnly,omitempty"`
}

// handlePorts serves GET /api/sandboxes/{name}/ports: the node and the
// ports the sandbox listens on: the read the ingress admits connections
// with (at most listenTTL old), so the list is what the tailnet reaches.
func (s *Server) handlePorts(w http.ResponseWriter, r *http.Request, parts []string) {
	if r.Method != http.MethodGet {
		fail(w, 405, "method not allowed")
		return
	}
	sb, ok := s.sandboxFor(w, parts, 2)
	if !ok || !s.require(w, r, false) {
		return
	}
	m := s.ingress
	cfg := m.config()
	resp := map[string]any{"overall": m.overall(), "provider": cfg.provider(), "hostname": cfg.prefix() + sb.Name, "ports": []portView{}}
	m.mu.Lock()
	p := m.provider
	m.mu.Unlock()
	node := ingress.Status{State: "off"}
	if sb.Ingress != nil && sb.Ingress.Disabled {
		node.Message, node.Code = "このサンドボックスは公開しない設定です", "disabled"
	} else if p != nil {
		node = p.Status(sb.Name)
	}
	resp["node"] = node
	ctx, cancel := timeout(r.Context(), 10*time.Second)
	defer cancel()
	st := s.rt.Status(ctx, sb)
	resp["running"] = st.State == "running"
	if st.State == "running" && m.rt != nil {
		list, err := m.listening(ctx, sb, false)
		if err != nil {
			resp["error"] = "ポートを読めません: " + safeCLIError(err)
			resp["errorCode"], resp["errorParams"] = "ports_unreadable", map[string]any{"detail": safeCLIError(err)}
		}
		views := []portView{}
		for _, lp := range list {
			v := portView{ListenPort: lp}
			opts, exposed := portOptions(cfg, sb, lp.Port)
			v.Disabled = !exposed || opts.Disabled
			v.TCPOnly = cfg.provider() == providerLocal && opts.Mode == ingress.ModeTCP
			if p != nil && !v.Disabled && !v.TCPOnly {
				v.URL, _ = p.URL(ingress.Target{Sandbox: sb.Name, Port: lp.Port})
			}
			views = append(views, v)
		}
		resp["ports"] = views
	}
	jsonResponse(w, 200, resp)
}
