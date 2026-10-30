package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"sync"

	"context"
	v1 "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1"
	pb "github.com/NVIDIA/OpenShell/sdk/go/proto/openshellv1"
	sb "github.com/NVIDIA/OpenShell/sdk/go/proto/sandboxv1"
)

// githubProfileID is Iwato's GitHub profile.
const githubProfileID = "iwato-github"

const maxProfileYAML = 64 << 10

// legacyBase is the built-in profile a provider's own profile was made
// from (BASE-for-NAME) before ADR 0022 ("" for none). Such a profile
// carries endpoints, so OpenShell binds the credential itself and Iwato
// adds no rules for it; the page asks to add the provider again. Deleting
// the provider deletes its profile.
func legacyBase(p ProviderInfo) string {
	bases := []string{"iwato-claude"}
	for _, x := range providerProfiles {
		bases = append(bases, x.ID)
	}
	for _, b := range bases {
		if p.Profile == legacyProfileID(b, p.Name) {
			return b
		}
	}
	return ""
}

// legacyProfileID is the per-provider profile name of base had before ADR
// 0022: base-for-NAME (OpenShell's ids are lowercase kebab-case without
// empty parts, so a name with "--" or a trailing "-" got a hash of itself).
func legacyProfileID(base, name string) string {
	var parts []string
	for _, p := range strings.Split(name, "-") {
		if p != "" {
			parts = append(parts, p)
		}
	}
	slug := strings.Join(parts, "-")
	id := base + "-for-" + slug
	if slug != name {
		h := sha256.Sum256([]byte(name))
		id += "-" + hex.EncodeToString(h[:4])
	}
	return id
}

// inspectedProtocols are the protocols OpenShell inspects requests of.
var inspectedProtocols = []string{"rest", "graphql", "websocket", "mcp", "json-rpc", "jsonrpc"}

// checkProfile is Iwato's check of a profile before it imports it, besides
// OpenShell's own: a valid id, the credentials only on inspected traffic
// (credential_conflicts.go), and no endpoints of its rule that OpenShell
// would refuse as ambiguous (endpoint_ambiguity.go).
func checkProfile(m *pb.ProviderProfile) error {
	if !validProfileID(m.GetId()) {
		return errCode("profile_id_invalid", map[string]any{"id": fmt.Sprintf("%q", truncate(m.GetId(), 80))}, fmt.Sprintf("id は英小文字・数字をハイフンでつないだものにしてください: %q", truncate(m.GetId(), 80)))
	}
	for i, e := range m.GetEndpoints() {
		if len(m.GetCredentials()) == 0 {
			break
		}
		if !slices.Contains(inspectedProtocols, strings.ToLower(e.GetProtocol())) || e.GetTls() == sb.NetworkTlsMode_NETWORK_TLS_MODE_SKIP || e.GetAllowUninspectedCredentials() {
			return errCode("profile_uninspected", map[string]any{"index": i, "host": e.GetHost()}, fmt.Sprintf("endpoints[%d] (%s): 認証情報が検査されない通信に乗ります。protocol を rest などにし、tls: skip と allow_uninspected_credentials は使わないでください", i, e.GetHost()))
		}
	}
	rules := map[string]v1.NetworkPolicyRule{"_provider_check": profileRule(m, "_provider_check")}
	if a := findEndpointAmbiguities(rules); len(a) > 0 {
		params := map[string]any{"left": a[0].LeftIndex, "leftWhere": a[0].LeftSelector, "right": a[0].RightIndex, "rightWhere": a[0].RightSelector, "detail": strings.Join(a[0].Conflicts, "; ")}
		return errCode("profile_ambiguous", params, fmt.Sprintf("宛先どうしの設定が食い違います (OpenShell が拒否します): endpoints[%d] (%s) と endpoints[%d] (%s): %s",
			a[0].LeftIndex, a[0].LeftSelector, a[0].RightIndex, a[0].RightSelector, strings.Join(a[0].Conflicts, "; ")))
	}
	return nil
}

// validProfileID is OpenShell's (openshell-providers is_valid_profile_id):
// lowercase letters and digits in parts joined by single hyphens.
func validProfileID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, part := range strings.Split(id, "-") {
		if part == "" {
			return false
		}
		for i := 0; i < len(part); i++ {
			if c := part[i]; !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9') {
				return false
			}
		}
	}
	return true
}

// The owner's custom profiles (providers_custom.go), besides the built-in
// ones. profileByID finds them too.
var extraProfiles = struct {
	sync.RWMutex
	m map[string]providerProfile
}{m: map[string]providerProfile{}}

func registerProfile(p providerProfile) {
	extraProfiles.Lock()
	defer extraProfiles.Unlock()
	extraProfiles.m[p.ID] = p
}

func unregisterProfile(id string) {
	extraProfiles.Lock()
	defer extraProfiles.Unlock()
	delete(extraProfiles.m, id)
}

// allProfiles are the built-in profiles, then the others by id: what
// Iwato imports.
func allProfiles() []providerProfile {
	out := slices.Clone(providerProfiles)
	extraProfiles.RLock()
	defer extraProfiles.RUnlock()
	for _, id := range slices.Sorted(maps.Keys(extraProfiles.m)) {
		out = append(out, extraProfiles.m[id])
	}
	return out
}

// customProfiles are the owner's custom profiles, by id.
func customProfiles() []providerProfile {
	extraProfiles.RLock()
	defer extraProfiles.RUnlock()
	var out []providerProfile
	for _, p := range extraProfiles.m {
		if p.Custom {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// profileStore writes profiles into the gateway: the custom ones.
// OpenShell and Fake implement it.
type profileStore interface {
	// PutProviderProfile imports the profile or replaces the gateway's
	// copy (refused with OpenShell's diagnostics).
	PutProviderProfile(ctx context.Context, m *pb.ProviderProfile) error
	// DeleteProviderProfile deletes it (a missing one is fine).
	DeleteProviderProfile(ctx context.Context, id string) error
}

// providerBase is the built-in profile a provider is of: its profile, or
// the base of its per-provider profile made before ADR 0022 (whose OAuth
// refresh and reconnect still work).
func providerBase(p ProviderInfo) string {
	if b := legacyBase(p); b != "" {
		return b
	}
	return p.Profile
}
