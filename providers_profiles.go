package main

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"

	pb "github.com/NVIDIA/OpenShell/sdk/go/proto/openshellv1"
	"google.golang.org/protobuf/proto"
)

// The provider profiles Iwato ships. OpenShell's gateway has no built-in
// profiles (its providers/*.yaml are examples to adapt), so Prepare imports
// these, and updates them when they change here (profileHashKey).
//
// Each profile is a YAML file in OpenShell's format, profiles/ID.yaml (the
// source of truth: `openshell provider profile lint -f` checks it), lowered
// to the proto as OpenShell's CLI does (providers_yaml.go). What only the
// page needs (the label, the note, the credentials' labels, ...) is here.
//
// A profile's binaries decide which processes may use the credential's
// endpoints, so they name the paths in Iwato's sandbox image
// (sandboximage/): the apt packages under /usr, the agent CLIs mise
// installs under /home/dev/.local/share/mise/installs (claude also under
// /home/dev/.local/share/claude/versions, grok under /home/dev/.grok).
// OpenShell matches a connection's executable or one of its ancestors,
// and `*` matches within one path component.

//go:embed profiles/*.yaml
var profileFiles embed.FS

// profileHashKey is the annotation that records which version of Iwato's
// profile the gateway holds.
const profileHashKey = "iwato/profile-hash"

// providerProfile is one of Iwato's profiles as the page shows it.
type providerProfile struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	// Group is the id of the heading the page's type selector puts it
	// under (profileGroups); the profiles of a group are next to each other.
	Group       string              `json:"group"`
	Note        string              `json:"note"`
	Credentials []profileCredential `json:"credentials"`
	// OAuth: Iwato gets the credential by an OAuth flow (atlassian.go,
	// xai.go, claude_oauth.go) and the gateway refreshes it; the owner enters no value.
	OAuth bool `json:"oauth,omitempty"`
	// OAuthFlow names the flow of an OAuth profile: "atlassian" (the
	// authorization code with a loopback redirect), "xai" (device code) or
	// "claude" (Claude Code's client: a loopback redirect or a pasted code).
	OAuthFlow string `json:"oauthFlow,omitempty"`
	// Guide is a command to run in the sandbox to use the provider.
	Guide string `json:"guide,omitempty"`
	// Hint is how a program in the sandbox picks the value up.
	Hint string `json:"hint,omitempty"`
	// DefaultName is the name a provider gets when the owner gives none
	// (providers_names.go): the page shows it as the placeholder.
	DefaultName string `json:"defaultName"`
	// Base is the built-in profile a scoped provider's profile is
	// rendered from (providers_scope.go).
	Base string `json:"base,omitempty"`
	// Scoped: providers of the profile get an access scope (every
	// built-in profile; TestProfileMetadata).
	Scoped bool `json:"scoped,omitempty"`
	// Custom: the owner's profile (providers_custom.go).
	Custom bool `json:"custom,omitempty"`
	// yaml is profiles/ID.yaml (or the rendered or the owner's YAML).
	yaml []byte
}

// proto is the profile as Iwato imports it (without its hash). The
// embedded files are checked when Iwato starts (init below).
func (p providerProfile) proto() *pb.ProviderProfile {
	m, err := parseProfileYAML(p.yaml)
	if err != nil {
		panic(fmt.Sprintf("profiles/%s.yaml: %v", p.ID, err))
	}
	return m
}

// profileCredential is a value the owner enters, by the name of the
// profile's credential. It is stored under each of Env, the environment
// variables the sandbox gets (as placeholders): the YAML's env_vars.
type profileCredential struct {
	Name  string   `json:"name"`
	Label string   `json:"label"`
	Env   []string `json:"env"`
}

