package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"iwato/internal/ingress"
	"iwato/internal/openshell/gateway"
	"iwato/internal/openshell/wslc"
	"iwato/internal/protect"
)

// The reset (設定の「すべて消す」, POST /api/reset) deletes everything Iwato
// made, so that the next preparation is a first run:
//
//   - every sandbox (those configured and any other the gateway has) with
//     its volumes (the home's, and an older sandbox's paths outside it,
//     LegacyPersist), and the volumes left over (this configuration's, by
//     the owner label, such as those a recreate no longer mounts);
//   - the gateway: its container, its state volume (the PKI, the
//     providers' credentials, the policies), its network and its image, and
//     what the WSLc driver made for the namespace;
//   - Iwato's sandbox image (only the one images.json records: the tag is
//     not the instance's own) and the images it replaced;
//   - the secrets in the configuration's folder: openshell/gateway-tls and
//     driver-tls, the driver's state, the Tailscale secret and nodes (logged
//     out first, as a delete does), the Web Push key and subscriptions, the
//     logins and the WebDAV app passwords. The sandboxes and the providers'
//     scopes leave iwato.json. On macOS the Keychain item that encrypts the
//     secret files goes too;
//   - the OAuth flows in progress: canceled first, so none creates a
//     provider in the fresh gateway.
//
// It keeps Iwato's executable, the logs, the engine itself (WSLc's session
// and its VHDX, which keeps the blocks of the deleted volumes until it is
// compacted), images the owner named for a sandbox, OpenShell's own
// runtime images, and by default the owner's files (presets/, profiles/)
// and iwato.json's other settings; ownerFiles deletes those too (the
// settings go back to their defaults, apart from what names this instance
// and its engine).
//
// Every login ends: the console prints a new login URL. Then Iwato prepares
// the images and the gateway again, as on its first start.

// deleteSecretKey removes the key of the protected secret files (tests replace it).
var deleteSecretKey = protect.DeleteKey

// resetWord is what the owner types to confirm.
const resetWord = "reset"

// resetOut is where the new login URL goes (the console).
var resetOut io.Writer = os.Stdout

func init() { opBusy["reset"] = "リセット中です" }

// resetWarn is what a reset could not do: a code and params for the page
// (web/i18n.js reset.warning.<code>) and the text in English (the console
// prints it too).
type resetWarn struct {
	Code   string         `json:"code"`
	Params map[string]any `json:"params,omitempty"`
	text   string
}

func resetWarning(code string, params map[string]any, text string) resetWarn {
	return resetWarn{Code: code, Params: params, text: text}
}

// failedTo is the warning that what failed with err (none when err is
// nil); params get err as detail.
func failedTo(code string, params map[string]any, what string, err error) []resetWarn {
	if err == nil {
		return nil
	}
	p := map[string]any{"detail": safeCLIError(err)}
	for k, v := range params {
		p[k] = v
	}
	return []resetWarn{resetWarning(code, p, what+": "+safeText(english(err)))}
}

// resetRuntime removes what the runtime made (OpenShell, Fake) and then
// prepares it again.
type resetRuntime interface {
	// Reset deletes the sandboxes, the gateway, the images and the
	// volumes, reporting each step; it returns what it could not do.
	Reset(ctx context.Context, sandboxes []Sandbox, step func(code string, params map[string]any, text string)) []resetWarn
	// PrepareAgain starts the preparation of a first run.
	PrepareAgain(ctx context.Context)
}

// resetJob is the reset in progress, for the page's progress.
type resetJob struct {
	mu      sync.Mutex
	running bool
	step    string
	code    string
	params  map[string]any
	started time.Time
}

// begin starts a reset unless one runs or blocked says why not; a retry
// of the preparation (unlessRunning) cannot slip in between.
func (j *resetJob) begin(blocked func() string) string {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.running {
		return opBusy["reset"]
	}
	if msg := blocked(); msg != "" {
		return msg
	}
	j.running, j.step, j.code, j.params, j.started = true, "starting", "starting", nil, time.Now()
	return ""
}

// unlessRunning runs f unless a reset runs, and says whether it ran.
func (j *resetJob) unlessRunning(f func()) bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.running {
		return false
	}
	f()
	return true
}

