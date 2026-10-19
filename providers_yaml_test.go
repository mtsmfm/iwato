package main

import (
	"strings"
	"testing"
	"time"

	pb "github.com/NVIDIA/OpenShell/sdk/go/proto/openshellv1"
	sb "github.com/NVIDIA/OpenShell/sdk/go/proto/sandboxv1"
	"google.golang.org/protobuf/encoding/prototext"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
)

// The Go constants match the YAML profiles: the token endpoints the flows
// check against and the variables the page reads.
func TestProfilesYAMLMatchConstants(t *testing.T) {
	// The page's metadata stays out of the YAML; the variables come from it.
	gh, _ := profileByID("iwato-github")
	if gh.Credentials[0].Label != "トークン" || strings.Join(gh.Credentials[0].Env, ",") != "GITHUB_TOKEN,GH_TOKEN" || strings.Contains(string(gh.yaml), "トークン") {
		t.Fatalf("%+v", gh.Credentials)
	}
	at, _ := profileByID(atlassianProfileID)
	if at.Credentials[0].Env[0] != atlassianCredentialKey || at.proto().Credentials[0].Refresh.TokenUrl != atlassianTokenURL {
		t.Fatal("the Atlassian constants and the YAML differ")
	}
	gk, _ := profileByID(grokSubscriptionProfileID)
	if gk.Credentials[0].Env[0] != grokOAuthCredentialKey || gk.proto().Credentials[0].Refresh.TokenUrl != xaiTokenURL {
		t.Fatal("the Grok constants and the YAML differ")
	}
	e2e := atlassianProfile("iwato-atlassian-e2e", "http://127.0.0.1:1/token")
	if e2e.Id != "iwato-atlassian-e2e" || e2e.Credentials[0].Refresh.TokenUrl != "http://127.0.0.1:1/token" || at.proto().Credentials[0].Refresh.TokenUrl != atlassianTokenURL {
		t.Fatal(e2e)
	}
}

// OpenShell's own example file (providers/github.yaml, v0.1.4) parses, with
// its flow-style binaries and discovery.
func TestParseOpenShellExample(t *testing.T) {
	p, err := parseProfileYAML([]byte(`
id: github
display_name: GitHub
description: GitHub API and Git operations
category: source_control
credentials:
  - name: api_token
    description: GitHub token
    env_vars: [GITHUB_TOKEN, GH_TOKEN]
    required: true
    auth_style: bearer
    header_name: authorization
discovery:
  credentials: [api_token]
endpoints:
  - host: api.github.com
    port: 443
    protocol: rest
    access: read-only
    enforcement: enforce
  - host: github.com
    port: 443
    protocol: rest
    enforcement: enforce
    rules:
      - allow: { method: GET, path: "**" }
      - allow: { method: POST, path: "/**/git-upload-pack" }
binaries: [/usr/bin/gh, /usr/local/bin/gh, {path: /usr/bin/git}]
`))
	if err != nil {
		t.Fatal(err)
	}
	if p.Category != pb.ProviderProfileCategory_PROVIDER_PROFILE_CATEGORY_SOURCE_CONTROL || p.Discovery.GetCredentials()[0] != "api_token" || len(p.Binaries) != 3 || p.Binaries[2].Path != "/usr/bin/git" ||
		p.Endpoints[0].Access != sb.NetworkAccessPreset_NETWORK_ACCESS_PRESET_READ_ONLY || p.Endpoints[1].Rules[1].Allow.Path != "/**/git-upload-pack" {
		t.Fatal(prototext.Format(p))
	}
	// As OpenShell: no category is other, legacy seconds are durations,
	// the MCP tool shorthand is params.name, enum case does not matter.
	p, err = parseProfileYAML([]byte(`
id: x
display_name: X
credentials:
  - name: k
    refresh: {strategy: OAuth2-Refresh-Token, refresh_before_seconds: 60, max_lifetime: "0.500s"}
    token_grant: {token_endpoint: https://t.example/token}
endpoints:
  - host: mcp.example
    protocol: mcp
    rules:
      - allow: {method: tools/call, tool: "search_*"}
`))
	if err != nil {
		t.Fatal(err)
	}
	r := p.Credentials[0].Refresh
	if p.Category != pb.ProviderProfileCategory_PROVIDER_PROFILE_CATEGORY_OTHER || r.Strategy != pb.ProviderCredentialRefreshStrategy_PROVIDER_CREDENTIAL_REFRESH_STRATEGY_OAUTH2_REFRESH_TOKEN ||
		r.RefreshBefore.AsDuration() != time.Minute || r.MaxLifetime.AsDuration() != 500*time.Millisecond ||
		p.Credentials[0].TokenGrant.GrantType != pb.ProviderCredentialTokenGrantType_PROVIDER_CREDENTIAL_TOKEN_GRANT_TYPE_CLIENT_CREDENTIALS ||
		p.Endpoints[0].Rules[0].Allow.Params["name"].GetGlob() != "search_*" {
		t.Fatal(prototext.Format(p))
	}
}

