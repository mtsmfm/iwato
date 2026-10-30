package main

import (
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	v1 "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1"
)

// initialEndpoints are the endpoints of the profile's initial rule
// (presets/providers), each bound to the provider.
func initialEndpoints(t *testing.T, id string) []v1.PolicyNetworkEndpoint {
	t.Helper()
	rules, err := providerInitialRules(id, "p")
	if err != nil || len(rules) != 1 {
		t.Fatal(id, rules, err)
	}
	for _, r := range rules {
		if len(r.Binaries) != 1 || r.Binaries[0].Path != "/**" {
			t.Errorf("%s: %v", id, r.Binaries)
		}
		for _, e := range r.Endpoints {
			if e.CredentialBinding == nil || e.CredentialBinding.Provider != "p" {
				t.Errorf("%s: %s not bound", id, e.Host)
			}
		}
		return r.Endpoints
	}
	return nil
}

func mustProfile(t *testing.T, id string) providerProfile {
	t.Helper()
	p, ok := profileByID(id)
	if !ok {
		t.Fatal("no profile", id)
	}
	return p
}

// The supervisor offers HTTP/2 to exactly iwato-modal's endpoints.
func TestHTTP2EndpointsAreModals(t *testing.T) {
	b, err := os.ReadFile("internal/openshell/gateway/Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	got := regexp.MustCompile(`OPENSHELL_HTTP2_ENDPOINTS=(\S+)`).FindAllSubmatch(b, -1)
	var want []string
	for _, e := range initialEndpoints(t, "iwato-modal") {
		want = append(want, fmt.Sprintf("%s:%d", e.Host, e.Port))
	}
	if len(got) != 1 || string(got[0][1]) != strings.Join(want, ",") {
		t.Errorf("OPENSHELL_HTTP2_ENDPOINTS in the Dockerfile: %q, want %q", got, strings.Join(want, ","))
	}
}

// The page's metadata agrees with the YAML: the note names every variable
// the sandbox gets, the groups come in profileGroups' order, and every
// endpoint of the initial rules is inspected (OpenShell binds no other,
// and Iwato never sets allow_uninspected_credentials) and on one of the
// credential's hosts.
func TestProfileMetadata(t *testing.T) {
	files, err := profileFiles.ReadDir("profiles")
	if err != nil || len(files) != len(providerProfiles) {
		t.Fatalf("%d files, %d profiles (%v)", len(files), len(providerProfiles), err)
	}
	for _, f := range files {
		if _, ok := profileByID(strings.TrimSuffix(f.Name(), ".yaml")); !ok {
			t.Errorf("profiles/%s has no metadata", f.Name())
		}
	}
	last := 0
	for _, p := range providerProfiles {
		g := slices.Index(profileGroups, p.Group)
		if g < 0 || g < last {
			t.Errorf("%s: group %q out of order", p.ID, p.Group)
		}
		last = g
		if p.Label == "" || p.Note == "" || p.DefaultName == "" || len(p.CredentialHosts) == 0 {
			t.Errorf("%s: %+v", p.ID, p)
		}
		m := p.proto()
		for i, c := range m.Credentials {
			if !slices.Equal(p.Credentials[i].Env, c.EnvVars) || p.Credentials[i].Label == "" {
				t.Errorf("%s: credential %s: %+v", p.ID, c.Name, p.Credentials[i])
			}
			for _, env := range c.EnvVars {
				if !strings.Contains(p.Note, env) {
					t.Errorf("%s: the note does not name %s", p.ID, env)
				}
			}
		}
		var hosts []string
		for _, e := range initialEndpoints(t, p.ID) {
			if !bindable(e) {
				t.Errorf("%s: %s%s is not inspected", p.ID, e.Host, e.Path)
			}
			if e.Enforcement != v1.NetworkEnforcementModeEnforce {
				t.Errorf("%s: %s%s is not enforced", p.ID, e.Host, e.Path)
			}
			if e.Path == "" {
				t.Errorf("%s: %s has no path (equal specificity with rules without one)", p.ID, e.Host)
			}
			if !credentialHost(p, e) {
				t.Errorf("%s: %s is not one of its credential hosts", p.ID, e.Host)
			}
			if hp := fmt.Sprintf("%s:%d", e.Host, e.Port); !slices.Contains(hosts, hp) {
				hosts = append(hosts, hp)
			}
		}
		if !slices.Equal(hosts, p.CredentialHosts) {
			t.Errorf("%s: credential hosts %v, initial rules on %v", p.ID, p.CredentialHosts, hosts)
		}
	}
}

// The model hub and cloud profiles: the variables their tools read, the
// hosts that get the key, and what is left to a policy proposal.
func TestServiceProfiles(t *testing.T) {
	python := []string{"/usr/bin/curl", "/usr/bin/wget", "/usr/bin/python3*",
		"/home/dev/.local/share/mise/installs/python/*/bin/python3*", "/home/dev/.local/share/uv/python/*/bin/python3*"}
	node := "/home/dev/.local/share/mise/installs/node/*/bin/node"
	git := []string{"/usr/bin/git", "/usr/lib/git-core/git-remote-http", "/usr/lib/git-core/git-remote-https"}
	for _, c := range []struct {
		id, auth, header string
		env              []string
		hosts, binaries  []string
	}{
		{"iwato-openrouter", "bearer", "authorization", []string{"OPENROUTER_API_KEY"}, []string{"openrouter.ai/api/**"}, append(slices.Clone(python), node)},
		{"iwato-huggingface", "bearer", "authorization", []string{"HF_TOKEN", "HUGGING_FACE_HUB_TOKEN"}, []string{"huggingface.co/**", "hf.co/**"}, append(append(slices.Clone(python), node), git...)},
		{"iwato-civitai", "bearer", "authorization", []string{"CIVITAI_API_TOKEN", "CIVITAI_TOKEN"}, []string{"civitai.com/api/**", "orchestration.civitai.com/**"}, python},
		{"iwato-vastai", "bearer", "authorization", []string{"VAST_API_KEY"}, []string{"console.vast.ai/api/**"}, python},
		{"iwato-runpod", "bearer", "authorization", []string{"RUNPOD_API_KEY"}, []string{"api.runpod.io/graphql", "api.runpod.io/v2/**", "rest.runpod.io/v1/**", "api.runpod.ai/v2/**", "hapi.runpod.net/v1/**"},
			append([]string{"/home/dev/.local/share/mise/installs/github-runpod-runpodctl/*/runpodctl", "/usr/local/bin/runpodctl"}, append(slices.Clone(python), node)...)},
		{"iwato-cloudflare", "bearer", "authorization", []string{"CLOUDFLARE_API_TOKEN"}, []string{"api.cloudflare.com/client/v4/graphql", "api.cloudflare.com/client/v4/**"},
			[]string{node, "/home/dev/.local/share/mise/installs/cloudflared/*/cloudflared", "/usr/bin/curl"}},
		{"iwato-comfy-cloud", "header", "x-api-key", []string{"COMFY_CLOUD_API_KEY", "COMFY_API_KEY"}, []string{"cloud.comfy.org/api/**", "cloud.comfy.org/api/prompt", "cloud.comfy.org/ws", "api.comfy.org/**"}, append(slices.Clone(python), node)},
	} {
		p := mustProfile(t, c.id)
		m := p.proto()
		if len(m.Credentials) != 1 || m.Credentials[0].AuthStyle != c.auth || m.Credentials[0].HeaderName != c.header || !slices.Equal(m.Credentials[0].EnvVars, c.env) || !m.Credentials[0].Required {
			t.Errorf("%s: %v", c.id, m.Credentials)
		}
		if m.Discovery == nil || !slices.Equal(m.Discovery.Credentials, []string{m.Credentials[0].Name}) {
			t.Errorf("%s: discovery %v", c.id, m.Discovery)
		}
		var hosts, bins []string
		for _, e := range initialEndpoints(t, c.id) {
			hosts = append(hosts, e.Host+e.Path)
			if e.Port != 443 {
				t.Errorf("%s: %s port %d", c.id, e.Host, e.Port)
			}
		}
		for _, b := range m.Binaries {
			bins = append(bins, b.Path)
		}
		if !slices.Equal(hosts, c.hosts) || !slices.Equal(bins, c.binaries) {
			t.Errorf("%s: %v %v", c.id, hosts, bins)
		}
		if p.Hint == "" {
			t.Errorf("%s: %+v", c.id, p)
		}
	}

	// Download hosts get no key: they are not in the profiles.
	for _, id := range []string{"iwato-huggingface", "iwato-civitai", "iwato-comfy-cloud"} {
		for _, e := range initialEndpoints(t, id) {
			if strings.Contains(e.Host, "*") || strings.HasPrefix(e.Host, "cdn") || strings.Contains(e.Host, "xethub") || strings.Contains(e.Host, "storage") {
				t.Errorf("%s: download host %s is credentialed", id, e.Host)
			}
		}
	}

	// Hugging Face: reads, and only the POSTs that read.
	for _, r := range initialEndpoints(t, "iwato-huggingface")[0].Rules {
		if a := r.Allow; a.Method != "GET" && a.Method != "HEAD" && a.Method != "OPTIONS" &&
			!slices.Contains([]string{"/api/**/paths-info/**", "/**/info/lfs/objects/batch", "/**/git-upload-pack"}, a.Path) {
			t.Error("huggingface.co allows", a)
		}
	}
	// Civitai: read-only (generation spends Buzz).
	for _, e := range initialEndpoints(t, "iwato-civitai") {
		if e.Access != v1.NetworkAccessPresetReadOnly {
			t.Error("civitai", e.Host, e.Access)
		}
	}
	// Vast.ai: reads and the offer searches; renting takes a proposal.
	for _, r := range initialEndpoints(t, "iwato-vastai")[0].Rules {
		if a := r.Allow; a.Method != "GET" && a.Method != "HEAD" && a.Method != "OPTIONS" &&
			!(a.Method == "PUT" && a.Path == "/api/v0/search/asks/") && !(a.Method == "POST" && a.Path == "/api/v0/bundles/") {
			t.Error("console.vast.ai allows", a)
		}
	}
	// RunPod: reads only (GraphQL queries, REST GET); pods and serverless
	// runs take a proposal.
	for _, e := range initialEndpoints(t, "iwato-runpod") {
		if e.Access != v1.NetworkAccessPresetReadOnly || len(e.Rules) > 0 || (e.Protocol == "graphql") != (e.Path == "/graphql") {
			t.Error("runpod", e.Host+e.Path, e.Protocol, e.Access)
		}
	}
	// Cloudflare: reads and GraphQL queries; deploys take a proposal.
	for _, e := range initialEndpoints(t, "iwato-cloudflare") {
		if e.Access != v1.NetworkAccessPresetReadOnly || len(e.Rules) > 0 || (e.Protocol == "graphql") != strings.HasSuffix(e.Path, "/graphql") {
			t.Error("cloudflare", e.Host+e.Path, e.Protocol, e.Access)
		}
	}
	// Comfy Cloud: the body is rewritten for /api/prompt only (uploads
	// over 256 KiB would fail), the WebSocket is the upgrade only.
	for _, e := range initialEndpoints(t, "iwato-comfy-cloud") {
		if e.RequestBodyCredentialRewrite != (e.Path == "/api/prompt") {
			t.Error("comfy body rewrite on", e.Host+e.Path)
		}
		if (e.Protocol == "websocket") != (e.Path == "/ws") {
			t.Error("comfy protocol", e.Host+e.Path, e.Protocol)
		}
	}
	// Modal: two headers; every endpoint inspected REST without SigV4 or
	// body rewriting, which the patched supervisor needs to offer h2.
	m := mustProfile(t, "iwato-modal").proto()
	if len(m.Credentials) != 2 || m.Credentials[0].HeaderName != "x-modal-token-id" || !slices.Equal(m.Credentials[0].EnvVars, []string{"MODAL_TOKEN_ID"}) ||
		m.Credentials[1].HeaderName != "x-modal-token-secret" || !slices.Equal(m.Credentials[1].EnvVars, []string{"MODAL_TOKEN_SECRET"}) {
		t.Error("modal credentials", m.Credentials)
	}
	var modalHosts []string
	for _, e := range initialEndpoints(t, "iwato-modal") {
		modalHosts = append(modalHosts, e.Host)
		if e.Protocol != "rest" || e.Port != 443 || e.CredentialSigning != "" || e.RequestBodyCredentialRewrite || len(e.Rules) != 1 || e.Rules[0].Allow.Method != "POST" || e.Rules[0].Allow.Path != "/modal.client.ModalClient/*" {
			t.Error("modal", e.Host, e.Protocol, e.Rules)
		}
	}
	if !slices.Equal(modalHosts, []string{"api.modal.com", "api.modal2.com"}) {
		t.Error("modal hosts", modalHosts)
	}
	// OpenRouter: inference is POST.
	if e := initialEndpoints(t, "iwato-openrouter"); e[0].Access != v1.NetworkAccessPresetReadWrite || !mustProfile(t, "iwato-openrouter").proto().InferenceCapable {
		t.Error("openrouter", e[0].Access)
	}
}
