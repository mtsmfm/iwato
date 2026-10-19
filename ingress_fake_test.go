//go:build uitest

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"iwato/internal/ingress"
	"iwato/internal/protect"
)

func TestIngressSecretStorage(t *testing.T) {
	s := newServer(testConfig(), NewFake(), false, "127.0.0.1:1")
	m := s.ingress
	m.dir = t.TempDir()
	if info := m.secretInfo(); info.Set {
		t.Fatal(info)
	}
	if validSecret("tskey-client-k123-abcdefgh?ephemeral=true") == nil || validSecret("hello") == nil {
		t.Fatal("bad secrets accepted")
	}
	secret := "tskey-client-kXYZ-0123456789abcdWXYZ"
	if err := validSecret(secret); err != nil {
		t.Fatal(err)
	}
	if err := m.setSecret(secret); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(m.dir, ingressSecretFile))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("%v %v", fi, err)
	}
	if b, _ := os.ReadFile(filepath.Join(m.dir, "iwato.json")); len(b) != 0 {
		t.Fatal("the secret went to the configuration")
	}
	info := m.secretInfo()
	if !info.Set || info.Kind != "oauth" || info.Hint != "tskey-client-…WXYZ" || info.Source != "file" || info.Storage != protect.Scheme {
		t.Fatalf("%+v", info)
	}
	t.Setenv(ingressSecretEnv, "tskey-auth-fromtheenvironment1")
	if v, src, _ := m.secret(); v != "tskey-auth-fromtheenvironment1" || src != "env" {
		t.Fatal(v, src)
	}
	t.Setenv(ingressSecretEnv, "")
	m.setSecret("")
	if info := m.secretInfo(); info.Set {
		t.Fatal("deleted secret still set")
	}
}