// Typos fail loudly, at any depth; so do values OpenShell refuses.
func TestParseProfileRejects(t *testing.T) {
	base := "id: x\ndisplay_name: X\n"
	for name, c := range map[string]struct{ doc, want string }{
		"top-level typo":       {base + "binaries: [/a]\nbinary: [/b]\n", "field binary not found"},
		"credential typo":      {base + "credentials:\n  - name: k\n    env_var: [K]\n", "field env_var not found"},
		"refresh typo":         {base + "credentials:\n  - name: k\n    refresh: {strategy: static, token_uri: x}\n", "field token_uri not found"},
		"material typo":        {base + "credentials:\n  - name: k\n    refresh: {material: [{name: a, secrett: true}]}\n", "field secrett not found"},
		"endpoint typo":        {base + "endpoints:\n  - host: h\n    acess: full\n", "field acess not found"},
		"rule typo":            {base + "endpoints:\n  - host: h\n    rules:\n      - allow: {method: GET, pth: /}\n", "field pth not found"},
		"rule wrapper typo":    {base + "endpoints:\n  - host: h\n    rules:\n      - allows: {method: GET}\n", "field allows not found"},
		"binary object typo":   {base + "binaries: [{path: /a, harness: true}]\n", "field harness not found"},
		"matcher typo":         {base + "endpoints:\n  - host: h\n    rules:\n      - allow: {method: GET, query: {q: {globb: x}}}\n", "field globb not found"},
		"discovery typo":       {base + "discovery: {credential: [k]}\n", "field credential not found"},
		"unknown access":       {base + "endpoints:\n  - host: h\n    access: readonly\n", "unknown access"},
		"unknown enforcement":  {base + "endpoints:\n  - host: h\n    enforcement: enforced\n", "unknown enforcement"},
		"unknown tls":          {base + "endpoints:\n  - host: h\n    tls: on\n", "unknown tls"},
		"unknown category":     {base + "category: sourcecontrol\n", "category"},
		"unknown strategy":     {base + "credentials:\n  - name: k\n    refresh: {strategy: oauth}\n", "refresh strategy"},
		"bad duration":         {base + "credentials:\n  - name: k\n    refresh: {refresh_before: 5m}\n", "not a duration"},
		"duration twice":       {base + "credentials:\n  - name: k\n    refresh: {refresh_before: 5s, refresh_before_seconds: 5}\n", "both set"},
		"empty rules":          {base + "endpoints:\n  - host: h\n    rules: []\n", "cannot be empty"},
		"tool and params.name": {base + "endpoints:\n  - host: h\n    rules:\n      - allow: {tool: a, params: {name: b}}\n", "not both"},
		"no id":                {"display_name: X\n", "missing field id"},
		"no display name":      {"id: x\n", "missing field display_name"},
		"no host":              {base + "endpoints:\n  - port: 443\n", "missing field host"},
		"duplicate key":        {base + "id: y\n", "already defined"},
		"two documents":        {base + "---\n" + base, "one YAML document"},
		"wrong type":           {base + "endpoints:\n  - host: h\n    port: https\n", "cannot unmarshal"},
	} {
		if _, err := parseProfileYAML([]byte(c.doc)); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// A profile the gateway holds (imported with the CLI) renders to YAML in
// OpenShell's format that parses back to the same profile, without the
// gateway's bookkeeping.
func TestProfileYAMLRendersGatewayProfiles(t *testing.T) {
	yes := true
	gw := &pb.ProviderProfile{
		Id: "corp-api", DisplayName: "Corp API", Description: "Internal: \"quoted\" # not a comment", Category: pb.ProviderProfileCategory_PROVIDER_PROFILE_CATEGORY_DATA,
		ResourceVersion: 7, Annotations: map[string]string{"k": "v"}, Source: "/tmp/corp.yaml", Scope: "platform", InferenceCapable: true,
		Credentials: []*pb.ProviderProfileCredential{
			{Name: "key", EnvVars: []string{"CORP_KEY"}, Required: true, AuthStyle: "header", HeaderName: "x-api-key", QueryParam: "key", PathTemplate: "/k/{key}",
				Refresh: &pb.ProviderCredentialRefresh{Strategy: pb.ProviderCredentialRefreshStrategy_PROVIDER_CREDENTIAL_REFRESH_STRATEGY_AWS_STS_ASSUME_ROLE, Scopes: []string{"a", "b"},
					RefreshBefore: &durationpb.Duration{Nanos: 500000000}, MaxLifetime: &durationpb.Duration{Seconds: 0},
					Material:          []*pb.ProviderCredentialRefreshMaterial{{Name: "role_arn", Required: true}},
					AdditionalOutputs: []*pb.ProviderCredentialRefreshOutput{{Output: "session_token", Credential: "session"}}}},
			{Name: "session", EnvVars: []string{"AWS_SESSION_TOKEN"},
				TokenGrant: &pb.ProviderCredentialTokenGrant{GrantType: pb.ProviderCredentialTokenGrantType_PROVIDER_CREDENTIAL_TOKEN_GRANT_TYPE_TOKEN_EXCHANGE, TokenEndpoint: "https://sts.example/token",
					Audience: "aud", Scopes: []string{"s"}, CacheTtl: &durationpb.Duration{Seconds: 90, Nanos: 1000},
					AudienceOverrides: []*pb.ProviderCredentialTokenGrantAudienceOverride{{Host: "h", Port: 8443, Path: "/p", Audience: "a2"}},
					SubjectToken:      &pb.ProviderCredentialTokenGrantSubjectToken{Source: "provider_credential", Credential: "key"}}},
		},
		Endpoints: []*sb.NetworkEndpoint{
			{Host: "api.corp.example", Ports: []uint32{443, 8443}, Path: "/v1/**", Protocol: "rest", Tls: sb.NetworkTlsMode_NETWORK_TLS_MODE_SKIP, Enforcement: sb.NetworkEnforcementMode_NETWORK_ENFORCEMENT_MODE_AUDIT,
				Rules:      []*sb.L7Rule{{Allow: &sb.L7Allow{Method: "GET", Path: "/v1/**", Query: map[string]*sb.L7QueryMatcher{"a": {Glob: "x*"}, "b": {Any: []string{"1", "2"}}}}}},
				DenyRules:  []*sb.L7DenyRule{{Method: "DELETE", Path: "/**"}},
				AllowedIps: []string{"10.0.0.0/8"}, AllowEncodedSlash: true, WebsocketCredentialRewrite: true, RequestBodyCredentialRewrite: true, AllowUninspectedCredentials: true,
				CredentialSigning: "sigv4", SigningService: "s3", SigningRegion: "us-west-2"},
			{Host: "gql.corp.example", Port: 443, Protocol: "graphql", Access: sb.NetworkAccessPreset_NETWORK_ACCESS_PRESET_READ_WRITE, PersistedQueries: "allowlist",
				GraphqlPersistedQueries: map[string]*sb.GraphqlOperation{"abc": {OperationType: "query", OperationName: "Viewer", Fields: []string{"viewer"}}}, GraphqlMaxBodyBytes: 1024},
			{Host: "mcp.corp.example", Port: 443, Protocol: "mcp", JsonRpcMaxBodyBytes: 2048,
				Mcp:   &sb.McpOptions{Versions: []string{"2025-11-25"}, StrictToolNames: &yes},
				Rules: []*sb.L7Rule{{Allow: &sb.L7Allow{Method: "tools/call", Params: map[string]*sb.L7QueryMatcher{"name": {Glob: "search"}}}}}},
		},
		Binaries:  []*sb.NetworkBinary{{Path: "/usr/bin/curl"}},
		Discovery: &pb.ProviderProfileDiscovery{Credentials: []string{"key"}},
	}
	y, err := profileYAML(gw)
	if err != nil {
		t.Fatal(err)
	}
	back, err := parseProfileYAML([]byte(y))
	if err != nil {
		t.Fatalf("%v\n%s", err, y)
	}
	want := proto.Clone(gw).(*pb.ProviderProfile)
	want.ResourceVersion, want.Annotations, want.Source, want.Scope = 0, nil, "", ""
	if !proto.Equal(back, want) {
		t.Fatalf("%s\n got %v\nwant %v", y, prototext.Format(back), prototext.Format(want))
	}
	for _, line := range []string{"id: corp-api\n", "category: data\n", "    refresh_before: 0.500s\n", "    max_lifetime: 0s\n", "    strategy: aws_sts_assume_role\n",
		"    grant_type: token_exchange\n", "    cache_ttl: 90.000001s\n", "    tls: skip\n", "    enforcement: audit\n", "    access: read-write\n", "    ports: [443, 8443]\n",
		"binaries:\n  - /usr/bin/curl\n", "discovery:\n  credentials: [key]\n"} {
		if !strings.Contains(y, line) {
			t.Errorf("no %q in\n%s", line, y)
		}
	}
	if strings.Contains(y, "resource_version") || strings.Contains(y, "annotations") || strings.Contains(y, "\nsource:") || strings.Contains(y, "\nscope:") {
		t.Errorf("the gateway's bookkeeping is rendered:\n%s", y)
	}

	// Iwato's own profiles render as their files say, comments aside.
	gh, _ := profileByID("iwato-github")
	y, err = profileYAML(gh.proto())
	if err != nil {
		t.Fatal(err)
	}
	if back, err := parseProfileYAML([]byte(y)); err != nil || !proto.Equal(back, gh.proto()) {
		t.Fatal(err, y)
	}
	for _, line := range []string{"category: source_control\n", "    env_vars: [GITHUB_TOKEN, GH_TOKEN]\n", "    access: read-only\n", "      - allow: {method: GET, path: '**'}\n"} {
		if !strings.Contains(y, line) {
			t.Errorf("no %q in\n%s", line, y)
		}
	}
	// Values the format has no name for are shown, not dropped.
	odd := &pb.ProviderProfile{Id: "o", DisplayName: "O", Endpoints: []*sb.NetworkEndpoint{{Host: "h", Access: 9}}}
	if y, _ := profileYAML(odd); !strings.Contains(y, "access: unknown(9)") {
		t.Fatal(y)
	}
}

func TestProfileDurations(t *testing.T) {
	for in, want := range map[string]time.Duration{"300s": 300 * time.Second, "0s": 0, "0.5s": 500 * time.Millisecond, "1.000000001s": time.Second + 1, "-2.5s": -2500 * time.Millisecond} {
		d, err := profileDuration("f", in, 0)
		if err != nil || d.AsDuration() != want {
			t.Errorf("%s: %v %v", in, d, err)
		}
	}
	for _, in := range []string{"5m", "s", "1.0000000001s", "1e3s", ".5s", "+1s", "1"} {
		if _, err := profileDuration("f", in, 0); err == nil {
			t.Errorf("%s accepted", in)
		}
	}
	if d, _ := profileDuration("f", "", 0); d != nil {
		t.Error("absent is not absent")
	}
	for d, want := range map[*durationpb.Duration]string{{Seconds: 300}: "300s", {Nanos: 500000000}: "0.500s", {Seconds: 1, Nanos: 1}: "1.000000001s", {Seconds: -1, Nanos: -500000000}: "-1.500s"} {
		if got := durationText(d); got != want {
			t.Errorf("%v: %s", d, got)
		}
	}
}
