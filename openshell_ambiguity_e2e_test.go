package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	v1 "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1"
	pb "github.com/NVIDIA/OpenShell/sdk/go/proto/openshellv1"

	"iwato/internal/hostterm"
	"iwato/internal/openshell/client"
)

// gatewayPairs reads the pairs of OpenShell's ambiguity refusal: the two
// rules (sorted) and the conflicting fields, one string each.
func gatewayPairs(msg string) []string {
	var out []string
	idx := gatewayAmbiguity.FindAllStringSubmatchIndex(msg, -1)
	for i, m := range idx {
		end := len(msg)
		if i+1 < len(idx) {
			end = idx[i+1][0]
		}
		rules := []string{msg[m[2]:m[3]], msg[m[8]:m[9]]}
		slices.Sort(rules)
		var fields []string
		for _, c := range strings.Split(strings.TrimRight(strings.TrimSpace(msg[m[1]:end]), ";"), "; ") {
			f, _, _ := strings.Cut(strings.TrimSpace(c), "=")
			fields = append(fields, f)
		}
		slices.Sort(fields)
		out = append(out, strings.Join(rules, "+")+":"+strings.Join(fields, ","))
	}
	slices.Sort(out)
	return out
}

// viewPairs are Iwato's predicted pairs in the same form.
func viewPairs(vs []AmbiguityView) []string {
	var out []string
	for _, v := range vs {
		rules := []string{v.Rule, v.With}
		slices.Sort(rules)
		fields := slices.Clone(v.Fields)
		slices.Sort(fields)
		out = append(out, strings.Join(rules, "+")+":"+strings.Join(fields, ","))
	}
	slices.Sort(out)
	return out
}