// set records the step shown: code and params name text for the page
// (text is English: it goes to the log).
func (j *resetJob) set(code string, params map[string]any, step string) {
	log.Printf("reset: %s", step)
	j.mu.Lock()
	j.step, j.code, j.params = step, code, params
	j.mu.Unlock()
}

func (j *resetJob) end() {
	j.mu.Lock()
	j.running, j.step, j.code, j.params = false, "", "", nil
	j.mu.Unlock()
}

func (j *resetJob) view() map[string]any {
	j.mu.Lock()
	defer j.mu.Unlock()
	if !j.running {
		return map[string]any{"running": false}
	}
	return map[string]any{"running": true, "step": j.step, "code": j.code, "params": j.params, "seconds": int(time.Since(j.started).Seconds())}
}

// handleReset answers GET /api/reset (the progress) and POST /api/reset
// ({"confirm":"reset","ownerFiles":bool}), which waits until the reset is
// done: the session that asked is checked before, so the answer still
// arrives once the logins are gone.
func (s *Server) handleReset(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if s.require(w, r, false) {
			jsonResponse(w, 200, s.resets.view())
		}
		return
	case http.MethodPost:
	default:
		fail(w, 405, "method not allowed")
		return
	}
	if !s.require(w, r, true) {
		return
	}
	var body struct {
		Confirm    string `json:"confirm"`
		OwnerFiles bool   `json:"ownerFiles"`
	}
	if decode(w, r, &body) != nil || body.Confirm != resetWord {
		failCode(w, 400, "reset_confirm", map[string]any{"word": resetWord}, "確認のため「"+resetWord+"」と入力してください")
		return
	}
	rt, ok := s.rt.(resetRuntime)
	if !ok {
		fail(w, 501, "reset unavailable")
		return
	}
	if msg := s.resets.begin(s.resetBlocked); msg != "" {
		switch msg {
		case resetWaitPrep:
			failCode(w, 409, "reset_wait_prep", nil, msg)
		case resetWaitUpdate:
			failCode(w, 409, "reset_wait_update", nil, msg)
		default:
			failBusy(w, msg)
		}
		return
	}
	// The reset goes on when the page goes away.
	type outcome struct {
		warnings []resetWarn
		err      error
	}
	done := make(chan outcome, 1)
	go func() {
		defer s.resets.end()
		ctx, cancel := context.WithTimeout(s.ctx, 30*time.Minute)
		defer cancel()
		warnings, err := s.reset(ctx, rt, body.OwnerFiles)
		if err != nil {
			log.Printf("reset: %v", english(err))
		}
		done <- outcome{warnings, err}
	}()
	select {
	case o := <-done:
		if o.err != nil {
			failErr(w, 409, o.err)
			return
		}
		texts, codes := []string{}, []resetWarn{}
		for _, w := range o.warnings {
			texts, codes = append(texts, w.text), append(codes, w)
		}
		jsonResponse(w, 200, map[string]any{"ok": true, "warnings": texts, "warningCodes": codes})
	case <-r.Context().Done():
	}
}

// resetBlocked refuses a reset while the images or the gateway are being
// prepared or the standard image is pulled: those would write again what
// the reset removes.
const (
	resetWaitPrep   = "準備が終わってからリセットしてください (イメージ・ゲートウェイの準備中です)"
	resetWaitUpdate = "標準イメージの更新が終わってからリセットしてください"
)

func (s *Server) resetBlocked() string {
	if s.images != nil {
		if p := s.images.get().Phase; p != "ready" && p != "failed" {
			return resetWaitPrep
		}
	}
	if p, ok := s.rt.(preparer); ok {
		if ph := p.Preparation().Phase; ph != "ready" && ph != "failed" {
			return resetWaitPrep
		}
	}
	if s.imageUpdates != nil && s.imageUpdates.get().Phase == "pulling" {
		return resetWaitUpdate
	}
	return ""
}