// Atlassian's remote MCP (Rovo MCP server). Iwato registers itself (DCR),
// lets the owner authorize it and gives the gateway the refresh token;
// the gateway refreshes the access token (oauth2_refresh_token) and the
// sandbox gets a placeholder. Discovery (atlassian.go) refuses an
// authorization server whose token endpoint is not atlassianTokenURL
// (the profile's token_url: TestProfilesYAMLMatchConstants).
const (
	atlassianProfileID     = "iwato-atlassian"
	atlassianCredentialKey = "ATLASSIAN_ACCESS_TOKEN"
	atlassianResource      = "https://mcp.atlassian.com/v2/mcp"
	atlassianTokenURL      = "https://auth.atlassian.com/oauth/token"
	// The command registers the MCP in the sandbox's Claude Code. Single
	// quotes: Claude Code expands ${...} in headers on every connection,
	// a shell would fix the placeholder in its configuration. tools=all
	// lists every tool by its own name instead of the execute meta-tools,
	// which take the tool's name as an argument (OpenShell matches only
	// the name of the tool called: providers_scope_atlassian.go).
	atlassianMCPCommand = "claude mcp add --transport http atlassian '" + atlassianResource + "?tools=all' --header 'Authorization: Bearer ${" + atlassianCredentialKey + "}'"
)

// atlassianProfile is iwato-atlassian under another ID with another token
// URL (e2e tests use a loopback one).
func atlassianProfile(id, tokenURL string) *pb.ProviderProfile {
	return profileWithTokenURL(atlassianProfileID, id, tokenURL)
}

// profileWithTokenURL is an OAuth profile under another ID with another
// token URL.
func profileWithTokenURL(base, id, tokenURL string) *pb.ProviderProfile {
	p, _ := profileByID(base)
	m := p.proto()
	m.Id = id
	m.Credentials[0].Refresh.TokenUrl = tokenURL
	return m
}

// Grok Build with the owner's xAI account (SuperGrok, X Premium). Iwato
// signs in at auth.x.ai with Grok Build's own public client (device code,
// xai.go) and gives the gateway the refresh token; the sandbox gets a
// placeholder in XAI_OAUTH_TOKEN, and grok takes it from
// GROK_AUTH_PROVIDER_COMMAND, which Iwato puts in the terminals'
// environment (grokAuthCommand, term_env.go). The
// constants are Grok Build's (crates/codegen/xai-grok-login: config.rs,
// device_code.rs); xaiTokenURL is the profile's token_url
// (TestProfilesYAMLMatchConstants).
const (
	grokSubscriptionProfileID = "iwato-grok-subscription"
	grokOAuthCredentialKey    = "XAI_OAUTH_TOKEN"
	xaiIssuer                 = "https://auth.x.ai"
	xaiDeviceCodeURL          = xaiIssuer + "/oauth2/device/code"
	xaiTokenURL               = xaiIssuer + "/oauth2/token"
	xaiClientID               = "b1a00492-073a-47ea-816f-4c329264a828"
	xaiScopes                 = "openid profile email offline_access grok-cli:access api:access conversations:read conversations:write workspaces:read workspaces:write"
	// grokAuthCommand is GROK_AUTH_PROVIDER_COMMAND: grok runs it with
	// sh -c and takes the JSON as an xAI session (the issuer), rerunning it
	// near the expiry and after a 401. The placeholder never expires (the
	// gateway refreshes behind it); expires_in only bounds how long grok
	// keeps an old one after a reconnect.
	grokAuthCommand = `[ -n "$` + grokOAuthCredentialKey + `" ] || { echo "` + grokOAuthCredentialKey + ` is not set: attach the Grok Build (xAI subscription) provider in Iwato and restart the sandbox" >&2; exit 1; }; ` +
		`printf "{\"access_token\":\"%s\",\"expires_in\":900,\"issuer\":\"` + xaiIssuer + `\"}\n" "$` + grokOAuthCredentialKey + `"`
)

// oauthCredentialKey is the variable of an OAuth profile's credential
// (the one the gateway refreshes).
func oauthCredentialKey(profile string) string {
	if p, ok := profileByID(profile); ok && p.OAuthFlow == "xai" {
		return grokOAuthCredentialKey
	} else if ok && p.OAuthFlow == "claude" {
		return claudeOAuthCredentialKey
	}
	return atlassianCredentialKey
}

// The headings of the page's type selector, in its order (ids: the page
// shows web/i18n.js providers.group.<id>).
const (
	groupAgent  = "agent"
	groupSource = "source"
	groupModels = "models"
	groupCloud  = "cloud"
)

var profileGroups = []string{groupAgent, groupSource, groupModels, groupCloud}

