package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	pb "github.com/NVIDIA/OpenShell/sdk/go/proto/openshellv1"
	sb "github.com/NVIDIA/OpenShell/sdk/go/proto/sandboxv1"
	"go.yaml.in/yaml/v3"
	"google.golang.org/protobuf/types/known/durationpb"
)

// Provider profiles as YAML, in OpenShell's format (the CLI's `openshell
// provider profile lint|import|export`: crates/openshell-providers/src/
// profiles.rs, ProviderTypeProfile, with the enum names of
// crates/openshell-policy/src/l7_validate.rs). parseProfileYAML lowers a
// file to the proto Iwato imports, as the CLI does; profileYAML renders a
// profile the gateway holds back to that format.
//
// Unlike OpenShell (serde ignores unknown keys at most levels), unknown
// fields are errors, so that a typo in Iwato's files fails loudly.

type profileDoc struct {
	ID               string            `yaml:"id"`
	ResourceVersion  uint64            `yaml:"resource_version,omitempty"`
	Annotations      map[string]string `yaml:"annotations,omitempty"`
	DisplayName      string            `yaml:"display_name"`
	Description      string            `yaml:"description,omitempty"`
	Category         string            `yaml:"category,omitempty"`
	Credentials      []credentialDoc   `yaml:"credentials,omitempty"`
	Endpoints        []endpointDoc     `yaml:"endpoints,omitempty"`
	Binaries         []binaryDoc       `yaml:"binaries,omitempty"`
	InferenceCapable bool              `yaml:"inference_capable,omitempty"`
	Discovery        *discoveryDoc     `yaml:"discovery,omitempty"`
	Source           string            `yaml:"source,omitempty"`
	Scope            string            `yaml:"scope,omitempty"`
}

type credentialDoc struct {
	Name         string         `yaml:"name"`
	Description  string         `yaml:"description,omitempty"`
	EnvVars      []string       `yaml:"env_vars,omitempty,flow"`
	Required     bool           `yaml:"required,omitempty"`
	AuthStyle    string         `yaml:"auth_style,omitempty"`
	HeaderName   string         `yaml:"header_name,omitempty"`
	QueryParam   string         `yaml:"query_param,omitempty"`
	Refresh      *refreshDoc    `yaml:"refresh,omitempty"`
	PathTemplate string         `yaml:"path_template,omitempty"`
	TokenGrant   *tokenGrantDoc `yaml:"token_grant,omitempty"`
}

type refreshDoc struct {
	Strategy             string        `yaml:"strategy,omitempty"`
	TokenURL             string        `yaml:"token_url,omitempty"`
	Scopes               []string      `yaml:"scopes,omitempty,flow"`
	RefreshBeforeSeconds int64         `yaml:"refresh_before_seconds,omitempty"`
	RefreshBefore        string        `yaml:"refresh_before,omitempty"`
	MaxLifetimeSeconds   int64         `yaml:"max_lifetime_seconds,omitempty"`
	MaxLifetime          string        `yaml:"max_lifetime,omitempty"`
	Material             []materialDoc `yaml:"material,omitempty"`
	AdditionalOutputs    []outputDoc   `yaml:"additional_outputs,omitempty"`
}

type materialDoc struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description,omitempty"`
	Required    bool   `yaml:"required,omitempty"`
	Secret      bool   `yaml:"secret,omitempty"`
}

type outputDoc struct {
	Output     string `yaml:"output"`
	Credential string `yaml:"credential"`
}

type tokenGrantDoc struct {
	GrantType           string                `yaml:"grant_type,omitempty"`
	TokenEndpoint       string                `yaml:"token_endpoint"`
	Audience            string                `yaml:"audience,omitempty"`
	JwtSvidAudience     string                `yaml:"jwt_svid_audience,omitempty"`
	ClientAssertionType string                `yaml:"client_assertion_type,omitempty"`
	Scopes              []string              `yaml:"scopes,omitempty,flow"`
	CacheTTLSeconds     int64                 `yaml:"cache_ttl_seconds,omitempty"`
	CacheTTL            string                `yaml:"cache_ttl,omitempty"`
	AudienceOverrides   []audienceOverrideDoc `yaml:"audience_overrides,omitempty"`
	SubjectToken        *subjectTokenDoc      `yaml:"subject_token,omitempty"`
	RequestedTokenType  string                `yaml:"requested_token_type,omitempty"`
}