// reset does the work of POST /api/reset. An error means nothing was
// deleted (a sandbox was busy); the warnings are what could not be done.
func (s *Server) reset(ctx context.Context, rt resetRuntime, ownerFiles bool) ([]resetWarn, error) {
	// No OAuth flow creates a provider in the fresh gateway.
	s.cancelOAuthFlows(true)
	// No sandbox is created (or its volume removed) meanwhile, and none is
	// started, stopped or deleted: each is claimed (a start is canceled).
	s.configMu.Lock()
	defer s.configMu.Unlock()
	list := s.sandboxList()
	ops := map[string]*sandboxOp{}
	defer func() {
		for name, op := range ops {
			s.endOp(name, op, nil)
		}
	}()
	for _, sb := range list {
		op, _, _, busy := s.beginOp(sb.Name, "reset")
		if busy != "" {
			return nil, errCodeEn("reset_sandbox_busy", map[string]any{"sandbox": sb.Name, "busy": busy, "op": busyCode(busy)}, fmt.Sprintf("サンドボックス %s: %s。終わってからリセットしてください", sb.Name, busy), "sandbox "+sb.Name+" is busy; reset when it is done", nil)
		}
		ops[sb.Name] = op
	}
	step := s.resets.set
	if s.images != nil {
		s.images.set(prepState("building", "", "resetting", nil, "リセットしています…"))
	}
	var warnings []resetWarn
	names := make([]string, len(list))
	for i, sb := range list {
		names[i] = sb.Name
	}
	step("unpublish", nil, "stopping publishing and logging the Tailscale nodes out")
	warnings = append(warnings, s.ingress.logoutAll(ctx, names)...)
	warnings = append(warnings, rt.Reset(ctx, list, step)...)
	for _, name := range names {
		if s.files != nil {
			s.files.Forget(name)
		}
		s.ingress.reset(name)
	}
	step("secrets", nil, "deleting the secrets in the configuration folder")
	warnings = append(warnings, s.resetFiles(ownerFiles)...)
	step("logins", nil, "deleting the logins")
	s.mu.Lock()
	s.policyChanges = nil
	for h := range s.sessions {
		s.endSessionLocked(h)
	}
	warnings = append(warnings, failedTo("logins", nil, "could not delete the logins", s.saveSessionsLocked())...)
	s.bootstrap, s.bootstrapUntil = randomHex(32), time.Now().Add(10*time.Minute)
	s.mu.Unlock()
	// One started meanwhile, while a login still was (its handover's
	// gateway calls end with its context).
	s.cancelOAuthFlows(false)
	for _, w := range warnings {
		noticef("reset: %s", w.text)
	}
	fmt.Fprint(resetOut, s.loginBlock("Iwato was reset (every login ended; preparing again)", "", true))
	// A first run again.
	rt.PrepareAgain(s.ctx)
	if s.retryImages != nil {
		s.retryImages()
	}
	go s.ingress.reconfigure(s.ctx)
	return warnings, nil
}

// resetFiles removes the secrets in the configuration's folder and, with
// ownerFiles, the owner's own files; it takes the sandboxes and the scopes
// out of iwato.json.
func (s *Server) resetFiles(ownerFiles bool) []resetWarn {
	var warnings []resetWarn
	warnTo := func(code string, params map[string]any, what string, err error) {
		if !errors.Is(err, os.ErrNotExist) {
			warnings = append(warnings, failedTo(code, params, what, err)...)
		}
	}
	s.mu.Lock()
	next := s.config
	s.mu.Unlock()
	next.Sandboxes, next.ProviderScopes = []Sandbox{}, nil
	if ownerFiles {
		next.Ingress, next.TermTrace, next.Timezone = nil, false, ""
	}
	if s.configPath != "" {
		warnTo("config", nil, "could not write iwato.json", writeConfig(s.configPath, next))
	}
	s.mu.Lock()
	s.config = next
	s.mu.Unlock()
	if ownerFiles {
		s.applyTimezone("")
	}
	warnTo("tailscale_secret", nil, "could not delete the Tailscale secret", s.ingress.setSecret(""))
	warnTo("secret_key", nil, "could not delete the key of the secret files", deleteSecretKey())
	if s.push != nil {
		warnTo("push_keys", nil, "could not make new Web Push keys", s.push.reset())
	}
	if s.davKeys != nil {
		warnTo("app_passwords", nil, "could not delete the app passwords", s.davKeys.clear())
	}
	if s.configPath != "" {
		dir := filepath.Dir(s.configPath)
		warnTo("tailscale_states", nil, "could not delete the Tailscale node states", os.RemoveAll(filepath.Join(dir, "ingress")))
		if ownerFiles {
			for _, d := range []string{"presets", "profiles"} {
				warnTo("folder", map[string]any{"folder": d}, "could not delete the "+d+" folder", os.RemoveAll(filepath.Join(dir, d)))
			}
		}
	}
	// The scoped profiles went with the providers; the custom ones are
	// read again (or are gone).
	extraProfiles.Lock()
	clear(extraProfiles.m)
	extraProfiles.Unlock()
	s.loadProfiles()
	reloadUserPresets()
	return warnings
}