// providerProfiles are Iwato's profiles, in the page's order (by
// profileGroups: TestProfileMetadata).
var providerProfiles = []providerProfile{
	{
		ID: "iwato-claude", Label: "Claude Code (セットアップトークン)", Group: groupAgent, Scoped: true,
		Note:        "CLAUDE_CODE_OAUTH_TOKEN。`claude setup-token` で作ったトークンを api.anthropic.com に送ります。",
		Credentials: []profileCredential{{Name: "oauth_token", Label: "トークン (claude setup-token)"}},
	},
	{
		ID: claudeSubscriptionProfileID, Label: "Claude Code (Claude でログイン)", Group: groupAgent, Scoped: true,
		Note:        "CLAUDE_CODE_OAUTH_TOKEN。Claude のアカウントでログインし、サンドボックスの claude が api.anthropic.com に送ります。",
		Credentials: []profileCredential{{Name: "access_token", Label: "アクセストークン"}},
		OAuth:       true,
		OAuthFlow:   "claude",
		Hint:        "claude は CLAUDE_CODE_OAUTH_TOKEN があればそれを使います (setup-token と同じく推論だけの扱い)。サンドボックスで /login していたら、その記録より環境変数が優先されます。確かめるには claude -p 'hello'。",
	},
	{
		ID: grokSubscriptionProfileID, Label: "Grok Build (xAI のサブスクリプション)", Group: groupAgent, Scoped: true,
		Note:        "XAI_OAUTH_TOKEN。xAI のアカウントでログインします。grok が cli-chat-proxy.grok.com/v1 と api.x.ai/v1 に送ります。",
		Credentials: []profileCredential{{Name: "access_token", Label: "アクセストークン"}},
		OAuth:       true,
		OAuthFlow:   "xai",
		Hint:        "Iwato がターミナルに GROK_AUTH_PROVIDER_COMMAND を設定し、grok はそれでこのトークンを使います (XAI_API_KEY より優先)。前に grok login していたら grok logout してください。付け外しは再起動で反映されます。確かめるには grok -p 'hello'。",
	},
	{
		ID: "iwato-openrouter", Label: "OpenRouter", Group: groupAgent, Scoped: true,
		Note:        "OPENROUTER_API_KEY。openrouter.ai/api に送ります (推論の POST も可)。",
		Credentials: []profileCredential{{Name: "api_key", Label: "API キー"}},
		Hint:        "OpenAI 互換の SDK は base_url を https://openrouter.ai/api/v1 にし、キーに OPENROUTER_API_KEY を渡します。",
	},
	{
		ID: atlassianProfileID, Label: "Atlassian (MCP)", Group: groupAgent, Scoped: true,
		Note:        "ATLASSIAN_ACCESS_TOKEN。Atlassian で OAuth の許可を取ります。claude が mcp.atlassian.com/v2/mcp に送るときだけ使います。",
		Credentials: []profileCredential{{Name: "access_token", Label: "アクセストークン"}},
		OAuth:       true,
		OAuthFlow:   "atlassian",
		Guide:       atlassianMCPCommand,
	},
	{
		ID: githubProfileID, Label: "GitHub", Group: groupSource, Scoped: true,
		Note:        "GH_TOKEN と GITHUB_TOKEN。最初は読み取りだけです。書き込めるリポジトリは追加した後に「範囲」で決めます。",
		Credentials: []profileCredential{{Name: "token", Label: "トークン"}},
	},
	{
		ID: "iwato-huggingface", Label: "Hugging Face", Group: groupModels, Scoped: true,
		Note:        "HF_TOKEN と HUGGING_FACE_HUB_TOKEN。huggingface.co と hf.co に送ります。読み取りだけで、アップロードはポリシーの提案で許可します。",
		Credentials: []profileCredential{{Name: "token", Label: "アクセストークン (hf_...)"}},
		Hint:        "hf (huggingface-cli)、huggingface_hub、transformers は HF_TOKEN をそのまま読みます。確かめるには hf auth whoami。",
	},
	{
		ID: "iwato-civitai", Label: "Civitai", Group: groupModels, Scoped: true,
		Note:        "CIVITAI_API_TOKEN と CIVITAI_TOKEN。civitai.com/api と orchestration.civitai.com に送ります。読み取りとダウンロードだけで、生成 (Buzz を使う) はポリシーの提案で許可します。",
		Credentials: []profileCredential{{Name: "api_key", Label: "API キー"}},
		Hint:        "ダウンロードは curl -fLOJ \"https://civitai.com/api/download/models/ID?token=$CIVITAI_TOKEN\" のように送ります。",
	},
	{
		ID: "iwato-comfy-cloud", Label: "Comfy Cloud", Group: groupCloud, Scoped: true,
		Note:        "COMFY_CLOUD_API_KEY と COMFY_API_KEY。cloud.comfy.org/api と api.comfy.org に送ります。ワークフローの実行 (クレジットを使う) もできます。",
		Credentials: []profileCredential{{Name: "api_key", Label: "API キー (platform.comfy.org)"}},
		Hint:        "Comfy の SDK は COMFY_API_KEY を読みます。出力の保存先 (署名付き URL) には値を送らないので、提案が出たら読み取りで承認します。",
	},
	{
		ID: "iwato-vastai", Label: "Vast.ai", Group: groupCloud, Scoped: true,
		Note:        "VAST_API_KEY。console.vast.ai/api に送ります。読み取りとオファーの検索だけで、インスタンスの作成・削除 (費用がかかる) はポリシーの提案で許可します。",
		Credentials: []profileCredential{{Name: "api_key", Label: "API キー"}},
		Hint:        "vastai は VAST_API_KEY を読みます (vastai set api-key は不要)。確かめるには vastai show user。",
	},
	{
		ID: "iwato-runpod", Label: "RunPod", Group: groupCloud, Scoped: true,
		Note:        "RUNPOD_API_KEY。api.runpod.io、rest.runpod.io、api.runpod.ai (サーバーレス)、hapi.runpod.net に送ります。読み取りだけで、Pod の作成・起動とサーバーレスの実行 (費用がかかる) はポリシーの提案で許可します。",
		Credentials: []profileCredential{{Name: "api_key", Label: "API キー"}},
		Hint:        "runpodctl (mise use -g github:runpod/runpodctl) と runpod の Python SDK は RUNPOD_API_KEY を読みます (runpodctl doctor は不要)。確かめるには runpodctl user。",
	},
}