type audienceOverrideDoc struct {
	Host     string   `yaml:"host,omitempty"`
	Port     uint32   `yaml:"port,omitempty"`
	Path     string   `yaml:"path,omitempty"`
	Audience string   `yaml:"audience"`
	Scopes   []string `yaml:"scopes,omitempty,flow"`
}

type subjectTokenDoc struct {
	Source           string `yaml:"source"`
	Credential       string `yaml:"credential"`
	SubjectTokenType string `yaml:"subject_token_type,omitempty"`
}

type discoveryDoc struct {
	Credentials []string `yaml:"credentials,omitempty,flow"`
}

type endpointDoc struct {
	Host     string   `yaml:"host"`
	Port     uint32   `yaml:"port,omitempty"`
	Ports    []uint32 `yaml:"ports,omitempty,flow"`
	Path     string   `yaml:"path,omitempty"`
	Protocol string   `yaml:"protocol,omitempty"`
	TLS      string   `yaml:"tls,omitempty"`
	Access   string   `yaml:"access,omitempty"`
	// Enforcement is enforce or audit.
	Enforcement string `yaml:"enforcement,omitempty"`
	// Rules and DenyRules are pointers: a present but empty list is an
	// error (OpenShell: it would deny everything, or nothing).
	Rules                        *[]ruleDoc              `yaml:"rules,omitempty"`
	DenyRules                    *[]matchDoc             `yaml:"deny_rules,omitempty"`
	AllowedIPs                   []string                `yaml:"allowed_ips,omitempty,flow"`
	AllowEncodedSlash            bool                    `yaml:"allow_encoded_slash,omitempty"`
	WebsocketCredentialRewrite   bool                    `yaml:"websocket_credential_rewrite,omitempty"`
	RequestBodyCredentialRewrite bool                    `yaml:"request_body_credential_rewrite,omitempty"`
	AllowUninspectedCredentials  bool                    `yaml:"allow_uninspected_credentials,omitempty"`
	PersistedQueries             string                  `yaml:"persisted_queries,omitempty"`
	GraphqlPersistedQueries      map[string]graphqlOpDoc `yaml:"graphql_persisted_queries,omitempty"`
	GraphqlMaxBodyBytes          uint32                  `yaml:"graphql_max_body_bytes,omitempty"`
	JSONRPCMaxBodyBytes          uint32                  `yaml:"json_rpc_max_body_bytes,omitempty"`
	MCP                          *mcpDoc                 `yaml:"mcp,omitempty"`
	CredentialSigning            string                  `yaml:"credential_signing,omitempty"`
	SigningService               string                  `yaml:"signing_service,omitempty"`
	SigningRegion                string                  `yaml:"signing_region,omitempty"`
}

type ruleDoc struct {
	Allow *matchDoc `yaml:"allow,omitempty,flow"`
}

// matchDoc is an allow rule or a deny rule (the same fields).
type matchDoc struct {
	Method        string                `yaml:"method,omitempty"`
	Path          string                `yaml:"path,omitempty"`
	Command       string                `yaml:"command,omitempty"`
	Query         map[string]matcherDoc `yaml:"query,omitempty"`
	OperationType string                `yaml:"operation_type,omitempty"`
	OperationName string                `yaml:"operation_name,omitempty"`
	Fields        []string              `yaml:"fields,omitempty,flow"`
	Params        map[string]matcherDoc `yaml:"params,omitempty"`
	// Tool is params.name for MCP (input only).
	Tool *matcherDoc `yaml:"tool,omitempty"`
}

type graphqlOpDoc struct {
	OperationType string   `yaml:"operation_type,omitempty"`
	OperationName string   `yaml:"operation_name,omitempty"`
	Fields        []string `yaml:"fields,omitempty,flow"`
}

type mcpDoc struct {
	Versions                []string `yaml:"versions,omitempty,flow"`
	StrictToolNames         *bool    `yaml:"strict_tool_names,omitempty"`
	AllowAllKnownMCPMethods *bool    `yaml:"allow_all_known_mcp_methods,omitempty"`
}

// matcherDoc is a glob as a string, or {glob, any}.
type matcherDoc struct {
	Glob string
	Any  []string
}

func (m *matcherDoc) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		return n.Decode(&m.Glob)
	}
	return strictMapping(n, map[string]any{"glob": &m.Glob, "any": &m.Any})
}

