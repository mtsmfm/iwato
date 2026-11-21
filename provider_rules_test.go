package main

import (
	"testing"

	v1 "github.com/ai-openshell/sdk-go/openshell/v1"
)

// aliasProviderRules gives a test copy of a built-in profile (another id)
// that profile's initial rules.
func aliasProviderRules(t *testing.T, alias, base string) {
	t.Helper()
	m := providerRulePresets()
	m[alias] = m[base]
	t.Cleanup(func() { delete(m, alias) })
}

// A credential host may be a pattern; "*" stays within one label.
func TestCredentialHostPattern(t *testing.T) {
	prof := providerProfile{CredentialHosts: []string{"api.modal.com:443", "input-plane.*.modal.com:443"}}
	for host, want := range map[string]bool{
		"api.modal.com": true, "input-plane.us-east.modal.com": true, "Input-Plane.eu.modal.com": true,
		"input-plane.*.modal.com": true, "input-plane.a.b.modal.com": false, "input-plane.modal.com": false,
		"evil.modal.com": false, "input-plane.us-east.modal.com.evil.example": false,
	} {
		if got := credentialHost(prof, v1.PolicyNetworkEndpoint{Host: host, Port: 443}); got != want {
			t.Errorf("%s: %v", host, got)
		}
	}
	if credentialHost(prof, v1.PolicyNetworkEndpoint{Host: "input-plane.us-east.modal.com", Port: 8443}) {
		t.Error("another port")
	}
}