func TestIngressLifecycle(t *testing.T) {
	s, _ := newTestServer(t)
	cookie, csrf := login(t, s)
	var made []*recordingProvider
	s.ingress.later = func(f func()) { f() }
	s.ingress.newProvider = func(*IngressConfig, func() string) ingress.Provider {
		p := &recordingProvider{}
		made = append(made, p)
		return p
	}
	ctx := context.Background()
	s.ingress.reconfigure(ctx)
	if _, ok := s.ingress.provider.(hostTaker); len(made) != 0 || !ok || s.ingress.overall().State != "on" {
		t.Fatal("local by default", s.ingress.overall())
	}
	_, b := request(t, s, "/api/sandboxes/dev/ports", "GET", "", cookie, "")
	if !strings.Contains(string(b), `"provider":"local"`) {
		t.Fatal(string(b))
	}
	// Tailscale needs owners; without a secret nothing is made.
	if res, b := request(t, s, "/api/ingress", "POST", `{"provider":"tailscale","tag":"tag:iwato","hostnamePrefix":"iwato-","owners":[]}`, cookie, csrf); res.StatusCode != 400 {
		t.Fatal(res.StatusCode, string(b))
	}
	if res, b := request(t, s, "/api/ingress", "POST", `{"provider":"tailscale","tag":"tag:iwato","hostnamePrefix":"iwato-","owners":[" alice@github ","alice@github"]}`, cookie, csrf); res.StatusCode != 200 {
		t.Fatal(res.StatusCode, string(b))
	}
	if o := s.config.Ingress.owners(); len(o) != 1 || o[0] != "alice@github" || s.config.Ingress.Tailscale.HostnamePrefix != nil {
		t.Fatalf("%+v", s.config.Ingress.Tailscale)
	}
	s.ingress.reconfigure(ctx)
	if st := s.ingress.overall(); st.State != "needs-secret" || len(made) != 0 || s.ingress.provider != nil {
		t.Fatal(st)
	}
	if res, b := request(t, s, "/api/ingress/secret", "POST", `{"secret":"tskey-auth-k1-0123456789"}`, cookie, csrf); res.StatusCode != 200 || strings.Contains(string(b), "0123456789\"") {
		t.Fatal(res.StatusCode, string(b))
	}
	s.ingress.reconfigure(ctx)
	if len(made) != 1 || !made[0].started || s.ingress.overall().State != "on" {
		t.Fatalf("%d providers, %+v", len(made), s.ingress.overall())
	}
	p := made[0]
	if l := p.last(); len(l) != 1 || l[0] != (ingress.SandboxState{Name: "dev", Running: false, Exposed: true}) {
		t.Fatalf("%+v", l)
	}
	// Start: the next sync says running; the backend lets connections in.
	if res, _ := request(t, s, "/api/sandboxes/dev/start", "POST", "{}", cookie, csrf); res.StatusCode != 200 {
		t.Fatal(res.StatusCode)
	}
	s.ingress.sync(ctx, false)
	if l := p.last(); !l[0].Running || !s.ingress.isRunning("dev") {
		t.Fatalf("%+v", l)
	}
	n := len(p.syncs)
	s.ingress.sync(ctx, false)
	if len(p.syncs) != n {
		t.Fatal("an unchanged state synced again")
	}
	// The list the page polls feeds it too.
	if res, _ := request(t, s, "/api/sandboxes/dev/stop", "POST", "{}", cookie, csrf); res.StatusCode != 200 {
		t.Fatal(res.StatusCode)
	}
	request(t, s, "/api/sandboxes", "GET", "", cookie, "")
	for deadline := time.Now().Add(2 * time.Second); s.ingress.isRunning("dev") && time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
	}
	if s.ingress.isRunning("dev") {
		t.Fatal("a stopped sandbox still counts as running")
	}
	// The ports tab: the node and the URLs (here stopped: none listed).
	res, b := request(t, s, "/api/sandboxes/dev/ports", "GET", "", cookie, "")
	if res.StatusCode != 200 || !strings.Contains(string(b), `"running":false`) || !strings.Contains(string(b), `"state":"connected"`) {
		t.Fatal(res.StatusCode, string(b))
	}
	request(t, s, "/api/sandboxes/dev/start", "POST", "{}", cookie, csrf)
	_, b = request(t, s, "/api/sandboxes/dev/ports", "GET", "", cookie, "")
	if !strings.Contains(string(b), `"url":"https://iwato-dev.tail0000.ts.net:5173/"`) || !strings.Contains(string(b), `"ipv6Only":true`) {
		t.Fatal(string(b))
	}
	// The tailnet's connections go in only to the listed ports.
	s.ingress.sync(ctx, false)
	if !s.ingress.forwarded(ingress.Target{Sandbox: "dev", Port: 5173}) || s.ingress.forwarded(ingress.Target{Sandbox: "dev", Port: 9999}) {
		t.Fatal("forwarded must follow the listening ports")
	}
	// A new prefix makes a new provider; delete forgets the node.
	request(t, s, "/api/ingress", "POST", `{"provider":"tailscale","tag":"tag:iwato","hostnamePrefix":"me-","owners":["alice@github"]}`, cookie, csrf)
	s.ingress.reconfigure(ctx)
	if len(made) != 2 || !p.closed {
		t.Fatal("the provider was not replaced")
	}
	if res, _ := request(t, s, "/api/sandboxes/dev/delete", "POST", `{"confirm":"dev"}`, cookie, csrf); res.StatusCode != 200 {
		t.Fatal(res.StatusCode)
	}
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		made[1].mu.Lock()
		done := len(made[1].forgot) == 1
		made[1].mu.Unlock()
		if done {
			break
		}
	}
	if made[1].forgot[0] != "dev" {
		t.Fatal(made[1].forgot)
	}
	// Removing the secret turns it off.
	request(t, s, "/api/ingress/secret/delete", "POST", "{}", cookie, csrf)
	s.ingress.reconfigure(ctx)
	if !made[1].closed || s.ingress.overall().State != "needs-secret" {
		t.Fatal(s.ingress.overall())
	}
	// The settings never show the secret.
	_, b = request(t, s, "/api/ingress", "GET", "", cookie, "")
	if !strings.Contains(string(b), `"suggestedOwners":["owner@github"]`) || !strings.Contains(string(b), `"hostnamePrefix":"me-"`) {
		t.Fatal(string(b))
	}
	// Back to local (not written: the default), the Tailscale settings kept.
	request(t, s, "/api/ingress", "POST", `{"provider":"local","tag":"tag:iwato","hostnamePrefix":"me-","owners":["alice@github"]}`, cookie, csrf)
	s.ingress.reconfigure(ctx)
	if _, ok := s.ingress.provider.(hostTaker); !ok || s.config.Ingress.Provider != "" || s.config.Ingress.prefix() != "me-" {
		t.Fatalf("%+v", s.config.Ingress)
	}
}