func (m matcherDoc) MarshalYAML() (any, error) {
	if len(m.Any) == 0 {
		return m.Glob, nil
	}
	return struct {
		Glob string   `yaml:"glob,omitempty"`
		Any  []string `yaml:"any,flow"`
	}{m.Glob, m.Any}, nil
}

// binaryDoc is a path, or {path}.
type binaryDoc struct{ Path string }

func (b *binaryDoc) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		return n.Decode(&b.Path)
	}
	return strictMapping(n, map[string]any{"path": &b.Path})
}

func (b binaryDoc) MarshalYAML() (any, error) { return b.Path, nil }

// strictMapping decodes a mapping's keys into fields, refusing other keys
// (yaml.Node.Decode does not inherit the decoder's KnownFields).
func strictMapping(n *yaml.Node, fields map[string]any) error {
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: expected a string or a mapping", n.Line)
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		dst, ok := fields[k.Value]
		if !ok {
			return fmt.Errorf("line %d: field %s not found", k.Line, k.Value)
		}
		if err := v.Decode(dst); err != nil {
			return err
		}
	}
	return nil
}

// parseProfileYAML reads one profile in OpenShell's format and lowers it
// to the proto, as `openshell provider profile import` does.
func parseProfileYAML(b []byte) (*pb.ProviderProfile, error) {
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	var doc profileDoc
	if err := dec.Decode(&doc); err != nil {
		return nil, err
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("a profile file holds one YAML document")
	}
	return doc.proto()
}

func (d profileDoc) proto() (*pb.ProviderProfile, error) {
	if d.ID == "" {
		return nil, errors.New("missing field id")
	}
	if d.DisplayName == "" {
		return nil, errors.New("missing field display_name")
	}
	cat, ok := profileCategories[yamlEnum(d.Category)]
	if !ok {
		return nil, fmt.Errorf("unsupported provider profile category: %s", d.Category)
	}
	p := &pb.ProviderProfile{
		Id: d.ID, ResourceVersion: d.ResourceVersion, Annotations: d.Annotations, DisplayName: d.DisplayName, Description: d.Description,
		Category: cat, InferenceCapable: d.InferenceCapable, Source: d.Source, Scope: d.Scope,
	}
	for i, c := range d.Credentials {
		pc, err := c.proto()
		if err != nil {
			return nil, fmt.Errorf("credentials[%d]: %w", i, err)
		}
		p.Credentials = append(p.Credentials, pc)
	}
	for i, e := range d.Endpoints {
		pe, err := e.proto()
		if err != nil {
			return nil, fmt.Errorf("endpoints[%d]: %w", i, err)
		}
		p.Endpoints = append(p.Endpoints, pe)
	}
	for i, b := range d.Binaries {
		if b.Path == "" {
			return nil, fmt.Errorf("binaries[%d]: missing field path", i)
		}
		p.Binaries = append(p.Binaries, &sb.NetworkBinary{Path: b.Path})
	}
	if d.Discovery != nil && len(d.Discovery.Credentials) > 0 {
		p.Discovery = &pb.ProviderProfileDiscovery{Credentials: d.Discovery.Credentials}
	}
	return p, nil
}

