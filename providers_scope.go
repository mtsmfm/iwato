package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	v1 "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1"
	pb "github.com/NVIDIA/OpenShell/sdk/go/proto/openshellv1"
	sb "github.com/NVIDIA/OpenShell/sdk/go/proto/sandboxv1"
	"google.golang.org/protobuf/proto"
)

// A provider's access scope (docs/adr/0006): what its credential may do on
// the hosts it reaches. OpenShell's profiles are per provider type and a
// provider's type cannot change after it is created, so every provider of a
// scoped profile (each of Iwato's built-in ones) gets a profile of its own,
// scopedProfileID, which Iwato renders from the scope kept in its
// configuration (Config.ProviderScopes) and imports like its built-in
// profiles. Changing the scope updates that profile; OpenShell applies it to
// the attached sandboxes on their next provider sync (the binding follows
// the profile's endpoints, its rules join the effective policy as
// _provider_NAME), which the page follows as 反映待ち.
//
// GitHub's reads cover every path of its hosts, so the credential stays
// bound to the whole hosts; the scope is in the rules. Writes Iwato cannot
// narrow below the credential (GraphQL mutations: OpenShell matches the
// operation, not its arguments) are named as such on the page, and the docs
// recommend a fine-grained token limited to the repositories.
//
// The other profiles have two modes (docs/adr/0014): read-only, the
// built-in profile as it is, and full, which allows every request on the
// profile's own endpoints (REST every method, GraphQL mutations, MCP every
// tool). Neither adds a host. Atlassian's has tools (read tools plus named
// write tools, providers_scope_atlassian.go) instead of read-only.

// githubProfileID is the built-in GitHub profile, whose scope has
// repositories and GraphQL mutations besides read-only and full.
const githubProfileID = "iwato-github"

// scopedBases are the built-in profiles whose providers get a scope.
var scopedBases = func() []string {
	var out []string
	for _, p := range providerProfiles {
		if p.Scoped {
			out = append(out, p.ID)
		}
	}
	return out
}()

// retiredBases are scoped bases Iwato no longer ships (iwato-claude:
// Claude Code's setup token). Their providers keep the profile the gateway
// holds and show as other providers; Iwato keeps their scope in the
// configuration and deletes both with the provider.
var retiredBases = []string{"iwato-claude"}

// retiredScope: the provider's own profile is of a retired base.
func retiredScope(p ProviderInfo) bool {
	return slices.ContainsFunc(retiredBases, func(b string) bool { return p.Profile == scopedProfileID(b, p.Name) })
}

// The scope's modes.
const (
	scopeReadOnly = "read-only"
	scopeRepos    = "repos"
	scopeFull     = "full"
)

// scopeModes are the modes a provider of the base can have.
func scopeModes(base string) []string {
	switch base {
	case githubProfileID:
		return []string{scopeReadOnly, scopeRepos, scopeFull}
	case atlassianProfileID:
		return []string{scopeTools, scopeFull}
	}
	return []string{scopeReadOnly, scopeFull}
}

// defaultScopeMode is the mode a new provider of the base gets.
func defaultScopeMode(base string) string {
	return scopeModes(base)[0]
}

// validScopeMode: one of the base's modes, or Atlassian's read-only, which
// the page does not offer but a configuration may hold (the shipped
// profile, every tool).
func validScopeMode(base, mode string) bool {
	return slices.Contains(scopeModes(base), mode) || base == atlassianProfileID && mode == scopeReadOnly
}

// ProviderScope is a provider's access scope, as configured.
type ProviderScope struct {
	// Base is the built-in profile (iwato-github, iwato-openrouter, ...).
	Base string `json:"base"`
	// Mode is read-only, repos (GitHub: write to Repos), tools
	// (Atlassian: read tools and Tools) or full.
	Mode string `json:"mode"`
	// Repos are OWNER/REPO, written to in mode repos (GitHub only).
	Repos []string `json:"repos,omitempty"`
	// Mutations are GraphQL mutations (top-level fields) allowed besides
	// queries, for every repository the credential reaches (GitHub only).
	Mutations []string `json:"mutations,omitempty"`
	// Tools are MCP tools allowed besides the read tools in mode tools
	// (Atlassian only).
	Tools []string `json:"tools,omitempty"`
	// YAML is the owner's own profile for the provider (edited in the
	// page's YAML editor): it replaces the rendering of the fields above.
	YAML string `json:"yaml,omitempty"`
}

