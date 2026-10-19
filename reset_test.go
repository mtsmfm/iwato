package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	v1 "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1"

	"iwato/internal/hostterm"
	"iwato/internal/ingress"
	"iwato/internal/protect"
)

// fakeWorld is a WSLc session for the reset: objects by kind and name, with
// their labels, answering inspect, list (label filters) and remove.
type fakeWorld struct {
	t    *testing.T
	mu   sync.Mutex
	objs map[string]map[string]map[string]any
}

func (f *fakeWorld) add(kind, name string, labels map[string]string, extra map[string]any) {
	if f.objs[kind] == nil {
		f.objs[kind] = map[string]map[string]any{}
	}
	d := map[string]any{"Name": name, "Labels": labels}
	if kind == "volume" {
		d["Driver"] = "guest"
	}
	if kind == "container" {
		d["Config"] = map[string]any{"Labels": labels}
	}
	maps.Copy(d, extra)
	f.objs[kind][name] = d
}

func (f *fakeWorld) names(kind string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Sorted(maps.Keys(f.objs[kind]))
}

func (f *fakeWorld) run(args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	kind, verb, rest := args[0], args[1], args[2:]
	switch verb {
	case "inspect":
		var out []map[string]any
		for _, n := range rest[2:] {
			if d, ok := f.objs[kind][n]; ok {
				out = append(out, d)
			}
		}
		b, _ := json.Marshal(append([]map[string]any{}, out...))
		if len(out) < len(rest)-2 {
			return b, exitStatus1(f.t)
		}
		return b, nil
	case "list":
		var match []string
		for n, d := range f.objs[kind] {
			l, _ := d["Labels"].(map[string]string)
			ok := true
			for i, a := range rest {
				if a == "--filter" {
					k, v, _ := strings.Cut(strings.TrimPrefix(rest[i+1], "label="), "=")
					ok = ok && l[k] == v
				}
			}
			if ok {
				match = append(match, n)
			}
		}
		return []byte(strings.Join(match, "\n")), nil
	case "remove":
		n := rest[len(rest)-1]
		if _, ok := f.objs[kind][n]; !ok {
			return nil, fmt.Errorf("wslc %s remove: no such %s %s", kind, kind, n)
		}
		delete(f.objs[kind], n)
		return nil, nil
	}
	return nil, fmt.Errorf("unexpected wslc %s", strings.Join(args, " "))
}

type worldRunner struct{ f *fakeWorld }

func (r worldRunner) Run(_ context.Context, _ []byte, args ...string) ([]byte, error) {
	return r.f.run(args...)
}

// resetSandboxes is the gateway's sandbox API: dev and one Iwato does not
// have in its configuration; a deleted one is gone at once.
type resetSandboxes struct {
	v1.SandboxInterface
	mu      sync.Mutex
	have    []string
	deleted []string
	// entered and release, when set, hold each Delete.
	entered, release chan struct{}
}

func (f *resetSandboxes) ListAll(context.Context, string, ...v1.ListOptions) ([]*v1.Sandbox, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*v1.Sandbox
	for _, n := range f.have {
		out = append(out, &v1.Sandbox{Name: n})
	}
	return out, nil
}