func (c credentialDoc) proto() (*pb.ProviderProfileCredential, error) {
	if c.Name == "" {
		return nil, errors.New("missing field name")
	}
	pc := &pb.ProviderProfileCredential{
		Name: c.Name, Description: c.Description, EnvVars: c.EnvVars, Required: c.Required,
		AuthStyle: c.AuthStyle, HeaderName: c.HeaderName, QueryParam: c.QueryParam, PathTemplate: c.PathTemplate,
	}
	if r := c.Refresh; r != nil {
		strategy, ok := refreshStrategies[yamlEnum(r.Strategy)]
		if !ok {
			return nil, fmt.Errorf("unsupported provider refresh strategy: %s", r.Strategy)
		}
		pr := &pb.ProviderCredentialRefresh{Strategy: strategy, TokenUrl: r.TokenURL, Scopes: r.Scopes}
		var err error
		if pr.RefreshBefore, err = profileDuration("refresh.refresh_before", r.RefreshBefore, r.RefreshBeforeSeconds); err != nil {
			return nil, err
		}
		if pr.MaxLifetime, err = profileDuration("refresh.max_lifetime", r.MaxLifetime, r.MaxLifetimeSeconds); err != nil {
			return nil, err
		}
		for _, m := range r.Material {
			if m.Name == "" {
				return nil, errors.New("refresh.material: missing field name")
			}
			pr.Material = append(pr.Material, &pb.ProviderCredentialRefreshMaterial{Name: m.Name, Description: m.Description, Required: m.Required, Secret: m.Secret})
		}
		for _, o := range r.AdditionalOutputs {
			pr.AdditionalOutputs = append(pr.AdditionalOutputs, &pb.ProviderCredentialRefreshOutput{Output: o.Output, Credential: o.Credential})
		}
		pc.Refresh = pr
	}
	if g := c.TokenGrant; g != nil {
		grant, ok := tokenGrantTypes[yamlEnum(g.GrantType)]
		if !ok {
			return nil, fmt.Errorf("unsupported provider token grant type: %s", g.GrantType)
		}
		if g.TokenEndpoint == "" {
			return nil, errors.New("token_grant: missing field token_endpoint")
		}
		pg := &pb.ProviderCredentialTokenGrant{
			GrantType: grant, TokenEndpoint: g.TokenEndpoint, Audience: g.Audience, JwtSvidAudience: g.JwtSvidAudience,
			ClientAssertionType: g.ClientAssertionType, Scopes: g.Scopes, RequestedTokenType: g.RequestedTokenType,
		}
		var err error
		if pg.CacheTtl, err = profileDuration("token_grant.cache_ttl", g.CacheTTL, g.CacheTTLSeconds); err != nil {
			return nil, err
		}
		for _, o := range g.AudienceOverrides {
			pg.AudienceOverrides = append(pg.AudienceOverrides, &pb.ProviderCredentialTokenGrantAudienceOverride{Host: o.Host, Port: o.Port, Path: o.Path, Audience: o.Audience, Scopes: o.Scopes})
		}
		if s := g.SubjectToken; s != nil {
			pg.SubjectToken = &pb.ProviderCredentialTokenGrantSubjectToken{Source: s.Source, Credential: s.Credential, SubjectTokenType: s.SubjectTokenType}
		}
		pc.TokenGrant = pg
	}
	return pc, nil
}

func (e endpointDoc) proto() (*sb.NetworkEndpoint, error) {
	if e.Host == "" {
		return nil, errors.New("missing field host")
	}
	tls, ok := tlsModes[e.TLS]
	if !ok {
		return nil, fmt.Errorf("unknown tls %q", e.TLS)
	}
	access, ok := accessPresets[e.Access]
	if !ok {
		return nil, fmt.Errorf("unknown access %q (read-only, read-write or full)", e.Access)
	}
	enforcement, ok := enforcementModes[e.Enforcement]
	if !ok {
		return nil, fmt.Errorf("unknown enforcement %q (enforce or audit)", e.Enforcement)
	}
	pe := &sb.NetworkEndpoint{
		Host: e.Host, Port: e.Port, Ports: e.Ports, Path: e.Path, Protocol: e.Protocol, Tls: tls, Access: access, Enforcement: enforcement,
		AllowedIps: e.AllowedIPs, AllowEncodedSlash: e.AllowEncodedSlash, WebsocketCredentialRewrite: e.WebsocketCredentialRewrite,
		RequestBodyCredentialRewrite: e.RequestBodyCredentialRewrite, AllowUninspectedCredentials: e.AllowUninspectedCredentials,
		PersistedQueries: e.PersistedQueries, GraphqlMaxBodyBytes: e.GraphqlMaxBodyBytes, JsonRpcMaxBodyBytes: e.JSONRPCMaxBodyBytes,
		CredentialSigning: e.CredentialSigning, SigningService: e.SigningService, SigningRegion: e.SigningRegion,
	}
	if e.Rules != nil {
		if len(*e.Rules) == 0 {
			return nil, errors.New("rules list cannot be empty (would deny all traffic); use access: full or remove rules")
		}
		for i, r := range *e.Rules {
			rule := &sb.L7Rule{}
			if r.Allow != nil {
				a, err := r.Allow.allow()
				if err != nil {
					return nil, fmt.Errorf("rules[%d].allow: %w", i, err)
				}
				rule.Allow = a
			}
			pe.Rules = append(pe.Rules, rule)
		}
	}
	if e.DenyRules != nil {
		if len(*e.DenyRules) == 0 {
			return nil, errors.New("deny_rules list cannot be empty (would have no effect); remove it")
		}
		for i, r := range *e.DenyRules {
			a, err := r.allow()
			if err != nil {
				return nil, fmt.Errorf("deny_rules[%d]: %w", i, err)
			}
			pe.DenyRules = append(pe.DenyRules, &sb.L7DenyRule{Method: a.Method, Path: a.Path, Command: a.Command, Query: a.Query,
				OperationType: a.OperationType, OperationName: a.OperationName, Fields: a.Fields, Params: a.Params})
		}
	}
	if len(e.GraphqlPersistedQueries) > 0 {
		pe.GraphqlPersistedQueries = map[string]*sb.GraphqlOperation{}
		for k, op := range e.GraphqlPersistedQueries {
			pe.GraphqlPersistedQueries[k] = &sb.GraphqlOperation{OperationType: op.OperationType, OperationName: op.OperationName, Fields: op.Fields}
		}
	}
	if m := e.MCP; m != nil {
		pe.Mcp = &sb.McpOptions{Versions: m.Versions, StrictToolNames: m.StrictToolNames, AllowAllKnownMcpMethods: m.AllowAllKnownMCPMethods}
	}
	return pe, nil
}