const (
	maxScopeRepos     = 50
	maxScopeMutations = 50
	maxProfileYAML    = 64 << 10
)

var (
	// A GitHub owner: letters, digits and single hyphens inside (39 at
	// most); a repository: letters, digits, '.', '_' and '-'.
	repoOwnerPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,37}[A-Za-z0-9])?$`)
	repoNamePattern  = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)
	// A GraphQL name.
	graphqlNamePattern = regexp.MustCompile(`^[_A-Za-z][_0-9A-Za-z]{0,99}$`)
)

// normalizeRepo checks OWNER/REPO (a trailing .git is dropped). It goes
// into rule paths verbatim, so nothing a glob or a path could read as
// special gets through.
func normalizeRepo(s string) (string, error) {
	s = strings.TrimSpace(s)
	owner, repo, ok := strings.Cut(s, "/")
	repo = strings.TrimSuffix(repo, ".git")
	if !ok || !repoOwnerPattern.MatchString(owner) || !repoNamePattern.MatchString(repo) || repo == "." || repo == ".." || strings.Contains(repo, "..") {
		return "", errCode("scope_repo_invalid", map[string]any{"repo": fmt.Sprintf("%q", truncate(s, 120))}, fmt.Sprintf("リポジトリは OWNER/REPO の形で書いてください (英数字・. _ -。* や .. は使えません): %q", truncate(s, 120)))
	}
	return owner + "/" + repo, nil
}

// normalize checks the scope and puts it in a canonical form.
func (sc ProviderScope) normalize() (ProviderScope, error) {
	if !slices.Contains(scopedBases, sc.Base) {
		return sc, errCode("scope_base_invalid", map[string]any{"profile": sc.Base}, fmt.Sprintf("範囲を設定できないプロファイルです: %s", sc.Base))
	}
	if len(sc.YAML) > maxProfileYAML {
		return sc, errCode("yaml_too_large", nil, "YAML が大きすぎます")
	}
	out := ProviderScope{Base: sc.Base, Mode: sc.Mode, YAML: sc.YAML}
	modes := scopeModes(sc.Base)
	switch {
	case sc.Mode == "":
		out.Mode = defaultScopeMode(sc.Base)
	case !validScopeMode(sc.Base, sc.Mode):
		return sc, errCode("scope_mode_invalid", map[string]any{"mode": fmt.Sprintf("%q", truncate(sc.Mode, 40)), "modes": modes}, fmt.Sprintf("範囲は %s のどれかです: %q", strings.Join(modes, "、"), truncate(sc.Mode, 40)))
	}
	if sc.Base == atlassianProfileID && out.Mode == scopeTools {
		tools, err := normalizeTools(sc.Tools)
		if err != nil {
			return sc, err
		}
		out.Tools = tools
	}
	if sc.Base != githubProfileID {
		// Repositories and mutations are GitHub's, tools Atlassian's.
		return out, nil
	}
	if len(sc.Repos) > maxScopeRepos {
		return sc, errCode("scope_repos_too_many", map[string]any{"max": maxScopeRepos}, fmt.Sprintf("リポジトリは %d 個までです", maxScopeRepos))
	}
	for _, r := range sc.Repos {
		if strings.TrimSpace(r) == "" {
			continue
		}
		n, err := normalizeRepo(r)
		if err != nil {
			return sc, err
		}
		if !slices.Contains(out.Repos, n) {
			out.Repos = append(out.Repos, n)
		}
	}
	if len(sc.Mutations) > maxScopeMutations {
		return sc, errCode("scope_mutations_too_many", map[string]any{"max": maxScopeMutations}, fmt.Sprintf("ミューテーションは %d 個までです", maxScopeMutations))
	}
	for _, m := range sc.Mutations {
		m = strings.TrimSpace(m)
		if m == "" {
			continue
		}
		if !graphqlNamePattern.MatchString(m) {
			return sc, errCode("scope_mutation_invalid", map[string]any{"name": fmt.Sprintf("%q", truncate(m, 120))}, fmt.Sprintf("GraphQL のミューテーションの名前ではありません: %q", truncate(m, 120)))
		}
		if !slices.Contains(out.Mutations, m) {
			out.Mutations = append(out.Mutations, m)
		}
	}
	if out.Mode == scopeRepos && len(out.Repos) == 0 {
		return sc, errCode("scope_repos_missing", nil, "書き込むリポジトリを 1 つ以上書いてください")
	}
	if out.Mode != scopeRepos {
		out.Repos = nil
	}
	if out.Mode == scopeFull {
		out.Mutations = nil
	}
	return out, nil
}

// scopedProfileID is the profile of the provider name of a scoped base:
// base-for-NAME (OpenShell's ids are lowercase kebab-case without empty
// parts, so a name with "--" or a trailing "-" gets a hash of itself).
func scopedProfileID(base, name string) string {
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

// scopedBaseOf is the scoped base of the provider's profile ("" when it
// is not one of Iwato's per-provider profiles).
func scopedBaseOf(p ProviderInfo) string {
	for _, b := range scopedBases {
		if p.Profile == scopedProfileID(b, p.Name) {
			return b
		}
	}
	return ""
}

// providerBase is the built-in profile a provider is of: the base of its
// own profile, or its profile.
func providerBase(p ProviderInfo) string {
	if b := scopedBaseOf(p); b != "" {
		return b
	}
	return p.Profile
}

// readRules allow what the read-only preset allows.
func readRules() []*sb.L7Rule {
	return []*sb.L7Rule{allowRule("GET", "**"), allowRule("HEAD", "**"), allowRule("OPTIONS", "**")}
}

func allowRule(method, path string) *sb.L7Rule {
	return &sb.L7Rule{Allow: &sb.L7Allow{Method: method, Path: path}}
}

// renderScope is the profile of provider name with the scope (a normalized
// one).
func renderScope(name string, sc ProviderScope) (*pb.ProviderProfile, error) {
	base, ok := builtinProfile(sc.Base)
	if !ok {
		return nil, fmt.Errorf("unknown profile %s", sc.Base)
	}
	id := scopedProfileID(sc.Base, name)
	if sc.YAML != "" {
		return customScopeProfile(name, sc.Base, []byte(sc.YAML))
	}
	m := base.proto()
	m.Id = id
	m.DisplayName = base.Label + " (" + name + ")"
	switch sc.Base {
	case githubProfileID:
		scopeGitHub(m, sc)
	case atlassianProfileID:
		scopeAtlassian(m, sc)
	default:
		scopeWrite(m, sc)
	}
	if err := checkProfile(m); err != nil {
		return nil, err
	}
	return m, nil
}

// scopeGitHub widens iwato-github (read-only: REST GET, git clone and
// fetch, GraphQL queries) by the scope.
func scopeGitHub(m *pb.ProviderProfile, sc ProviderScope) {
	var what []string
	switch sc.Mode {
	case scopeFull:
		what = append(what, "full write")
	case scopeRepos:
		what = append(what, "write to "+strings.Join(sc.Repos, ", "))
	default:
		what = append(what, "read-only")
	}
	if len(sc.Mutations) > 0 {
		what = append(what, "GraphQL mutations "+strings.Join(sc.Mutations, ", "))
	}
	m.Description = "GitHub API and Git over HTTPS for Iwato's sandbox image: " + strings.Join(what, "; ")
	for _, e := range m.Endpoints {
		if sc.Mode == scopeFull {
			e.Access, e.Rules = sb.NetworkAccessPreset_NETWORK_ACCESS_PRESET_FULL, nil
			continue
		}
		switch {
		case e.Host == "api.github.com" && e.Path == "/graphql":
			if len(sc.Mutations) > 0 {
				e.Access = sb.NetworkAccessPreset_NETWORK_ACCESS_PRESET_UNSPECIFIED
				e.Rules = []*sb.L7Rule{
					{Allow: &sb.L7Allow{OperationType: "query"}},
					{Allow: &sb.L7Allow{OperationType: "mutation", Fields: slices.Clone(sc.Mutations)}},
				}
			}
		case e.Host == "api.github.com", e.Host == "uploads.github.com":
			// REST writes (and release asset uploads) under each repository.
			if len(sc.Repos) > 0 {
				e.Access = sb.NetworkAccessPreset_NETWORK_ACCESS_PRESET_UNSPECIFIED
				e.Rules = readRules()
				for _, r := range sc.Repos {
					e.Rules = append(e.Rules, allowRule("*", "/repos/"+r+"/**"))
				}
			}
		case e.Host == "github.com":
			// git push: the GET of info/refs?service=git-receive-pack is a
			// read already; the pack goes to git-receive-pack.
			for _, r := range sc.Repos {
				e.Rules = append(e.Rules, allowRule("POST", "/"+r+".git/git-receive-pack"), allowRule("POST", "/"+r+"/git-receive-pack"))
			}
		}
	}
}

// scopeWrite widens a profile other than GitHub's in mode full: every
// request on its own endpoints (access full: REST every method, GraphQL
// every operation, MCP every tool). Read-only is the profile as it is.
func scopeWrite(m *pb.ProviderProfile, sc ProviderScope) {
	if sc.Mode != scopeFull {
		return
	}
	m.Description += " (write allowed)"
	for _, e := range m.Endpoints {
		e.Access, e.Rules = sb.NetworkAccessPreset_NETWORK_ACCESS_PRESET_FULL, nil
	}
}

// customScopeProfile reads the owner's YAML for provider name: the
// provider's own id, and the base's credentials (the provider holds
// values under their variables).
func customScopeProfile(name, base string, b []byte) (*pb.ProviderProfile, error) {
	if len(b) > maxProfileYAML {
		return nil, errCode("yaml_too_large", nil, "YAML が大きすぎます")
	}
	m, err := parseProfileYAML(b)
	if err != nil {
		return nil, errCode("profile_yaml_invalid", map[string]any{"detail": err.Error()}, "YAML を読めません: "+err.Error())
	}
	id := scopedProfileID(base, name)
	if m.Id != id {
		return nil, errCode("scope_yaml_id", map[string]any{"id": id}, fmt.Sprintf("id は %s のままにしてください (このプロバイダーのプロファイルです)", id))
	}
	bp, _ := builtinProfile(base)
	bm := bp.proto()
	if len(m.Credentials) != len(bm.Credentials) || !slices.EqualFunc(m.Credentials, bm.Credentials, func(a, b *pb.ProviderProfileCredential) bool { return proto.Equal(a, b) }) {
		return nil, errCode("scope_yaml_credentials", map[string]any{"profile": base}, fmt.Sprintf("credentials は %s のままにしてください (プロバイダーが預けている値の名前です)", base))
	}
	if err := withinBase(m, bm); err != nil {
		return nil, err
	}
	if err := checkProfile(m); err != nil {
		return nil, err
	}
	return m, nil
}

// withinBase keeps the owner's YAML of a scoped provider on its base's
// hosts and programs: the credential goes only where the base profile
// sends it (each endpoint's host and port are one of the base's, no
// globs), and only the base's binaries use it. Paths, rules and access
// within those stay editable.
func withinBase(m, base *pb.ProviderProfile) error {
	type hostPort struct {
		host string
		port uint32
	}
	allowed := map[hostPort]bool{}
	for _, e := range base.Endpoints {
		for _, port := range profileEndpointPorts(e) {
			allowed[hostPort{strings.ToLower(e.Host), port}] = true
		}
	}
	for i, e := range m.Endpoints {
		ports := profileEndpointPorts(e)
		if len(ports) == 0 {
			return errCode("scope_yaml_port", map[string]any{"index": i, "host": e.Host}, fmt.Sprintf("endpoints[%d] (%s): port を書いてください", i, e.Host))
		}
		for _, port := range ports {
			if !allowed[hostPort{strings.ToLower(e.Host), port}] {
				return errCode("scope_yaml_host", map[string]any{"index": i, "host": e.Host, "port": port, "profile": base.Id}, fmt.Sprintf("endpoints[%d] (%s:%d): このプロバイダーの認証情報は %s の宛先 (ホストとポート) にしか送れません。ほかの宛先はカスタムのプロファイルで", i, e.Host, port, base.Id))
			}
		}
	}
	for i, b := range m.Binaries {
		if !slices.ContainsFunc(base.Binaries, func(x *sb.NetworkBinary) bool { return x.Path == b.Path }) {
			return errCode("scope_yaml_binary", map[string]any{"index": i, "binary": b.Path, "profile": base.Id}, fmt.Sprintf("binaries[%d] (%s): 使えるプログラムは %s のものだけです", i, b.Path, base.Id))
		}
	}
	return nil
}

func profileEndpointPorts(e *sb.NetworkEndpoint) []uint32 {
	if len(e.Ports) > 0 {
		return e.Ports
	}
	if e.Port != 0 {
		return []uint32{e.Port}
	}
	return nil
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

// The profiles besides the built-in ones: each scoped provider's, and the
// owner's custom profiles (providers_custom.go). profileByID finds them
// too, so a provider of one gets its base's labels and credentials.
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

// scopedProfile is the registry entry of a scoped provider's profile: its
// base's labels with the rendered profile.
func scopedProfile(m *pb.ProviderProfile, base string) (providerProfile, error) {
	bp, ok := builtinProfile(base)
	if !ok {
		return providerProfile{}, fmt.Errorf("unknown profile %s", base)
	}
	y, err := profileYAML(m)
	if err != nil {
		return providerProfile{}, err
	}
	p := bp
	p.ID, p.Base, p.yaml = m.Id, base, []byte(y)
	return p, nil
}

// registerScopes registers the profiles of the configured scopes (when
// Iwato starts). A scope that no longer renders (the owner's YAML) is
// left out: the gateway keeps the profile it has.
func registerScopes(scopes map[string]ProviderScope) {
	for _, name := range slices.Sorted(maps.Keys(scopes)) {
		sc := scopes[name]
		if slices.Contains(retiredBases, sc.Base) {
			continue
		}
		m, err := renderScope(name, sc)
		if err == nil {
			var p providerProfile
			if p, err = scopedProfile(m, sc.Base); err == nil {
				if sc.YAML != "" {
					p.yaml = []byte(sc.YAML)
				}
				registerProfile(p)
				continue
			}
		}
		log.Printf("provider %s: the scope does not render: %v", name, english(err))
	}
}

// ScopeView is a provider's scope for the page.
type ScopeView struct {
	Mode string `json:"mode"`
	// Modes are the modes the provider's type has (scopeModes).
	Modes     []string `json:"modes"`
	Repos     []string `json:"repos"`
	Mutations []string `json:"mutations"`
	Tools     []string `json:"tools"`
	// ReadTools are the tools mode tools always allows (Atlassian).
	ReadTools []string `json:"readTools,omitempty"`
	// Custom: the owner edited the profile as YAML.
	Custom bool `json:"custom,omitempty"`
	// Missing: Iwato's configuration has no scope for the provider (the
	// gateway's profile is shown as it is; saving one replaces it).
	Missing bool `json:"missing,omitempty"`
}

func scopeView(base string, sc ProviderScope, ok bool) *ScopeView {
	var read []string
	if base == atlassianProfileID {
		read = atlassianReadTools
	}
	if !ok {
		return &ScopeView{Mode: defaultScopeMode(base), Modes: scopeModes(base), Repos: []string{}, Mutations: []string{}, Tools: []string{}, ReadTools: read, Missing: true}
	}
	v := &ScopeView{Mode: sc.Mode, Modes: scopeModes(base), Repos: append([]string{}, sc.Repos...), Mutations: append([]string{}, sc.Mutations...), Tools: append([]string{}, sc.Tools...), ReadTools: read, Custom: sc.YAML != ""}
	if v.Mode == "" {
		v.Mode = defaultScopeMode(base)
	}
	if base == atlassianProfileID && v.Mode == scopeReadOnly {
		// The shipped profile, which allows every tool.
		v.Mode = scopeFull
	}
	return v
}

func (s *Server) providerScope(name string) (ProviderScope, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sc, ok := s.config.ProviderScopes[name]
	return sc, ok
}

// validScopes checks the configuration's scopes.
func validScopes(scopes map[string]ProviderScope) error {
	if len(scopes) > 200 {
		return errors.New("too many provider scopes")
	}
	for name, sc := range scopes {
		if !providerNamePattern.MatchString(name) {
			return fmt.Errorf("provider scope: invalid provider name %q", name)
		}
		if slices.Contains(retiredBases, sc.Base) {
			continue
		}
		n, err := sc.normalize()
		if err != nil {
			return fmt.Errorf("scope of provider %s: %w", name, err)
		}
		if !slices.Equal(n.Repos, sc.Repos) || !slices.Equal(n.Mutations, sc.Mutations) || !slices.Equal(n.Tools, sc.Tools) || n.Mode != sc.Mode {
			return fmt.Errorf("scope of provider %s: not in canonical form", name)
		}
	}
	return nil
}

// profileStore writes profiles into the gateway: the scoped providers' and
// the custom ones. OpenShell and Fake implement it.
type profileStore interface {
	// PutProviderProfile imports the profile or replaces the gateway's
	// copy (refused with OpenShell's diagnostics).
	PutProviderProfile(ctx context.Context, m *pb.ProviderProfile) error
	// DeleteProviderProfile deletes it (a missing one is fine).
	DeleteProviderProfile(ctx context.Context, id string) error
}

// providerObserver follows a running sandbox's current state of a
// provider: the receipt for ProviderReadiness. OpenShell and Fake.
type providerObserver interface {
	ObserveProvider(ctx context.Context, sandbox, provider string) (string, error)
}

// createScopedProvider creates a provider of a scoped profile with its
// own profile, in the base's default mode (defaultScopeMode), under name
// or, when name is empty, the first free name of the profile (as
// createProviderNamed).
func (s *Server) createScopedProvider(ctx context.Context, rt providerRuntime, store profileStore, name string, prof providerProfile, creds map[string]string) (string, error) {
	var taken []string
	list, _, err := rt.ListProviders(ctx)
	if err != nil {
		return "", err
	}
	for _, p := range list {
		taken = append(taken, p.Name)
	}
	candidates := []string{name}
	if name == "" {
		candidates = nil
		for i := 1; i <= providerNameRetries; i++ {
			candidates = append(candidates, providerNameCandidate(providerNameBase(prof.ID), i))
		}
	}
	for _, n := range candidates {
		if slices.Contains(taken, n) {
			if name != "" {
				return "", fmt.Errorf("provider %s already exists", n)
			}
			continue
		}
		sc := ProviderScope{Base: prof.ID, Mode: defaultScopeMode(prof.ID)}
		m, err := renderScope(n, sc)
		if err != nil {
			return "", err
		}
		// A profile left by a deleted provider of the name is replaced.
		if err := store.PutProviderProfile(ctx, m); err != nil {
			return "", errCode("profile_import_failed", map[string]any{"detail": safeCLIError(err)}, "プロファイルを取り込めません: "+safeCLIError(err))
		}
		err = rt.CreateProvider(ctx, n, m.Id, creds)
		if isAlreadyExists(err) && name == "" {
			continue
		}
		if err != nil {
			return "", err
		}
		p, err := scopedProfile(m, prof.ID)
		if err == nil {
			registerProfile(p)
		}
		if err := s.updateConfig(func(c *Config) error {
			if c.ProviderScopes == nil {
				c.ProviderScopes = map[string]ProviderScope{}
			}
			c.ProviderScopes[n] = sc
			return nil
		}); err != nil {
			log.Printf("provider %s: save the scope: %v", n, english(err))
		}
		return n, nil
	}
	return "", errCode("provider_names_taken", map[string]any{"first": candidates[0], "last": candidates[len(candidates)-1]}, fmt.Sprintf("%s から %s まで使われています。名前を入力してください", candidates[0], candidates[len(candidates)-1]))
}

// forgetScope removes a deleted provider's own profile and scope.
func (s *Server) forgetScope(ctx context.Context, p ProviderInfo) {
	if scopedBaseOf(p) == "" && !retiredScope(p) {
		return
	}
	if store, ok := s.rt.(profileStore); ok {
		if err := store.DeleteProviderProfile(ctx, p.Profile); err != nil {
			log.Printf("provider %s: delete the profile %s: %v", p.Name, p.Profile, safeText(english(err)))
		}
	}
	unregisterProfile(p.Profile)
	if _, ok := s.providerScope(p.Name); ok {
		if err := s.updateConfig(func(c *Config) error {
			delete(c.ProviderScopes, p.Name)
			if len(c.ProviderScopes) == 0 {
				c.ProviderScopes = nil
			}
			return nil
		}); err != nil {
			log.Printf("provider %s: forget the scope: %v", p.Name, english(err))
		}
	}
}

// handleProviderScope changes a provider's scope (POST
// /api/providers/NAME/scope): {mode, repos, mutations, tools} from the simple
// editor, {yaml} from the YAML editor (the provider's profile becomes
// custom), or {reset: true} back to the simple scope. Iwato checks the new
// profile against the sandboxes it is attached to, the gateway imports it
// and the running ones apply it on their next sync (反映待ち).
func (s *Server) handleProviderScope(w http.ResponseWriter, r *http.Request, name string) {
	var body struct {
		Mode      string   `json:"mode"`
		Repos     []string `json:"repos"`
		Mutations []string `json:"mutations"`
		Tools     []string `json:"tools"`
		YAML      string   `json:"yaml"`
		Reset     bool     `json:"reset"`
	}
	if decodeLimit(w, r, &body, 2*maxProfileYAML) != nil {
		fail(w, 400, "invalid request")
		return
	}
	rt, hasRT := s.rt.(providerRuntime)
	store, hasStore := s.rt.(profileStore)
	if !hasRT || !hasStore {
		fail(w, 501, "providers unavailable")
		return
	}
	ctx, cancel := timeout(s.ctx, 60*time.Second)
	defer cancel()
	sc, pending, code, err := s.changeScope(ctx, rt, store, name, func(old ProviderScope, base string) (ProviderScope, error) {
		sc := ProviderScope{Base: base, Mode: body.Mode, Repos: body.Repos, Mutations: body.Mutations, Tools: body.Tools}
		switch {
		case body.Reset:
			sc = old
			sc.YAML = ""
		case strings.TrimSpace(body.YAML) != "":
			sc = old
			sc.YAML = body.YAML
		}
		return sc, nil
	})
	if err != nil {
		failScope(w, code, err)
		return
	}
	jsonResponse(w, 200, map[string]any{"ok": true, "scope": scopeView(sc.Base, sc, true), "pending": pending})
}

// failScope answers changeScope's error: a refusal with what the page
// words it from (failCheck), else with its code.
func failScope(w http.ResponseWriter, status int, err error) {
	if a := (*errAmbiguity)(nil); errors.As(err, &a) {
		failCheck(w, err)
		return
	}
	failErr(w, status, err)
}

// changeScope sets provider name's scope to what edit makes of the current
// one (the base's default mode when the configuration has none): Iwato
// checks the new profile against the sandboxes it is attached to, the
// gateway imports it and the running ones apply it on their next sync
// (反映待ち, whose sandboxes it returns). An error comes with its HTTP
// status.
func (s *Server) changeScope(ctx context.Context, rt providerRuntime, store profileStore, name string, edit func(old ProviderScope, base string) (ProviderScope, error)) (ProviderScope, []string, int, error) {
	s.providerMu.Lock()
	defer s.providerMu.Unlock()
	list, attached, err := s.providerState(ctx, rt)
	if err != nil {
		return ProviderScope{}, nil, 409, errors.New(safeCLIError(err))
	}
	i := slices.IndexFunc(list, func(p ProviderInfo) bool { return p.Name == name })
	if i < 0 {
		return ProviderScope{}, nil, 404, errors.New("unknown provider")
	}
	p := list[i]
	// The sandboxes it is attached to: their policies are checked against
	// the new profile, and are not to change meanwhile.
	var users []string
	for _, sb := range s.sandboxList() {
		if slices.Contains(attached[sb.Name], name) {
			users = append(users, sb.Name)
		}
	}
	defer s.lockPolicy(users...)()
	base := scopedBaseOf(p)
	if base == "" {
		if bp, ok := builtinProfile(p.Profile); ok && bp.Scoped {
			return ProviderScope{}, nil, 409, errCode("scope_legacy_provider", map[string]any{"name": name, "profile": bp.Label}, name+" は範囲を設定できるようになる前に作ったので、読み取りだけのままです (OpenShell はプロバイダーのプロファイルを後から変えられません)。範囲を決めるには "+bp.Label+" のプロバイダーを追加し直し、付け替えてください")
		}
		return ProviderScope{}, nil, 409, errCode("scope_unsupported", nil, "範囲を設定できないプロバイダーです")
	}
	old, had := s.providerScope(name)
	if !had {
		old = ProviderScope{Base: base, Mode: defaultScopeMode(base)}
	}
	sc, err := edit(old, base)
	if err != nil {
		return ProviderScope{}, nil, 409, err
	}
	if sc, err = sc.normalize(); err != nil {
		return ProviderScope{}, nil, 400, err
	}
	m, err := renderScope(name, sc)
	if err != nil {
		return ProviderScope{}, nil, 400, err
	}
	if refusal, err := s.scopeProblems(ctx, p, m, attached); err != nil {
		return ProviderScope{}, nil, 409, errCode("policy_unverifiable", map[string]any{"detail": safeCLIError(err)}, "ポリシーを確かめられません: "+safeCLIError(err))
	} else if refusal != nil {
		return ProviderScope{}, nil, 409, refusal
	}
	if err := store.PutProviderProfile(ctx, m); err != nil {
		return ProviderScope{}, nil, 409, errCode("openshell_refused", map[string]any{"detail": safeCLIError(err)}, "OpenShell が受け付けませんでした: "+safeCLIError(err))
	}
	entry, err := scopedProfile(m, base)
	if err != nil {
		return ProviderScope{}, nil, 500, err
	}
	if sc.YAML != "" {
		entry.yaml = []byte(sc.YAML)
	}
	registerProfile(entry)
	if err := s.updateConfig(func(c *Config) error {
		if c.ProviderScopes == nil {
			c.ProviderScopes = map[string]ProviderScope{}
		}
		c.ProviderScopes[name] = sc
		return nil
	}); err != nil {
		return ProviderScope{}, nil, 500, errCode("provider_config_unsaved", map[string]any{"detail": err.Error()}, "OpenShell では変更しましたが、設定に保存できませんでした: "+err.Error())
	}
	return sc, s.followScope(ctx, name, attached), 200, nil
}

// scopeProblems is why the new profile m of provider p would not fit the
// sandboxes it is attached to: their policy carries its credential
// uninspected (credential_conflicts.go), or its rule would be ambiguous
// with theirs. OpenShell checks the same when it updates the profile;
// Iwato names the rules (a refusal the page words: web/refusals.js).
func (s *Server) scopeProblems(ctx context.Context, p ProviderInfo, m *pb.ProviderProfile, attached map[string][]string) (*errAmbiguity, error) {
	label := p.Name
	if bp, ok := profileByID(p.Profile); ok {
		label = bp.Label
	}
	for _, sb := range s.sandboxList() {
		if !slices.Contains(attached[sb.Name], p.Name) {
			continue
		}
		if rr, ok := s.rt.(policyRulesRuntime); ok {
			rules, err := rr.PolicyRules(ctx, sb)
			if err != nil {
				return nil, err
			}
			if cs := credentialConflicts(rules, profileScopes(p.Name, label, m)); len(cs) > 0 {
				return errRefused("provider_scope_conflict", map[string]any{"sandbox": sb.Name}, "サンドボックス "+sb.Name+": "+conflictMessage(cs, true), nil, cs), nil
			}
		}
		_, effective, ok, err := s.policyLayers(ctx, sb)
		if err != nil {
			return nil, err
		}
		if !ok || effective == nil {
			continue
		}
		providers := s.ruleProviders(ctx, sb)
		before := maps.Clone(effective)
		for key, rp := range providers {
			if rp.Name == p.Name {
				delete(before, key)
				delete(providers, key)
			}
		}
		after := maps.Clone(before)
		key := composeProviderRule(after, p.Name, m)
		providers[key] = providerLabel(p)
		if list := newAmbiguities(before, after); len(list) > 0 {
			views := ambiguityViews(list, []string{key}, after, providers)
			return errRefused("ambiguous_scope", map[string]any{"sandbox": sb.Name}, ambiguityMessage("サンドボックス "+sb.Name+" のポリシーと食い違うので変えられません", views, ""), views, nil), nil
		}
	}
	return nil, nil
}

// followScope records the scope change for the running sandboxes the
// provider is attached to (反映待ち) and returns their names.
func (s *Server) followScope(ctx context.Context, name string, attached map[string][]string) []string {
	obs, ok := s.rt.(providerObserver)
	if !ok {
		return []string{}
	}
	receipts := map[string]string{}
	for _, sb := range s.sandboxList() {
		if !slices.Contains(attached[sb.Name], name) || s.rt.Status(ctx, sb).State != "running" {
			continue
		}
		receipt, err := obs.ObserveProvider(ctx, sb.Name, name)
		if err != nil || receipt == "" {
			log.Printf("sandbox %s: provider %s: follow the scope change: %v", sb.Name, name, english(err))
			continue
		}
		receipts[sb.Name] = receipt
	}
	if len(receipts) == 0 {
		return []string{}
	}
	if err := s.updateSandboxes(func(list []Sandbox) ([]Sandbox, error) {
		for i := range list {
			receipt, ok := receipts[list[i].Name]
			if !ok {
				continue
			}
			pending := slices.DeleteFunc(slices.Clone(list[i].ProviderApplying), func(a ProviderApply) bool { return a.Provider == name && a.Scope })
			list[i].ProviderApplying = append(pending, ProviderApply{Provider: name, Attach: true, Scope: true, Receipt: receipt, At: time.Now().UTC()})
		}
		return list, nil
	}); err != nil {
		log.Printf("provider %s: %v", name, english(err))
	}
	return slices.Sorted(maps.Keys(receipts))
}