// logoutAll stops the exposure and logs every Tailscale node out (the
// configured sandboxes' and any whose state is in the folder), as deleting
// a sandbox does; the provider is made again after the reset.
func (m *ingressManager) logoutAll(ctx context.Context, sandboxes []string) []resetWarn {
	m.reconfMu.Lock()
	defer m.reconfMu.Unlock()
	m.mu.Lock()
	p := m.provider
	m.provider, m.providerKey, m.lastSync = nil, "", ""
	m.mu.Unlock()
	names := slices.Clone(sandboxes)
	var states []string
	if m.dir != "" {
		entries, _ := os.ReadDir(filepath.Join(m.dir, "ingress", "tailscale"))
		for _, e := range entries {
			if e.IsDir() && safeID.MatchString(e.Name()) {
				states = append(states, e.Name())
				if !slices.Contains(names, e.Name()) {
					names = append(names, e.Name())
				}
			}
		}
	}
	var warnings []resetWarn
	if p == nil && len(states) > 0 {
		// Tailscale without its secret (or turned off) still logs out the
		// nodes that joined.
		p = m.newProvider(m.config(), m.secretValue)
	}
	if p != nil {
		for _, name := range names {
			if err := p.Forget(ctx, name); err != nil {
				var lo *ingress.LogoutError
				if errors.As(err, &lo) {
					warnings = append(warnings, resetWarning("tailscale_logout", map[string]any{"node": lo.Node, "detail": lo.Err.Error()}, "Tailscale: "+err.Error()))
				} else {
					warnings = append(warnings, resetWarning("tailscale", map[string]any{"detail": err.Error()}, "Tailscale: "+err.Error()))
				}
			}
		}
		_ = p.Close()
	}
	return warnings
}

// reset makes a new VAPID key and drops every subscription (a browser
// subscribed with the old key gets no more pushes).
func (s *PushStore) reset() error {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.key, s.subs = k, nil
	return s.saveLocked()
}

// clear removes every app password and secret URL.
func (k *davKeys) clear() error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.keys = nil
	return k.saveLocked()
}