func (m matchDoc) allow() (*sb.L7Allow, error) {
	a := &sb.L7Allow{Method: m.Method, Path: m.Path, Command: m.Command, OperationType: m.OperationType, OperationName: m.OperationName, Fields: m.Fields,
		Query: matchersProto(m.Query), Params: matchersProto(m.Params)}
	if m.Tool != nil {
		if _, ok := a.Params["name"]; ok {
			return nil, errors.New("MCP rules must use either tool or params.name, not both")
		}
		if a.Params == nil {
			a.Params = map[string]*sb.L7QueryMatcher{}
		}
		a.Params["name"] = &sb.L7QueryMatcher{Glob: m.Tool.Glob, Any: m.Tool.Any}
	}
	return a, nil
}

func matchersProto(in map[string]matcherDoc) map[string]*sb.L7QueryMatcher {
	if len(in) == 0 {
		return nil
	}
	out := map[string]*sb.L7QueryMatcher{}
	for k, v := range in {
		out[k] = &sb.L7QueryMatcher{Glob: v.Glob, Any: v.Any}
	}
	return out
}

func matchersDoc(in map[string]*sb.L7QueryMatcher) map[string]matcherDoc {
	if len(in) == 0 {
		return nil
	}
	out := map[string]matcherDoc{}
	for k, v := range in {
		out[k] = matcherDoc{Glob: v.GetGlob(), Any: v.GetAny()}
	}
	return out
}

// profileDuration is a protobuf duration ("300s", "0.500s") or the legacy
// whole seconds; absent stays absent (the gateway's default).
func profileDuration(field, s string, seconds int64) (*durationpb.Duration, error) {
	if s != "" && seconds != 0 {
		return nil, fmt.Errorf("%s and %s_seconds are both set", field, field)
	}
	if s == "" {
		if seconds == 0 {
			return nil, nil
		}
		return &durationpb.Duration{Seconds: seconds}, nil
	}
	num, ok := strings.CutSuffix(s, "s")
	if !ok || num == "" || strings.ContainsAny(num, "eE+_") {
		return nil, fmt.Errorf("%s: %q is not a duration like \"300s\"", field, s)
	}
	neg := strings.HasPrefix(num, "-")
	whole, frac, _ := strings.Cut(strings.TrimPrefix(num, "-"), ".")
	if whole == "" || len(frac) > 9 {
		return nil, fmt.Errorf("%s: %q is not a duration like \"300s\"", field, s)
	}
	sec, err := strconv.ParseInt(whole, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("%s: %q is not a duration like \"300s\"", field, s)
	}
	var nanos int64
	if frac != "" {
		if nanos, err = strconv.ParseInt(frac+strings.Repeat("0", 9-len(frac)), 10, 32); err != nil {
			return nil, fmt.Errorf("%s: %q is not a duration like \"300s\"", field, s)
		}
	}
	if neg {
		sec, nanos = -sec, -nanos
	}
	return &durationpb.Duration{Seconds: sec, Nanos: int32(nanos)}, nil
}