// TestOpenShellAmbiguityE2E checks, against the gateway that
// internal/openshell/wslc/e2e.sh starts (MODE=tls), that Iwato's
// prediction of OpenShell's endpoint ambiguity check matches the gateway's
// own refusal: a hand rule auditing api.github.com while the GitHub
// provider's rule enforces it, and the provider attached over such a rule.
func TestOpenShellAmbiguityE2E(t *testing.T) {
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
	gh, _ := profileByID("iwato-github")
	creds, _ := profileCredentials(gh, map[string]string{"token": "ghp_e2eSECRETambiguity"})
	if err := o.CreateProvider(ctx, "github-amb", "iwato-github", creds); err != nil {
		t.Fatal(err)
	}
	defer o.DeleteProvider(context.Background(), "github-amb")

	w := newSandbox("iwato-ambiguity-e2e")
	if err := o.Start(ctx, w); err != nil {
		t.Fatal("start:", err)
	}
	defer o.Delete(context.Background(), w, true)
	c.Sandboxes = []Sandbox{w}
	s := newServer(c, o, false, "127.0.0.1:29999")
	cookie, csrf := reviewLogin(t, s)
	do := func(method, path, body string) (int, string) {
		r := reviewRequest(s, method, path, body, s.origin, "same-origin", s.host, cookie, csrf)
		return r.Code, r.Body.String()
	}
	if code, body := do("POST", "/api/sandboxes/"+w.Name+"/providers/attach", `{"provider":"github-amb"}`); code != 200 {
		t.Fatal("attach:", code, body)
	}

	// A rule auditing api.github.com: Iwato refuses it, and so does the
	// gateway, for the same pair and fields.
	audited := PolicyEdit{Op: "add", Rule: "gh_audit", Binaries: []string{"/usr/bin/curl"}, Host: "api.github.com", Ports: []uint32{443}, Access: "full", Audit: true}
	body, _ := json.Marshal(audited)
	code, resp := do("POST", "/api/sandboxes/"+w.Name+"/policy/add", string(body))
	var refused struct {
		Error       string
		Ambiguities []AmbiguityView
	}
	if code != 409 || json.Unmarshal([]byte(resp), &refused) != nil || len(refused.Ambiguities) == 0 {
		t.Fatal("Iwato took the audited rule:", code, resp)
	}
	_, err = o.EditPolicy(ctx, w, audited)
	t.Logf("OpenShell's answer to the audited rule: %v", err)
	if err == nil || !strings.Contains(err.Error(), "network endpoint ambiguity validation failed") {
		t.Fatal("OpenShell took the audited rule:", err)
	}
	if got, want := gatewayPairs(err.Error()), viewPairs(refused.Ambiguities); !slices.Equal(got, want) {
		t.Fatalf("the gateway refused %v, Iwato predicted %v", got, want)
	}
	if msg, ok := translateGatewayError(safeCLIError(err), s.ruleProviders(ctx, w)); !ok || !strings.Contains(msg.text, "プロバイダー github-amb (GitHub) の規則 _provider_github_amb") {
		t.Fatal("translation:", msg)
	}
	// Enforced, the gateway takes it; Iwato refuses it all the same, as
	// the provider's host is the provider's (ADR 0006).
	enforced := audited
	enforced.Rule, enforced.Audit = "gh_enforce", false
	body, _ = json.Marshal(enforced)
	if code, resp := do("POST", "/api/sandboxes/"+w.Name+"/policy/add", string(body)); code != 409 || !strings.Contains(resp, `"code":"policy_provider_host"`) {
		t.Fatal("the enforced rule:", code, resp)
	}
	if _, err := o.EditPolicy(ctx, w, enforced); err != nil {
		t.Fatal("the gateway refused the enforced rule:", err)
	}

	// The provider attached over an audited rule: refused by Iwato and by
	// the gateway's attachment preflight alike; switched to enforce, on.
	if ok, _, err := o.DetachProvider(ctx, w.Name, "github-amb"); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if code, resp := do("POST", "/api/sandboxes/"+w.Name+"/policy/remove", `{"rule":"gh_enforce"}`); code != 200 {
		t.Fatal(code, resp)
	}
	// A rule from before (by hand Iwato refuses it, provider or not).
	if _, err := o.EditPolicy(ctx, w, audited); err != nil {
		t.Fatal("the audited rule without the provider:", err)
	}
	code, resp = do("POST", "/api/sandboxes/"+w.Name+"/providers/attach", `{"provider":"github-amb"}`)
	refused.Ambiguities = nil
	if code != 409 || json.Unmarshal([]byte(resp), &refused) != nil || len(refused.Ambiguities) == 0 || refused.Ambiguities[0].Fix != "enforce" {
		t.Fatal("Iwato attached over the audited rule:", code, resp)
	}
	_, _, err = o.AttachProvider(ctx, w.Name, "github-amb")
	t.Logf("OpenShell's own attach over the audited rule: %v", err)
	if err == nil {
		o.DetachProvider(context.Background(), w.Name, "github-amb")
		t.Fatal("OpenShell attached over the audited rule")
	}
	var plain interface{ Error() string } = err
	if errors.Unwrap(err) != nil {
		plain = errors.Unwrap(err)
	}
	if got, want := gatewayPairs(plain.Error()), viewPairs(refused.Ambiguities); !slices.Equal(got, want) {
		t.Fatalf("the gateway refused %v, Iwato predicted %v", got, want)
	}
	if code, resp := do("POST", "/api/sandboxes/"+w.Name+"/providers/fix", `{"provider":"github-amb","mode":"enforce"}`); code != 200 {
		t.Fatal("fix:", code, resp)
	}
	// Enforced, it still gives the token full access, beyond the
	// provider's read-only rule: Iwato refuses until it is gone.
	if code, resp := do("POST", "/api/sandboxes/"+w.Name+"/providers/attach", `{"provider":"github-amb"}`); code != 409 || !strings.Contains(resp, `"code":"provider_attach_beyond_scope"`) {
		t.Fatal("attach over a rule beyond the scope:", code, resp)
	}
	if code, resp := do("POST", "/api/sandboxes/"+w.Name+"/policy/remove", `{"rule":"gh_audit"}`); code != 200 {
		t.Fatal(code, resp)
	}
	if code, resp := do("POST", "/api/sandboxes/"+w.Name+"/providers/attach", `{"provider":"github-amb"}`); code != 200 {
		t.Fatal("attach after the fix:", code, resp)
	}
}
