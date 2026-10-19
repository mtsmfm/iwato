package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	v1 "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1"
	pb "github.com/NVIDIA/OpenShell/sdk/go/proto/openshellv1"

	"iwato/internal/hostterm"
	"iwato/internal/openshell/client"
)

// TestOpenShellCredentialConflictE2E reproduces, against the gateway that
// internal/openshell/wslc/e2e.sh starts (MODE=tls), an L4 rule for
// api.anthropic.com approved before the Claude provider. It
// shows what OpenShell does with it and that Iwato refuses the attach,
// fixes it, and keeps the conflict from coming back.
func TestOpenShellCredentialConflictE2E(t *testing.T) {
	addr := os.Getenv("OPENSHELL_E2E_ADDR")
	if addr == "" {
		t.Skip("run by internal/openshell/wslc/e2e.sh")
	}
	tlsDir := os.Getenv("OPENSHELL_E2E_TLS")
	ca, cert, key := filepath.Join(tlsDir, "ca.crt"), filepath.Join(tlsDir, "client", "tls.crt"), filepath.Join(tlsDir, "client", "tls.key")
	c := defaultConfig()
	c.WSLC = os.Getenv("OPENSHELL_E2E_WSLC")
	b, err := NewEngine(c)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := client.Dial(addr, ca, cert, key)
	if err != nil {
		t.Fatal(err)
	}
	sdk, err := v1.NewClient(v1.Config{Address: addr, TLS: &v1.TLSConfig{CAFile: ca, CertFile: cert, KeyFile: key}})
	if err != nil {
		t.Fatal(err)
	}
	o := &OpenShell{e: b, ready: make(chan struct{}), sdk: sdk, conn: conn, env: termEnv, terms: map[string]map[string]*hostterm.Session{}}
	close(o.ready)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := ensureProfiles(ctx, pb.NewOpenShellClient(conn)); err != nil {
		t.Fatal(err)
	}
	o.profilesDone = true
	cl, _ := profileByID("iwato-claude")
	creds, _ := profileCredentials(cl, map[string]string{"oauth_token": "sk-ant-oat01-e2eSECRET"})
	if err := o.CreateProvider(ctx, "claude-conflict", "iwato-claude", creds); err != nil {
		t.Fatal(err)
	}
	defer o.DeleteProvider(context.Background(), "claude-conflict")

	w := newSandbox("iwato-conflict-e2e")
	if err := o.Start(ctx, w); err != nil {
		t.Fatal("start:", err)
	}
	defer o.Delete(context.Background(), w, true)
	// Approved before the provider: curl to api.anthropic.com, L4.
	if _, err := o.EditPolicy(ctx, w, PolicyEdit{Op: "add", Binaries: []string{"/usr/bin/curl"}, Host: "api.anthropic.com", Ports: []uint32{443}}); err != nil {
		t.Fatal("the L4 rule:", err)
	}

	c.Sandboxes = []Sandbox{w}
	s := newServer(c, o, false, "127.0.0.1:29999")
	cookie, csrf := reviewLogin(t, s)
	do := func(method, path, body string) (int, string) {
		r := reviewRequest(s, method, path, body, s.origin, "same-origin", s.host, cookie, csrf)
		return r.Code, r.Body.String()
	}
	code, body := do("POST", "/api/sandboxes/"+w.Name+"/providers/attach", `{"provider":"claude-conflict"}`)
	var resp struct{ Conflicts []CredentialConflict }
	if code != 409 || json.Unmarshal([]byte(body), &resp) != nil || len(resp.Conflicts) != 1 || resp.Conflicts[0].Rule != "allow_api_anthropic_com_443" {
		t.Fatal("Iwato attached over the L4 rule:", code, body)
	}
	if _, attached, _ := o.ListProviders(ctx); len(attached[w.Name]) != 0 {
		t.Fatal("attached anyway:", attached[w.Name])
	}
	if code, body := do("POST", "/api/sandboxes/"+w.Name+"/providers/fix", `{"provider":"claude-conflict"}`); code != 200 {
		t.Fatal("fix:", code, body)
	}
	code, body = do("POST", "/api/sandboxes/"+w.Name+"/providers/attach", `{"provider":"claude-conflict"}`)
	if code != 200 {
		t.Fatal("attach after the fix:", code, body)
	}
	// The sandbox applies it (the gateway accepts its effective policy).
	sb, _ := s.lookupSandbox(w.Name)
	if len(sb.ProviderApplying) != 1 {
		t.Fatalf("%+v", sb.ProviderApplying)
	}
	deadline := time.Now().Add(60 * time.Second)
	for {
		state, reason, err := o.ProviderReadiness(ctx, w.Name, "claude-conflict", sb.ProviderApplying[0].Receipt)
		if err != nil {
			t.Fatal(err)
		}
		if state == "ready" {
			break
		}
		if state == "failed" || state == "withheld" || time.Now().After(deadline) {
			t.Fatalf("after the fix: %s (%s)", state, reason)
		}
		time.Sleep(500 * time.Millisecond)
	}
	// While attached: Iwato refuses an L4 rule for the host, and so does
	// OpenShell's own merge validation.
	if code, body := do("POST", "/api/sandboxes/"+w.Name+"/policy/add", `{"rule":"","binaries":["/usr/bin/curl"],"host":"api.anthropic.com","ports":[443]}`); code != 409 || !strings.Contains(body, "検査なし") {
		t.Fatal("Iwato took an L4 rule:", code, body)
	}
	_, err = o.EditPolicy(ctx, w, PolicyEdit{Op: "add", Binaries: []string{"/usr/bin/curl"}, Host: "api.anthropic.com", Ports: []uint32{443}})
	t.Logf("OpenShell's answer to the same rule: %v", err)
	if err == nil || !strings.Contains(err.Error(), "credentialed endpoint") {
		t.Fatal("OpenShell took the L4 rule:", err)
	}
	// Inspected too, Iwato refuses it: the provider's host is the
	// provider's (ADR 0006).
	if code, body := do("POST", "/api/sandboxes/"+w.Name+"/policy/add", `{"rule":"","binaries":["/usr/bin/curl"],"host":"api.anthropic.com","ports":[443],"access":"read-only"}`); code != 409 || !strings.Contains(body, `"code":"policy_provider_host"`) {
		t.Fatal("an inspected rule:", code, body)
	}

	// OpenShell's gap (why Iwato checks before attaching): its attach
	// checks the creation policy only, not the approved revisions.
	if ok, _, err := o.DetachProvider(ctx, w.Name, "claude-conflict"); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if _, err := o.EditPolicy(ctx, w, PolicyEdit{Op: "add", Rule: "l4_again", Binaries: []string{"/usr/bin/curl"}, Host: "api.anthropic.com", Ports: []uint32{443}}); err != nil {
		t.Fatal("the L4 rule without the provider:", err)
	}
	ok, receipt, err := o.AttachProvider(ctx, w.Name, "claude-conflict")
	t.Logf("OpenShell's own attach over an L4 rule: ok=%v err=%v", ok, err)
	if err == nil && receipt != "" {
		state, reason := "", ""
		for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(time.Second) {
			if state, reason, _ = o.ProviderReadiness(ctx, w.Name, "claude-conflict", receipt); state != "pending" && state != "persisted" {
				break
			}
		}
		t.Logf("then the sandbox: %s (%s)", state, reason)
	}
}