// Reset deletes the sandboxes through the gateway, then the gateway, the
// driver's leftovers, the persisted paths' volumes, the images and the
// certificates.
func (o *OpenShell) Reset(ctx context.Context, sandboxes []Sandbox, step func(code string, params map[string]any, text string)) []resetWarn {
	var warnings []resetWarn
	warnTo := func(code string, params map[string]any, what string, err error) {
		warnings = append(warnings, failedTo(code, params, what, err)...)
	}
	// A failed preparation is not retried from here on (RetryPreparation
	// acts on failed only); the reset prepares again at its end.
	o.mu.Lock()
	o.prep = prepState("building", "openshell", "resetting", nil, "リセットしています…")
	o.prep.Log = o.logPath()
	o.step = time.Now()
	o.mu.Unlock()
	step("sandboxes", nil, "deleting the sandboxes")
	list := slices.Clone(sandboxes)
	if api, err := o.sandboxes(ctx); err != nil {
		warnTo("openshell_unavailable", nil, "OpenShell is not usable, so the sandboxes are deleted without the gateway", err)
		for _, sb := range list {
			o.dropTerminals(sb)
			o.forgetDraft(sb.Name)
		}
		list = nil
	} else if all, err := api.ListAll(ctx, openshellWorkspace); err != nil {
		warnTo("sandbox_list", nil, "could not list OpenShell's sandboxes", err)
	} else {
		for _, sb := range all {
			if !slices.ContainsFunc(list, func(w Sandbox) bool { return w.Name == sb.Name }) {
				list = append(list, Sandbox{Name: sb.Name})
			}
		}
	}
	for _, sb := range list {
		step("sandbox", map[string]any{"name": sb.Name}, "deleting sandbox "+sb.Name)
		warnTo("sandbox", map[string]any{"name": sb.Name}, "could not delete sandbox "+sb.Name, o.Delete(ctx, sb, true))
	}
	step("gateway", nil, "deleting the gateway")
	// Operations wait for the clients until the preparation runs again.
	o.mu.Lock()
	conn, stop := o.conn, o.stopDriver
	o.ready, o.err = make(chan struct{}), nil
	o.prep = prepState("building", "openshell", "resetting", nil, "リセットしています…")
	o.prep.Log = o.logPath()
	o.step = time.Now()
	o.conn, o.sdk, o.fwd, o.stopDriver, o.served, o.profilesDone = nil, nil, nil, nil, false, false
	o.mu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
	_, err := gateway.Remove(ctx, o.gatewayCLI(), o.gatewayOptions(ctx))
	warnTo("gateway", nil, "could not delete all of the gateway", err)
	if stop != nil {
		stop()
	}
	// Upstream's Docker driver labels its containers and volumes as the
	// WSLc driver does (the namespace is its sandbox_label).
	_, _, err = wslc.RemoveAll(ctx, o.gatewayCLI(), o.e.config.Namespace)
	warnTo("driver", nil, "could not delete all of the driver's containers and volumes", err)
	// Once no container mounts them.
	step("volumes", nil, "deleting the volumes left")
	if vols, err := o.e.volumes(ctx); err != nil {
		warnTo("volume_list", nil, "could not list the volumes", err)
	} else {
		for _, v := range vols {
			warnTo("volume", map[string]any{"name": v.Name}, "could not delete volume "+v.Name, o.e.removeVolume(ctx, v.Name))
		}
	}
	step("images", nil, "deleting the images")
	warnings = append(warnings, o.e.removeOwnImages(ctx, filepath.Dir(o.dir))...)
	step("certificates", nil, "deleting the certificates")
	for _, d := range []string{"gateway-tls", "driver-tls", "sandboxes"} {
		warnTo("folder", map[string]any{"folder": d}, "could not delete "+d, os.RemoveAll(filepath.Join(o.dir, d)))
	}
	o.termMu.Lock()
	o.daemons = nil
	o.termMu.Unlock()
	o.logMu.Lock()
	o.logCache = nil
	o.logMu.Unlock()
	return warnings
}

// PrepareAgain prepares the driver and the gateway as on the first start.
func (o *OpenShell) PrepareAgain(ctx context.Context) { go o.Prepare(ctx) }

// removeOwnImages removes the sandbox image when it is the one images.json
// records, and the images it replaced, then the records.
func (e *Engine) removeOwnImages(ctx context.Context, dir string) []resetWarn {
	e.cleanupMu.Lock()
	defer e.cleanupMu.Unlock()
	var warnings []resetWarn
	e.forgetImage(sandboxImage)
	defer e.forgetImage(sandboxImage)
	rec := readImageRecords(dir)[sandboxImage]
	d, err := e.inspect(ctx, "image", sandboxImage)
	switch {
	case err != nil:
		warnings = append(warnings, failedTo("image_inspect", map[string]any{"image": sandboxImage}, "could not inspect "+sandboxImage, err)...)
	case d == nil:
	case rec.ID == "" || rec.ID != val(d["Id"]):
		warnings = append(warnings, resetWarning("image_not_ours", map[string]any{"image": sandboxImage}, sandboxImage+" was kept: it is not the image Iwato made"))
	default:
		if _, err := e.run(ctx, "image", "remove", sandboxImage); err != nil {
			warnings = append(warnings, failedTo("image", map[string]any{"image": sandboxImage}, "could not delete "+sandboxImage+" (does another container use it?)", err)...)
		}
	}
	for _, id := range readStaleImages(dir) {
		if !imageIDPattern.MatchString(id) {
			continue
		}
		d, err := e.inspect(ctx, "image", id)
		if err != nil || d == nil {
			continue
		}
		if tags, _ := d["RepoTags"].([]any); len(tags) > 0 {
			continue // tagged again: no longer Iwato's
		}
		if _, err := e.run(ctx, "image", "remove", id); err != nil {
			warnings = append(warnings, failedTo("stale_image", map[string]any{"image": id}, "could not delete the old image "+id, err)...)
		}
	}
	for _, f := range []string{"images.json", staleImagesFile} {
		if err := os.Remove(filepath.Join(dir, f)); !errors.Is(err, os.ErrNotExist) {
			warnings = append(warnings, failedTo("file", map[string]any{"file": f}, "could not delete "+f, err)...)
		}
	}
	return warnings
}