// durationText writes a duration as OpenShell does ("300s", "0.500s").
func durationText(d *durationpb.Duration) string {
	if d == nil {
		return ""
	}
	sec, nanos := d.GetSeconds(), int64(d.GetNanos())
	sign := ""
	if sec < 0 || nanos < 0 {
		sign, sec, nanos = "-", -sec, -nanos
	}
	if nanos == 0 {
		return sign + strconv.FormatInt(sec, 10) + "s"
	}
	frac := fmt.Sprintf("%09d", nanos)
	switch {
	case strings.HasSuffix(frac, "000000"):
		frac = frac[:3]
	case strings.HasSuffix(frac, "000"):
		frac = frac[:6]
	}
	return sign + strconv.FormatInt(sec, 10) + "." + frac + "s"
}

// yamlEnum normalizes an enum as OpenShell reads categories, strategies
// and grant types: case and - against _ do not matter.
func yamlEnum(s string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(s)), "-", "_")
}

var (
	// An absent category is other, as in OpenShell.
	profileCategories = map[string]pb.ProviderProfileCategory{
		"": pb.ProviderProfileCategory_PROVIDER_PROFILE_CATEGORY_OTHER, "other": pb.ProviderProfileCategory_PROVIDER_PROFILE_CATEGORY_OTHER,
		"inference": pb.ProviderProfileCategory_PROVIDER_PROFILE_CATEGORY_INFERENCE, "agent": pb.ProviderProfileCategory_PROVIDER_PROFILE_CATEGORY_AGENT,
		"source_control": pb.ProviderProfileCategory_PROVIDER_PROFILE_CATEGORY_SOURCE_CONTROL, "messaging": pb.ProviderProfileCategory_PROVIDER_PROFILE_CATEGORY_MESSAGING,
		"data": pb.ProviderProfileCategory_PROVIDER_PROFILE_CATEGORY_DATA, "knowledge": pb.ProviderProfileCategory_PROVIDER_PROFILE_CATEGORY_KNOWLEDGE,
	}
	refreshStrategies = map[string]pb.ProviderCredentialRefreshStrategy{
		"":                           pb.ProviderCredentialRefreshStrategy_PROVIDER_CREDENTIAL_REFRESH_STRATEGY_UNSPECIFIED,
		"static":                     pb.ProviderCredentialRefreshStrategy_PROVIDER_CREDENTIAL_REFRESH_STRATEGY_STATIC,
		"external":                   pb.ProviderCredentialRefreshStrategy_PROVIDER_CREDENTIAL_REFRESH_STRATEGY_EXTERNAL,
		"oauth2_refresh_token":       pb.ProviderCredentialRefreshStrategy_PROVIDER_CREDENTIAL_REFRESH_STRATEGY_OAUTH2_REFRESH_TOKEN,
		"oauth2_client_credentials":  pb.ProviderCredentialRefreshStrategy_PROVIDER_CREDENTIAL_REFRESH_STRATEGY_OAUTH2_CLIENT_CREDENTIALS,
		"google_service_account_jwt": pb.ProviderCredentialRefreshStrategy_PROVIDER_CREDENTIAL_REFRESH_STRATEGY_GOOGLE_SERVICE_ACCOUNT_JWT,
		"aws_sts_assume_role":        pb.ProviderCredentialRefreshStrategy_PROVIDER_CREDENTIAL_REFRESH_STRATEGY_AWS_STS_ASSUME_ROLE,
	}
	// An absent grant type is client_credentials, as in OpenShell.
	tokenGrantTypes = map[string]pb.ProviderCredentialTokenGrantType{
		"": pb.ProviderCredentialTokenGrantType_PROVIDER_CREDENTIAL_TOKEN_GRANT_TYPE_CLIENT_CREDENTIALS, "client_credentials": pb.ProviderCredentialTokenGrantType_PROVIDER_CREDENTIAL_TOKEN_GRANT_TYPE_CLIENT_CREDENTIALS,
		"token_exchange": pb.ProviderCredentialTokenGrantType_PROVIDER_CREDENTIAL_TOKEN_GRANT_TYPE_TOKEN_EXCHANGE,
	}
	// The network enums are exact strings (l7_validate.rs).
	tlsModes = map[string]sb.NetworkTlsMode{
		"": sb.NetworkTlsMode_NETWORK_TLS_MODE_UNSPECIFIED, "skip": sb.NetworkTlsMode_NETWORK_TLS_MODE_SKIP,
		"terminate": sb.NetworkTlsMode_NETWORK_TLS_MODE_TERMINATE, "passthrough": sb.NetworkTlsMode_NETWORK_TLS_MODE_PASSTHROUGH,
	}
	accessPresets = map[string]sb.NetworkAccessPreset{
		"": sb.NetworkAccessPreset_NETWORK_ACCESS_PRESET_UNSPECIFIED, "read-only": sb.NetworkAccessPreset_NETWORK_ACCESS_PRESET_READ_ONLY,
		"read-write": sb.NetworkAccessPreset_NETWORK_ACCESS_PRESET_READ_WRITE, "full": sb.NetworkAccessPreset_NETWORK_ACCESS_PRESET_FULL,
	}
	enforcementModes = map[string]sb.NetworkEnforcementMode{
		"": sb.NetworkEnforcementMode_NETWORK_ENFORCEMENT_MODE_UNSPECIFIED, "enforce": sb.NetworkEnforcementMode_NETWORK_ENFORCEMENT_MODE_ENFORCE,
		"audit": sb.NetworkEnforcementMode_NETWORK_ENFORCEMENT_MODE_AUDIT,
	}
)

