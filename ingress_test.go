package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"iwato/internal/ingress"
)

func TestIngressConfigParsing(t *testing.T) {
	dir := t.TempDir()
	write := func(ingressJSON, sandboxJSON string) error {
		c := defaultConfig()
		b, _ := json.Marshal(c)
		s := strings.Replace(string(b), `"sandboxes":[]`, `"ingress":`+ingressJSON+`,"sandboxes":[{"name":"dev","ingress":`+sandboxJSON+`}]`, 1)
		p := filepath.Join(dir, "iwato.json")
		os.WriteFile(p, []byte(s), 0o600)
		_, err := readConfig(p)
		return err
	}
	good := `{"provider":"tailscale","auth":"provider","host":"localhost","tailscale":{"tag":"tag:iwato","hostnamePrefix":"iwato-","owners":["alice@github"]}}`
	if err := write(good, `{"disabled":false,"ports":{"5432":{"mode":"tcp"},"3001":{"host":"preserve"},"9229":{"disabled":true}}}`); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ ing, sb, want string }{
		{`{"provider":"cloudflare"}`, `{}`, "unknown ingress provider"},
		{`{"auth":"iwato"}`, `{}`, "not supported yet"},
		{`{"tailscale":{"tag":"iwato"}}`, `{}`, "tag:iwato"},
		{`{"tailscale":{"hostnamePrefix":"Iwato_"}}`, `{}`, "hostnamePrefix"},
		{`{"tailscale":{"owners":["a b"]}}`, `{}`, "owner"},
		{`{}`, `{"ports":{"0":{}}}`, "invalid port"},
		{`{}`, `{"ports":{"080":{}}}`, "invalid port"},
		{`{}`, `{"ports":{"80":{"mode":"udp"}}}`, "invalid mode"},
		{`{}`, `{"auth":"both"}`, "not supported yet"},
		{`{"unknown":1}`, `{}`, "unknown field"},
	} {
		if err := write(c.ing, c.sb); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s %s: %v", c.ing, c.sb, err)
		}
	}
	cfg := &IngressConfig{Host: "forwarded"}
	sb := Sandbox{Name: "dev", Ingress: &SandboxIngress{Ports: map[string]PortIngress{"3001": {Host: "preserve", Mode: "http"}, "9229": {Disabled: true}}}}
	if o, ok := portOptions(cfg, sb, 3000); !ok || o.Host != ingress.HostForwarded {
		t.Fatal(o)
	}
	if o, _ := portOptions(cfg, sb, 3001); o.Host != ingress.HostPreserve || o.Mode != ingress.ModeHTTP {
		t.Fatal(o)
	}
	if o, _ := portOptions(nil, sb, 9229); !o.Disabled {
		t.Fatal(o)
	}
	sb.Ingress.Disabled = true
	if _, ok := portOptions(cfg, sb, 3000); ok {
		t.Fatal("a disabled sandbox is exposed")
	}
	if (*IngressConfig)(nil).prefix() != "iwato-" || (*IngressConfig)(nil).tag() != "tag:iwato" {
		t.Fatal("defaults")
	}
	empty := ""
	if (&IngressConfig{Tailscale: &TailscaleIngressConfig{HostnamePrefix: &empty}}).prefix() != "" {
		t.Fatal("an empty prefix is allowed")
	}
}

// recordingProvider records Syncs and Forgets.
type recordingProvider struct {
	mu      sync.Mutex
	syncs   [][]ingress.SandboxState
	forgot  []string
	closed  bool
	started bool
}

func (p *recordingProvider) Name() string { return "tailscale" }
func (p *recordingProvider) Start(context.Context, ingress.Core) error {
	p.started = true
	return nil
}
func (p *recordingProvider) Sync(_ context.Context, sbs []ingress.SandboxState) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.syncs = append(p.syncs, sbs)
	return nil
}
func (p *recordingProvider) Forget(_ context.Context, sb string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.forgot = append(p.forgot, sb)
	return nil
}
func (p *recordingProvider) URL(t ingress.Target) (string, bool) {
	return "https://iwato-" + t.Sandbox + ".tail0000.ts.net:" + itoa(int(t.Port)) + "/", true
}
func (p *recordingProvider) Status(string) ingress.Status {
	return ingress.Status{State: "connected", DNSName: "x"}
}
func (p *recordingProvider) HasIdentity() bool { return true }
func (p *recordingProvider) Close() error      { p.closed = true; return nil }

func (p *recordingProvider) last() []ingress.SandboxState {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.syncs) == 0 {
		return nil
	}
	return p.syncs[len(p.syncs)-1]
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func TestParseSelfLogin(t *testing.T) {
	out := []byte(`{"Self":{"UserID":12345,"HostName":"pc"},"User":{"12345":{"ID":12345,"LoginName":"alice@github"},"9":{"LoginName":"bob@github"}}}`)
	if got := parseSelfLogin(out); got != "alice@github" {
		t.Fatal(got)
	}
	if parseSelfLogin([]byte(`{}`)) != "" {
		t.Fatal()
	}
}