// The YAML of each profile, and the credentials' variables from it. A
// file that does not parse, or credentials that differ from the labels
// here, stop Iwato at once (and its tests).
func init() {
	for i := range providerProfiles {
		p := &providerProfiles[i]
		b, err := profileFiles.ReadFile("profiles/" + p.ID + ".yaml")
		if err != nil {
			panic(err)
		}
		p.yaml = b
		m := p.proto()
		if m.Id != p.ID {
			panic(fmt.Sprintf("profiles/%s.yaml has id %s", p.ID, m.Id))
		}
		if len(m.Credentials) != len(p.Credentials) {
			panic(fmt.Sprintf("profiles/%s.yaml: %d credentials, %d labels", p.ID, len(m.Credentials), len(p.Credentials)))
		}
		for j, c := range m.Credentials {
			if c.Name != p.Credentials[j].Name {
				panic(fmt.Sprintf("profiles/%s.yaml: credential %s, label for %s", p.ID, c.Name, p.Credentials[j].Name))
			}
			p.Credentials[j].Env = c.EnvVars
		}
	}
}

// profileByID finds a built-in profile, a scoped provider's
// (providers_scope.go) or a custom one (providers_custom.go).
func profileByID(id string) (providerProfile, bool) {
	if p, ok := builtinProfile(id); ok {
		return p, true
	}
	extraProfiles.RLock()
	defer extraProfiles.RUnlock()
	p, ok := extraProfiles.m[id]
	return p, ok
}

func builtinProfile(id string) (providerProfile, bool) {
	for _, p := range providerProfiles {
		if p.ID == id {
			return p, true
		}
	}
	return providerProfile{}, false
}

// profileHash identifies a profile's content: the proto lowered from the
// YAML, not the file's bytes, so that comments and layout do not rewrite
// the gateway's copy.
func profileHash(p *pb.ProviderProfile) string {
	b, err := proto.MarshalOptions{Deterministic: true}.Marshal(p)
	if err != nil {
		panic(err)
	}
	h := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(h[:])
}

// importedProfile is the profile as Iwato imports it: with its hash.
func importedProfile(p providerProfile) *pb.ProviderProfile {
	m := p.proto()
	m.Annotations = map[string]string{profileHashKey: profileHash(m)}
	return m
}