// enumName is the YAML name of an enum value, or "unknown(N)" as
// OpenShell's export writes a value it does not know.
func enumName[T comparable](names map[string]T, v T, num int32) string {
	for k, x := range names {
		// "" and a named alias of the same value: prefer the name.
		if x == v && k != "" {
			return k
		}
	}
	for k, x := range names {
		if x == v {
			return k
		}
	}
	return fmt.Sprintf("unknown(%d)", num)
}

// profileYAML renders a profile the gateway holds in OpenShell's format
// (as `openshell profile export -o yaml`, leaving out empty fields).
// Annotations, resource_version, source and scope are the gateway's
// bookkeeping and are left out too.
func profileYAML(p *pb.ProviderProfile) (string, error) {
	d := profileDoc{ID: p.GetId(), DisplayName: p.GetDisplayName(), Description: p.GetDescription(), InferenceCapable: p.GetInferenceCapable()}
	d.Category = enumName(profileCategories, p.GetCategory(), int32(p.GetCategory()))
	if p.GetCategory() == pb.ProviderProfileCategory_PROVIDER_PROFILE_CATEGORY_UNSPECIFIED {
		d.Category = "other"
	}
	for _, c := range p.GetCredentials() {
		cd := credentialDoc{Name: c.GetName(), Description: c.GetDescription(), EnvVars: c.GetEnvVars(), Required: c.GetRequired(), AuthStyle: c.GetAuthStyle(),
			HeaderName: c.GetHeaderName(), QueryParam: c.GetQueryParam(), PathTemplate: c.GetPathTemplate()}
		if r := c.GetRefresh(); r != nil {
			rd := &refreshDoc{Strategy: enumName(refreshStrategies, r.GetStrategy(), int32(r.GetStrategy())), TokenURL: r.GetTokenUrl(), Scopes: r.GetScopes(),
				RefreshBefore: durationText(r.GetRefreshBefore()), MaxLifetime: durationText(r.GetMaxLifetime())}
			if r.GetStrategy() == pb.ProviderCredentialRefreshStrategy_PROVIDER_CREDENTIAL_REFRESH_STRATEGY_UNSPECIFIED {
				rd.Strategy = ""
			}
			for _, m := range r.GetMaterial() {
				rd.Material = append(rd.Material, materialDoc{Name: m.GetName(), Description: m.GetDescription(), Required: m.GetRequired(), Secret: m.GetSecret()})
			}
			for _, o := range r.GetAdditionalOutputs() {
				rd.AdditionalOutputs = append(rd.AdditionalOutputs, outputDoc{Output: o.GetOutput(), Credential: o.GetCredential()})
			}
			cd.Refresh = rd
		}
		if g := c.GetTokenGrant(); g != nil {
			gd := &tokenGrantDoc{TokenEndpoint: g.GetTokenEndpoint(), Audience: g.GetAudience(), JwtSvidAudience: g.GetJwtSvidAudience(),
				ClientAssertionType: g.GetClientAssertionType(), Scopes: g.GetScopes(), CacheTTL: durationText(g.GetCacheTtl()), RequestedTokenType: g.GetRequestedTokenType()}
			if g.GetGrantType() == pb.ProviderCredentialTokenGrantType_PROVIDER_CREDENTIAL_TOKEN_GRANT_TYPE_TOKEN_EXCHANGE {
				gd.GrantType = "token_exchange"
			}
			for _, o := range g.GetAudienceOverrides() {
				gd.AudienceOverrides = append(gd.AudienceOverrides, audienceOverrideDoc{Host: o.GetHost(), Port: o.GetPort(), Path: o.GetPath(), Audience: o.GetAudience(), Scopes: o.GetScopes()})
			}
			if s := g.GetSubjectToken(); s != nil {
				gd.SubjectToken = &subjectTokenDoc{Source: s.GetSource(), Credential: s.GetCredential(), SubjectTokenType: s.GetSubjectTokenType()}
			}
			cd.TokenGrant = gd
		}
		d.Credentials = append(d.Credentials, cd)
	}
	for _, e := range p.GetEndpoints() {
		ed := endpointDoc{Host: e.GetHost(), Port: e.GetPort(), Ports: e.GetPorts(), Path: e.GetPath(), Protocol: e.GetProtocol(),
			TLS: enumName(tlsModes, e.GetTls(), int32(e.GetTls())), Access: enumName(accessPresets, e.GetAccess(), int32(e.GetAccess())),
			Enforcement: enumName(enforcementModes, e.GetEnforcement(), int32(e.GetEnforcement())), AllowedIPs: e.GetAllowedIps(),
			AllowEncodedSlash: e.GetAllowEncodedSlash(), WebsocketCredentialRewrite: e.GetWebsocketCredentialRewrite(),
			RequestBodyCredentialRewrite: e.GetRequestBodyCredentialRewrite(), AllowUninspectedCredentials: e.GetAllowUninspectedCredentials(),
			PersistedQueries: e.GetPersistedQueries(), GraphqlMaxBodyBytes: e.GetGraphqlMaxBodyBytes(), JSONRPCMaxBodyBytes: e.GetJsonRpcMaxBodyBytes(),
			CredentialSigning: e.GetCredentialSigning(), SigningService: e.GetSigningService(), SigningRegion: e.GetSigningRegion()}
		if len(e.GetRules()) > 0 {
			rules := []ruleDoc{}
			for _, r := range e.GetRules() {
				rd := ruleDoc{}
				if a := r.GetAllow(); a != nil {
					rd.Allow = &matchDoc{Method: a.GetMethod(), Path: a.GetPath(), Command: a.GetCommand(), Query: matchersDoc(a.GetQuery()),
						OperationType: a.GetOperationType(), OperationName: a.GetOperationName(), Fields: a.GetFields(), Params: matchersDoc(a.GetParams())}
				}
				rules = append(rules, rd)
			}
			ed.Rules = &rules
		}
		if len(e.GetDenyRules()) > 0 {
			deny := []matchDoc{}
			for _, a := range e.GetDenyRules() {
				deny = append(deny, matchDoc{Method: a.GetMethod(), Path: a.GetPath(), Command: a.GetCommand(), Query: matchersDoc(a.GetQuery()),
					OperationType: a.GetOperationType(), OperationName: a.GetOperationName(), Fields: a.GetFields(), Params: matchersDoc(a.GetParams())})
			}
			ed.DenyRules = &deny
		}
		if len(e.GetGraphqlPersistedQueries()) > 0 {
			ed.GraphqlPersistedQueries = map[string]graphqlOpDoc{}
			for k, op := range e.GetGraphqlPersistedQueries() {
				ed.GraphqlPersistedQueries[k] = graphqlOpDoc{OperationType: op.GetOperationType(), OperationName: op.GetOperationName(), Fields: op.GetFields()}
			}
		}
		if m := e.GetMcp(); m != nil {
			ed.MCP = &mcpDoc{Versions: m.GetVersions(), StrictToolNames: m.StrictToolNames, AllowAllKnownMCPMethods: m.AllowAllKnownMcpMethods}
		}
		d.Endpoints = append(d.Endpoints, ed)
	}
	for _, b := range p.GetBinaries() {
		d.Binaries = append(d.Binaries, binaryDoc{Path: b.GetPath()})
	}
	if creds := p.GetDiscovery().GetCredentials(); len(creds) > 0 {
		d.Discovery = &discoveryDoc{Credentials: creds}
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(d); err != nil {
		return "", err
	}
	if err := enc.Close(); err != nil {
		return "", err
	}
	return buf.String(), nil
}