func (f *resetSandboxes) Delete(_ context.Context, _, name string, _ ...v1.DeleteOptions) (*v1.DeletionResult, error) {
	if f.release != nil {
		f.entered <- struct{}{}
		<-f.release
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.have = slices.DeleteFunc(f.have, func(n string) bool { return n == name })
	f.deleted = append(f.deleted, name)
	return nil, nil
}

func (f *resetSandboxes) Get(context.Context, string, string) (*v1.Sandbox, error) {
	return nil, &v1.StatusError{Code: v1.ErrorNotFound, Message: "sandbox not found"}
}

// resetRT is the OpenShell runtime without the preparation that follows.
type resetRT struct {
	*OpenShell
	again int
}

func (r *resetRT) PrepareAgain(context.Context) { r.again++ }

// forgetProvider records the Tailscale nodes logged out.
type forgetProvider struct {
	ingress.Provider
	mu     sync.Mutex
	forgot []string
}

func (p *forgetProvider) Forget(_ context.Context, name string) error {
	p.mu.Lock()
	p.forgot = append(p.forgot, name)
	p.mu.Unlock()
	return nil
}
func (p *forgetProvider) Close() error { return nil }

// The reset removes what Iwato made in WSLc (sandboxes through the gateway,
// their and the left-over volumes, the gateway's container, volume, network
// and image, the driver's leftovers, the recorded sandbox image and the
// replaced one) and the secrets in the folder, logs the Tailscale nodes
// out and ends every login. Another owner's or nobody's resources stay, and
// so do the owner's files and the logs. It needs the typed word and the
// CSRF token.
func TestResetRemovesOnlyIwatosOwn(t *testing.T) {
	dir := t.TempDir()
	c := testConfig()
	c.Sandboxes[0].LegacyPersist = []string{"/sandbox/work"} // made before the image convention
	c.Ingress = &IngressConfig{Provider: "tailscale"}
	dev := c.Sandboxes[0]
	own := map[string]string{ownerKey: c.Instance}
	driver := map[string]string{"openshell.ai/managed-by": "openshell", "openshell.ai/sandbox-namespace": c.Namespace}
	w := &fakeWorld{t: t, objs: map[string]map[string]map[string]any{}}
	w.add("volume", persistVolume(c, dev, homeDir), own, nil)
	w.add("volume", persistVolume(c, dev, "/sandbox/work"), own, nil)
	w.add("volume", persistVolume(c, newSandbox("old"), "/data"), own, nil)
	w.add("volume", "iwato-personal-state", nil, nil)
	w.add("volume", "openshell-sb1-channel", driver, nil)
	w.add("volume", "theirs", map[string]string{ownerKey: strings.Repeat("b", 32)}, nil)
	w.add("volume", "unlabeled", nil, nil)
	w.add("volume", "other-namespace", map[string]string{"openshell.ai/managed-by": "openshell", "openshell.ai/sandbox-namespace": "work"}, nil)
	w.add("container", "iwato-personal-gateway", map[string]string{"iwato.gateway.image": "iwato-personal-gateway:old"}, nil)
	w.add("container", "openshell-dev-supervisor", driver, nil)
	w.add("container", "web", nil, nil)
	w.add("network", "iwato-personal", nil, nil)
	w.add("network", "bridge", nil, nil)
	sandboxID, staleID := "sha256:"+strings.Repeat("a", 64), "sha256:"+strings.Repeat("c", 64)
	w.add("image", sandboxImage, nil, map[string]any{"Id": sandboxID})
	w.add("image", staleID, nil, map[string]any{"Id": staleID})
	w.add("image", "iwato-personal-gateway:old", nil, nil)
	w.add("image", "ubuntu:24.04", nil, map[string]any{"RepoTags": []any{"ubuntu:24.04"}})

	write := func(name, data string) {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{"openshell/gateway-tls/client.key", "openshell/driver-tls/relay.key", "openshell/sandboxes/x/state", "openshell/gateway-build.log",
		"ingress/tailscale/gone/tailscaled.state", "presets/mine.yaml", "profiles/mine.yaml", "iwato.log"} {
		write(f, "x")
	}
	write("images.json", `{"`+sandboxImage+`":{"source":"s","id":"`+sandboxID+`"}}`)
	write(staleImagesFile, `["`+staleID+`"]`)
	configPath := filepath.Join(dir, "iwato.json")
	if err := writeConfig(configPath, c); err != nil {
		t.Fatal(err)
	}

	e := &Engine{config: c, run: func(_ context.Context, a ...string) ([]byte, error) { return w.run(a...) }}
	api := &resetSandboxes{have: []string{"dev", "stray"}}
	ready := make(chan struct{})
	close(ready)
	o := &OpenShell{e: e, dir: filepath.Join(dir, "openshell"), ready: ready, prep: ImageState{Phase: "ready"}, sandboxAPI: api, runner: worldRunner{w}, terms: map[string]map[string]*hostterm.Session{}}
	rt := &resetRT{OpenShell: o}
	s := newServer(c, rt, false, "127.0.0.1:1")
	h := httptest.NewServer(s.Handler())
	t.Cleanup(h.Close)
	u, _ := url.Parse(h.URL)
	s.host, s.origin, s.configPath = u.Host, h.URL, configPath
	if err := s.loadSessions(filepath.Join(dir, "sessions.json")); err != nil {
		t.Fatal(err)
	}
	s.davKeys = &davKeys{path: filepath.Join(dir, "dav-keys.json"), keys: []davKey{{ID: "k", Name: "pc"}}}
	var err error
	if s.push, err = OpenPushStore(filepath.Join(dir, "webpush.json"), "mailto:x"); err != nil {
		t.Fatal(err)
	}
	pushKey := s.push.PublicKey()
	s.ingress.dir = dir
	if err := s.ingress.setSecret("tskey" + "-client-k0123456789abcdefghij"); err != nil {
		t.Fatal(err)
	}
	ts := &forgetProvider{}
	s.ingress.newProvider = func(*IngressConfig, func() string) ingress.Provider { return ts }
	var console bytes.Buffer
	resetOut = &console
	t.Cleanup(func() { resetOut = os.Stdout })

	keyDeleted := 0
	deleteSecretKey = func() error { keyDeleted++; return nil }
	t.Cleanup(func() { deleteSecretKey = protect.DeleteKey })
	// A sign-in in progress.
	flow := &claudeFlow{status: "waiting", ctx: s.oauthLife()}
	s.claude.current = flow

	cookie, csrf := login(t, s)
	if r, _ := request(t, s, "/api/reset", "POST", `{"confirm":"reset"}`, cookie, ""); r.StatusCode != 403 {
		t.Fatal("no CSRF token accepted", r.StatusCode)
	}
	if r, _ := request(t, s, "/api/reset", "POST", `{"confirm":"yes"}`, cookie, csrf); r.StatusCode != 400 {
		t.Fatal("wrong word accepted", r.StatusCode)
	}
	if len(api.deleted) != 0 || len(w.names("volume")) != 8 {
		t.Fatal("deleted before confirmed")
	}
	r, b := request(t, s, "/api/reset", "POST", `{"confirm":"reset"}`, cookie, csrf)
	if r.StatusCode != 200 {
		t.Fatal(r.StatusCode, string(b))
	}
	var res struct{ Warnings []string }
	_ = json.Unmarshal(b, &res)
	if len(res.Warnings) != 0 {
		t.Fatal("warnings:", res.Warnings)
	}

	if got := strings.Join(api.deleted, " "); got != "dev stray" {
		t.Fatal("deleted in OpenShell:", got)
	}
	for kind, want := range map[string]string{"volume": "other-namespace theirs unlabeled", "container": "web", "network": "bridge", "image": "ubuntu:24.04"} {
		if got := strings.Join(w.names(kind), " "); got != want {
			t.Errorf("%ss left: %s, want %s", kind, got, want)
		}
	}
	for _, f := range []string{"openshell/gateway-tls", "openshell/driver-tls", "openshell/sandboxes", "ingress", "images.json", staleImagesFile} {
		if _, err := os.Stat(filepath.Join(dir, f)); !os.IsNotExist(err) {
			t.Errorf("%s stays", f)
		}
	}
	for _, f := range []string{"presets/mine.yaml", "profiles/mine.yaml", "iwato.log", "openshell/gateway-build.log"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("%s is gone", f)
		}
	}
	if info := s.ingress.secretInfo(); info.Set {
		t.Error("the Tailscale secret stays")
	}
	if keyDeleted != 1 {
		t.Error("the Keychain key was deleted times:", keyDeleted)
	}
	if flow.status != "canceled" || flow.ctx.Err() == nil || s.claude.current != nil {
		t.Error("the sign-in in progress goes on:", flow.status)
	}
	slices.Sort(ts.forgot)
	if got := strings.Join(ts.forgot, " "); got != "dev gone" {
		t.Error("Tailscale nodes logged out:", got)
	}
	saved, err := readConfig(configPath)
	if err != nil || len(saved.Sandboxes) != 0 || saved.Instance != c.Instance || saved.Ingress == nil || saved.Ingress.Provider != "tailscale" {
		t.Errorf("iwato.json: %+v %v", saved, err)
	}
	if len(s.davKeys.keys) != 0 || s.push.PublicKey() == pushKey || s.push.Count() != 0 {
		t.Error("app passwords or Web Push keys stay")
	}
	if r, _ := request(t, s, "/api/sandboxes", "GET", "", cookie, ""); r.StatusCode != 401 {
		t.Error("the login stays", r.StatusCode)
	}
	if !strings.Contains(console.String(), "/#token="+s.bootstrap) || s.bootstrap == "" {
		t.Error("no new login URL on the console:", console.String())
	}
	if rt.again != 1 || o.Preparation().Phase != "building" {
		t.Error("not prepared again", rt.again, o.Preparation())
	}

	// With the owner's files: those and the settings go too.
	o.mu.Lock()
	o.prep = ImageState{Phase: "ready"}
	o.mu.Unlock()
	cookie, csrf = login(t, s)
	if r, b := request(t, s, "/api/reset", "POST", `{"confirm":"reset","ownerFiles":true}`, cookie, csrf); r.StatusCode != 200 {
		t.Fatal(r.StatusCode, string(b))
	}
	for _, f := range []string{"presets", "profiles"} {
		if _, err := os.Stat(filepath.Join(dir, f)); !os.IsNotExist(err) {
			t.Errorf("%s stays", f)
		}
	}
	if saved, _ := readConfig(configPath); saved.Ingress != nil || saved.Instance != c.Instance {
		t.Errorf("iwato.json: %+v", saved)
	}
}

// A failed preparation may be reset; a retry from another page while the
// reset runs is refused (it would prepare the gateway beside the reset and
// close the same channel twice), and the reset prepares again once.
func TestRetryDuringReset(t *testing.T) {
	c := testConfig()
	w := &fakeWorld{t: t, objs: map[string]map[string]map[string]any{}}
	e := &Engine{config: c, run: func(_ context.Context, a ...string) ([]byte, error) { return w.run(a...) }}
	api := &resetSandboxes{have: []string{"dev"}, entered: make(chan struct{}), release: make(chan struct{})}
	ready := make(chan struct{})
	close(ready)
	o := &OpenShell{e: e, dir: filepath.Join(t.TempDir(), "openshell"), ready: ready, prep: ImageState{Phase: "failed"}, sandboxAPI: api, runner: worldRunner{w}, terms: map[string]map[string]*hostterm.Session{}}
	rt := &resetRT{OpenShell: o}
	s := newServer(c, rt, false, "127.0.0.1:1")
	h := httptest.NewServer(s.Handler())
	t.Cleanup(h.Close)
	u, _ := url.Parse(h.URL)
	s.host, s.origin = u.Host, h.URL
	resetOut = &bytes.Buffer{}
	t.Cleanup(func() { resetOut = os.Stdout })
	cookie, csrf := login(t, s)
	done := make(chan int)
	go func() {
		r, _ := request(t, s, "/api/reset", "POST", `{"confirm":"reset"}`, cookie, csrf)
		done <- r.StatusCode
	}()
	<-api.entered
	if r, b := request(t, s, "/api/images/retry", "POST", "", cookie, csrf); r.StatusCode != 409 || !strings.Contains(string(b), opBusy["reset"]) {
		t.Error("retry during the reset:", r.StatusCode, string(b))
	}
	if o.RetryPreparation(context.Background()) {
		t.Error("RetryPreparation acted during the reset")
	}
	close(api.release)
	if code := <-done; code != 200 {
		t.Fatal("reset:", code)
	}
	if rt.again != 1 {
		t.Error("prepared again", rt.again, "times")
	}
}
