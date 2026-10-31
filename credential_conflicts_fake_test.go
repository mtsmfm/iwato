//go:build uitest

package main

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// An L4 rule approved earlier for api.anthropic.com: the attach is refused
// with the rules named; the fix removes the endpoint and the attach goes
// through.
func TestProviderAttachRefusesUninspectedRule(t *testing.T) {
	s, d, do := providerServer(t)
	if code, body := do("POST", "/api/sandboxes/dev/start", `{}`); code != 200 {
		t.Fatal(code, body)
	}
	code, body := do("POST", "/api/sandboxes/dev/providers/attach", `{"provider":"claude"}`)
	var resp struct {
		Error     string
		Conflicts []CredentialConflict
	}
	if code != 409 || json.Unmarshal([]byte(body), &resp) != nil || len(resp.Conflicts) != 1 || resp.Conflicts[0].Rule != "allow_api_anthropic_com_443" || !strings.Contains(resp.Error, "サンドボックスを起動できなくなります") {
		t.Fatal(code, body)
	}
	if _, attached, _ := d.ListProviders(context.Background()); slices.Contains(attached["dev"], "claude") {
		t.Fatal("attached anyway")
	}
	if code, body := do("POST", "/api/sandboxes/dev/providers/fix", `{"provider":"claude"}`); code != 200 || !strings.Contains(body, `"rule":"allow_api_anthropic_com_443"`) {
		t.Fatal(code, body)
	}
	sb, _ := s.lookupSandbox("dev")
	rules, _ := d.PolicyRules(context.Background(), sb)
	if _, ok := rules["allow_api_anthropic_com_443"]; ok {
		t.Fatal("the endpoint is still there")
	}
	if code, body := do("POST", "/api/sandboxes/dev/providers/attach", `{"provider":"claude"}`); code != 200 {
		t.Fatal(code, body)
	}
	// Nothing left to fix.
	if code, body := do("POST", "/api/sandboxes/dev/providers/fix", `{"provider":"claude"}`); code != 200 || !strings.Contains(body, `"removed":null`) {
		t.Fatal(code, body)
	}
	// While it is attached, neither a hand-made rule nor an approval as
	// proposed may bring an uninspected one back.
	code, body = do("POST", "/api/sandboxes/dev/policy/add", `{"rule":"","binaries":["/usr/bin/curl"],"host":"api.anthropic.com","ports":[443]}`)
	if code != 409 || !strings.Contains(body, "検査なしで許可することになります") {
		t.Fatal(code, body)
	}
	if code, body := do("POST", "/api/sandboxes/dev/policy/add", `{"rule":"","binaries":["/usr/bin/curl"],"host":"example.org","ports":[443]}`); code != 200 {
		t.Fatal("an unrelated L4 rule:", code, body)
	}
}

// The fake's proposal (gh to uploads.github.com, L4) while a GitHub provider
// is attached: approving it as proposed, or with only its program changed
// (the approve dialog's Binaries keep the endpoints), is refused; narrowed
// to reads it goes through.
func TestApprovalRefusesUninspectedCredentialRule(t *testing.T) {
	s, d, do := providerServer(t)
	if code, body := do("POST", "/api/sandboxes/dev/start", `{}`); code != 200 {
		t.Fatal(code, body)
	}
	if code, body := do("POST", "/api/sandboxes/dev/providers/attach", `{"provider":"github-personal"}`); code != 200 {
		t.Fatal(code, body)
	}
	for _, scope := range []string{"", `,"scope":{}`} {
		code, body := do("POST", "/api/sandboxes/dev/proposals/approve", `{"requestId":"`+requestID("fake-uploads")+`"`+digestField(s.rt, requestID("fake-uploads"))+``+scope+`}`)
		if code != 409 || !strings.Contains(body, "github-personal (GitHub)") || !strings.Contains(body, "メソッドとパスを選んで") {
			t.Fatal(scope, code, body)
		}
	}
	// The git push proposal is inspected as proposed: no conflict.
	if code, body := do("POST", "/api/sandboxes/dev/proposals/approve", `{"requestId":"`+requestID("fake-git")+`"`+digestField(s.rt, requestID("fake-git"))+`}`); code != 200 {
		t.Fatal(code, body)
	}
	if code, body := do("POST", "/api/sandboxes/dev/proposals/approve", `{"requestId":"`+requestID("fake-uploads")+`"`+digestField(s.rt, requestID("fake-uploads"))+`,"scope":{"rules":[{"method":"GET","path":"/**"}]}}`); code != 200 {
		t.Fatal(code, body)
	}
	sb, _ := s.lookupSandbox("dev")
	rules, _ := d.PolicyRules(context.Background(), sb)
	r := rules["allow_uploads_github_com_443"]
	if e := r.Endpoints; len(e) != 1 || e[0].Protocol != "rest" || len(e[0].Rules) != 1 || len(r.Binaries) != 1 || r.Binaries[0].Path != "/**" {
		t.Fatalf("%+v", r)
	}
}
