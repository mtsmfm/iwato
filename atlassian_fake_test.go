//go:build uitest

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAtlassianDiscovery(t *testing.T) {
	f := newFakeAtlassian(t)
	md, err := f.oauth().Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if md.Issuer != f.srv.URL+"/tenant1" || md.TokenEndpoint != f.srv.URL+"/oauth/token" || md.RegistrationEndpoint != f.srv.URL+"/tenant1/dcr/register" || md.Resource != f.srv.URL+"/v2/mcp" || len(md.Scopes) != len(atlassianScopes) {
		t.Fatalf("%+v", md)
	}
	want := []string{"offline_access", "read:me", "read:jira:agent-interface", "write:jira:agent-interface", "search:jira:agent-interface", "read:confluence:agent-interface", "write:confluence:agent-interface", "search:confluence:agent-interface"}
	if got := defaultAtlassianScopes(md.Scopes); !slices.Equal(got, want) {
		t.Fatal(got)
	}
	// Only offline_access when the resource lists none.
	if got := defaultAtlassianScopes(nil); !slices.Equal(got, []string{"offline_access"}) {
		t.Fatal(got)
	}
}

// spyRuntime records the refresh configuration Iwato gives the gateway.
type spyRuntime struct {
	*Fake
	mu         sync.Mutex
	calls      []string
	material   []map[string]string
	secretKeys [][]string
	expires    []time.Time
	rotateErr  error
}

func newOAuthTest(t *testing.T) *oauthTest {
	c := testConfig()
	d := NewFake()
	d.seedProviders()
	rt := &spyRuntime{Fake: d}
	s := newServer(c, rt, false, "127.0.0.1:29999")
	fake := newFakeAtlassian(t)
	s.oauth.backend = fake.oauth()
	cookie, csrf := reviewLogin(t, s)
	var logs bytes.Buffer
	var logMu sync.Mutex
	log.SetOutput(writerFunc(func(p []byte) (int, error) { logMu.Lock(); defer logMu.Unlock(); return logs.Write(p) }))
	t.Cleanup(func() { log.SetOutput(io.Discard) })
	do := func(method, path, body string) (int, string) {
		w := reviewRequest(s, method, path, body, s.origin, "same-origin", s.host, cookie, csrf)
		return w.Code, w.Body.String()
	}
	return &oauthTest{oauthFlowTest: &oauthFlowTest{t: t, s: s, do: do}, rt: rt, fake: fake, logs: &logs}
}

// The fake's authorization page sends the browser back to the loopback
// redirect with the state; anything else is refused.
func TestFakeAuthorizePage(t *testing.T) {
	_, _, do := providerServer(t)
	state := strings.Repeat("ab", 32)
	code, body := do("GET", "/fake/atlassian/authorize?redirect_uri="+url.QueryEscape("http://127.0.0.1:4567/callback")+"&state="+state+"&code_challenge_method=S256&scope=offline_access", "")
	if code != 200 || !strings.Contains(body, `href="http://127.0.0.1:4567/callback?code=fake-code&amp;state=`+state+`"`) {
		t.Fatal(code, body)
	}
	if code, _ := do("GET", "/fake/atlassian/authorize?redirect_uri="+url.QueryEscape("https://evil.example/callback")+"&state="+state+"&code_challenge_method=S256", ""); code != 400 {
		t.Fatal("another redirect", code)
	}
	s := newServer(testConfig(), NewFake(), false, "127.0.0.1:29999")
	if w := reviewRequest(s, "GET", "/fake/atlassian/authorize", "", "", "", s.host, nil, ""); w.Code == 200 && strings.Contains(w.Body.String(), "許可する") {
		t.Fatal("the fake page outside the fake")
	}
}

// The gateway refreshes at the profile's token_url: another token endpoint
// stops the flow before anything is registered.
func TestAtlassianDiscoveryRefusesAnotherTokenURL(t *testing.T) {
	f := newFakeAtlassian(t)
	f.tokenEndpoint = f.srv.URL + "/other/token"
	_, err := f.oauth().Discover(context.Background())
	if !errors.Is(err, errTokenURL) {
		t.Fatal(err)
	}
	if !strings.Contains(discoveryMessage(err), "プロファイル (iwato-atlassian) と違うので中止") {
		t.Fatal(discoveryMessage(err))
	}
	// The real profile's URL against the test server's metadata.
	o := f.oauth()
	f.tokenEndpoint = ""
	o.TokenURL = atlassianTokenURL
	if _, err := o.Discover(context.Background()); !errors.Is(err, errTokenURL) {
		t.Fatal(err)
	}
	// A plain http resource is refused.
	o = f.oauth()
	o.Resource = "http://mcp.example/v2/mcp"
	if _, err := o.Discover(context.Background()); err == nil {
		t.Fatal("http resource accepted")
	}
}

func TestAtlassianRegisterAndExchange(t *testing.T) {
	f := newFakeAtlassian(t)
	o := f.oauth()
	ctx := context.Background()
	md, err := o.Discover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	id, err := o.Register(ctx, md, "http://127.0.0.1:5555/callback")
	if err != nil || id != "client-123" {
		t.Fatal(id, err)
	}
	reg := f.registered[0]
	if reg["token_endpoint_auth_method"] != "none" || reg["client_name"] != "Iwato" || reg["redirect_uris"].([]any)[0] != "http://127.0.0.1:5555/callback" || len(reg["grant_types"].([]any)) != 2 {
		t.Fatal(reg)
	}
	verifier, challenge := pkce()
	if len(verifier) != 43 || len(challenge) != 43 || verifier == challenge {
		t.Fatal(verifier, challenge)
	}
	u, _ := url.Parse(authorizeURL(md, id, "http://127.0.0.1:5555/callback", "st", challenge, []string{"offline_access", "read:me"}))
	q := u.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") != challenge || q.Get("resource") != md.Resource || q.Get("scope") != "offline_access read:me" || q.Get("state") != "st" || q.Get("response_type") != "code" {
		t.Fatal(u)
	}
	tok, err := o.Exchange(ctx, md, id, "good-code", verifier, "http://127.0.0.1:5555/callback")
	if err != nil || tok.AccessToken != "at-SECRET-1" || tok.RefreshToken != "rt-SECRET-1" || tok.ExpiresIn != 3600 || tok.Lifetimes["refresh_token_expires_in"] != 7776000 {
		t.Fatalf("%+v %v", tok, err)
	}
	form := f.exchanged[0]
	for k, v := range map[string]string{"grant_type": "authorization_code", "code_verifier": verifier, "client_id": "client-123", "resource": md.Resource, "redirect_uri": "http://127.0.0.1:5555/callback"} {
		if form.Get(k) != v {
			t.Errorf("%s = %q", k, form.Get(k))
		}
	}
	if form.Has("client_secret") {
		t.Error("a public client sent a secret")
	}
	// An error is its code only: never the description (it echoes input).
	_, err = o.Exchange(ctx, md, id, "bad-code", verifier, "http://127.0.0.1:5555/callback")
	if err == nil || !strings.HasSuffix(err.Error(), "HTTP 400: invalid_grant") || strings.Contains(err.Error(), "ECHO") {
		t.Fatal(err)
	}
	// Scopes: offline_access always, nothing the resource does not list.
	if got, err := checkScopes([]string{"read:me"}, md.Scopes); err != nil || !slices.Equal(got, []string{"offline_access", "read:me"}) {
		t.Fatal(got, err)
	}
	if _, err := checkScopes([]string{"admin:everything"}, md.Scopes); err == nil {
		t.Fatal("an unknown scope")
	}
	if _, err := checkScopes([]string{"read me"}, nil); err == nil {
		t.Fatal("a malformed scope")
	}
}

func (r *spyRuntime) ConfigureRefresh(ctx context.Context, provider, key string, material map[string]string, secretKeys []string, expiresAt time.Time) error {
	r.mu.Lock()
	r.calls = append(r.calls, "configure "+provider+" "+key)
	r.material = append(r.material, map[string]string{"client_id": material["client_id"], "refresh_token": material["refresh_token"]})
	r.secretKeys = append(r.secretKeys, secretKeys)
	r.expires = append(r.expires, expiresAt)
	r.mu.Unlock()
	return r.Fake.ConfigureRefresh(ctx, provider, key, material, secretKeys, expiresAt)
}

func (r *spyRuntime) DeleteRefresh(ctx context.Context, provider, key string) error {
	r.mu.Lock()
	r.calls = append(r.calls, "delete-refresh "+provider)
	r.mu.Unlock()
	return r.Fake.DeleteRefresh(ctx, provider, key)
}

func (r *spyRuntime) UpdateProvider(ctx context.Context, name string, creds map[string]string) error {
	r.mu.Lock()
	r.calls = append(r.calls, "update "+name+" "+strings.Join(mapKeys(creds), ","))
	r.mu.Unlock()
	return r.Fake.UpdateProvider(ctx, name, creds)
}

func (r *spyRuntime) CreateProvider(ctx context.Context, name, profile string, creds map[string]string) error {
	r.mu.Lock()
	r.calls = append(r.calls, "create "+name+" "+profile+" "+strings.Join(mapKeys(creds), ","))
	r.mu.Unlock()
	return r.Fake.CreateProvider(ctx, name, profile, creds)
}

func (r *spyRuntime) RotateRefresh(ctx context.Context, provider, key string) (RefreshInfo, error) {
	r.mu.Lock()
	r.calls = append(r.calls, "rotate "+provider)
	err := r.rotateErr
	r.mu.Unlock()
	if err != nil {
		r.Fake.mu.Lock()
		r.Fake.prov.refresh[provider] = RefreshInfo{Status: "configuration_required", Recovery: "fix_configuration", FailureCode: "oauth_invalid_target"}
		r.Fake.mu.Unlock()
		return RefreshInfo{}, err
	}
	return r.Fake.RotateRefresh(ctx, provider, key)
}

func (r *spyRuntime) log() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.calls)
}

// The whole flow with the browser's redirect: discovery for the dialog,
// a wrong state is refused, the right one is taken once; the provider is
// created and the gateway gets the grant; Iwato logs lifetimes, never
// values, and closes the listener.
func TestAtlassianFlowCreatesTheProvider(t *testing.T) {
	o := newOAuthTest(t)
	code, body := o.do("GET", "/api/oauth/atlassian", "")
	if code != 200 || !strings.Contains(body, `{"name":"write:jira:agent-interface","default":true}`) || !strings.Contains(body, `{"name":"read:rovo:agent-interface","default":false}`) || !strings.Contains(body, "'Authorization: Bearer ${ATLASSIAN_ACCESS_TOKEN}'") {
		t.Fatal(code, body)
	}
	// Validation before anything is registered.
	for _, c := range []struct{ body, want string }{
		{`{"name":"Bad","scopes":[]}`, "名前は"},
		{`{"name":"atlassian-work","scopes":[]}`, "もうあります"},
		{`{"name":"nope","scopes":[],"reconnect":true}`, "がありません"},
		{`{"name":"github-work","scopes":[],"reconnect":true}`, "Atlassian のプロバイダーではありません"},
		{`{"name":"new","scopes":["admin:all"]}`, "提供していない"},
	} {
		if code, resp := o.do("POST", "/api/oauth/atlassian/start", c.body); code < 400 || !strings.Contains(resp, c.want) {
			t.Errorf("%s: %d %s", c.body, code, resp)
		}
	}
	if len(o.fake.registered) != 0 {
		t.Fatal("registered before validating")
	}
	v := o.start(`{"name":"atlassian-new","scopes":["read:me","read:jira:agent-interface"]}`)
	if v.Status != "waiting" || !strings.HasPrefix(v.RedirectURI, "http://127.0.0.1:") || !strings.HasPrefix(v.AuthorizeURL, o.fake.srv.URL+"/authorize?") || !slices.Equal(v.Scopes, []string{"offline_access", "read:me", "read:jira:agent-interface"}) {
		t.Fatalf("%+v", v)
	}
	if got := o.fake.registered[0]["redirect_uris"].([]any)[0]; got != v.RedirectURI {
		t.Fatal(got)
	}
	state := stateOf(t, v.AuthorizeURL)
	// Not the flow's state: refused, the flow waits on.
	if code, _ := callbackGet(t, v.RedirectURI, "code=good-code&state="+strings.Repeat("0", 64)); code != 400 {
		t.Fatal("wrong state taken:", code)
	}
	if resp, err := http.Get(strings.TrimSuffix(v.RedirectURI, "/callback") + "/other"); err != nil || resp.StatusCode != 404 {
		t.Fatal("another path", err)
	}
	if o.flow(v.ID).Status != "waiting" {
		t.Fatal("a wrong state ended the flow")
	}
	code, page := callbackGet(t, v.RedirectURI, "code=good-code&state="+state)
	if code != 200 || !strings.Contains(page, "Iwato の画面に戻って") {
		t.Fatal(code, page)
	}
	done := o.wait(v.ID, "done", "failed")
	if done.Status != "done" || done.Warning != "" || done.Refresh == nil || done.Refresh.Status != "refreshed" {
		t.Fatalf("%+v", done)
	}
	// The exchange had the verifier whose challenge was in the URL.
	form := o.fake.exchanged[0]
	if form.Get("code") != "good-code" || form.Get("resource") != o.fake.srv.URL+"/v2/mcp" || form.Get("client_id") != "client-123" {
		t.Fatal(form)
	}
	if want := []string{"create atlassian-new iwato-atlassian ATLASSIAN_ACCESS_TOKEN", "configure atlassian-new ATLASSIAN_ACCESS_TOKEN", "rotate atlassian-new"}; !slices.Equal(o.rt.log(), want) {
		t.Fatal(o.rt.log())
	}
	if m := o.rt.material[0]; m["client_id"] != "client-123" || m["refresh_token"] != "rt-SECRET-1" || !slices.Equal(o.rt.secretKeys[0], []string{"refresh_token"}) {
		t.Fatal(m)
	}
	if d := time.Until(o.rt.expires[0]); d < 3500*time.Second || d > 3600*time.Second {
		t.Fatal("expires", d)
	}
	// Iwato kept no token in the flow.
	o.s.oauth.mu.Lock()
	f := o.s.oauth.current
	o.s.oauth.mu.Unlock()
	if f.srv != nil {
		t.Fatal("the listener is still referenced")
	}
	// The listener is gone: the second redirect finds nobody.
	waitClosed(t, v.RedirectURI)
	p, ok := provider(listProviders(t, o.do), "atlassian-new")
	if !ok || p.Refresh == nil || p.Refresh.Status != "refreshed" || p.Refresh.NextRefreshAt == "" {
		t.Fatalf("%+v", p)
	}
	logs := o.logs.String()
	if strings.Contains(logs, "SECRET") || strings.Contains(logs, "good-code") || strings.Contains(logs, state) {
		t.Fatal("a value in the log:", logs)
	}
	if !strings.Contains(logs, "refresh_token_expires_in:7776000") || !strings.Contains(logs, "refresh_token true") || !strings.Contains(logs, "access token lifetime 1h0m0s") {
		t.Fatal("lifetimes not logged:", logs)
	}
	// The created provider takes no values from the page.
	if code, resp := o.do("POST", "/api/providers/atlassian-new/update", `{"values":{"access_token":"x"}}`); code != 409 || !strings.Contains(resp, "再接続") {
		t.Fatal(code, resp)
	}
	if code, resp := o.do("POST", "/api/providers", `{"name":"atl","profile":"iwato-atlassian","values":{"access_token":"x"}}`); code != 400 || !strings.Contains(resp, "Atlassian に接続") {
		t.Fatal(code, resp)
	}
}

// From a phone: the redirect fails there, the owner pastes its URL.
func TestAtlassianFlowPastedCallback(t *testing.T) {
	o := newOAuthTest(t)
	v := o.start(`{"name":"atlassian-phone","scopes":[]}`)
	state := stateOf(t, v.AuthorizeURL)
	paste := func(u string) (int, string) {
		b, _ := json.Marshal(map[string]string{"url": u})
		return o.do("POST", "/api/oauth/atlassian/flows/"+v.ID+"/callback", string(b))
	}
	for _, bad := range []string{
		"https://example.com/callback?code=good-code&state=" + state,
		"http://127.0.0.1:1/callback?code=good-code&state=" + state,
		strings.Replace(v.RedirectURI, "/callback", "/other", 1) + "?code=good-code&state=" + state,
		v.RedirectURI + "?code=good-code&state=" + strings.Repeat("1", 64),
	} {
		if code, resp := paste(bad); code != 400 {
			t.Errorf("%s: %d %s", bad, code, resp)
		}
	}
	if code, _ := o.do("POST", "/api/oauth/atlassian/flows/"+strings.Repeat("a", 32)+"/callback", `{"url":"x"}`); code != 404 {
		t.Fatal("another flow id", code)
	}
	if code, resp := paste(v.RedirectURI + "?code=good-code&state=" + state); code != 200 {
		t.Fatal(code, resp)
	}
	if code, _ := paste(v.RedirectURI + "?code=good-code&state=" + state); code != 409 {
		t.Fatal("taken twice", code)
	}
	if o.wait(v.ID, "done", "failed").Status != "done" {
		t.Fatal(o.flow(v.ID))
	}
	waitClosed(t, v.RedirectURI)
	// Atlassian's refusal ends a flow without a provider.
	v = o.start(`{"name":"atlassian-denied","scopes":[]}`)
	if code, page := callbackGet(t, v.RedirectURI, "error=access_denied&state="+stateOf(t, v.AuthorizeURL)); code != 200 || !strings.Contains(page, "access_denied") {
		t.Fatal(code, page)
	}
	if f := o.flow(v.ID); f.Status != "failed" {
		t.Fatal(f)
	}
	if _, ok := provider(listProviders(t, o.do), "atlassian-denied"); ok {
		t.Fatal("created")
	}
}

// Nobody authorizes: the listener closes after oauthFlowTTL.
func TestAtlassianFlowTimesOut(t *testing.T) {
	old := oauthFlowTTL
	oauthFlowTTL = 300 * time.Millisecond
	defer func() { oauthFlowTTL = old }()
	o := newOAuthTest(t)
	v := o.start(`{"name":"atlassian-slow","scopes":[]}`)
	f := o.wait(v.ID, "expired")
	if f.AuthorizeURL != "" || !strings.Contains(f.Message, "待ち受けを閉じました") {
		t.Fatalf("%+v", f)
	}
	waitClosed(t, v.RedirectURI)
	// Cancel and a new flow replacing a waiting one.
	oauthFlowTTL = time.Minute
	a := o.start(`{"name":"atlassian-a","scopes":[]}`)
	b := o.start(`{"name":"atlassian-b","scopes":[]}`)
	waitClosed(t, a.RedirectURI)
	if code, _ := o.do("GET", "/api/oauth/atlassian/flows/"+a.ID, ""); code != 404 {
		t.Fatal("the replaced flow is still served")
	}
	if code, resp := o.do("POST", "/api/oauth/atlassian/flows/"+b.ID+"/cancel", `{}`); code != 200 || !strings.Contains(resp, `"status":"canceled"`) {
		t.Fatal(code, resp)
	}
	waitClosed(t, b.RedirectURI)
	if len(o.rt.log()) != 0 {
		t.Fatal(o.rt.log())
	}
}

// 再接続: the grant is replaced on the same provider; running sandboxes
// that have it are marked 再起動待ち (the placeholder changes).
func TestAtlassianReconnect(t *testing.T) {
	o := newOAuthTest(t)
	if code, body := o.do("POST", "/api/sandboxes/dev/start", `{}`); code != 200 {
		t.Fatal(code, body)
	}
	if code, body := o.do("POST", "/api/sandboxes/dev/providers/attach", `{"provider":"atlassian-oss"}`); code != 200 {
		t.Fatal(code, body)
	}
	if code, body := o.do("POST", "/api/sandboxes/dev/providers/attach", `{"provider":"atlassian-work"}`); code != 409 || !strings.Contains(body, "Atlassian のプロバイダーは 1 つのサンドボックスに 1 つまで") {
		t.Fatal("two Atlassian providers on a sandbox:", code, body)
	}
	o.s.updateSandboxes(func(l []Sandbox) ([]Sandbox, error) { l[0].ProvidersChangedAt = time.Time{}; return l, nil })
	p, _ := provider(listProviders(t, o.do), "atlassian-oss")
	if p.Refresh == nil || p.Refresh.Status != "reauthorization_required" || p.Refresh.Recovery != "reauthorize" {
		t.Fatalf("%+v", p.Refresh)
	}
	v := o.start(`{"name":"atlassian-oss","scopes":[],"reconnect":true}`)
	callbackGet(t, v.RedirectURI, "code=good-code&state="+stateOf(t, v.AuthorizeURL))
	done := o.wait(v.ID, "done", "failed")
	if done.Status != "done" || !strings.Contains(done.Message, "再起動してください") {
		t.Fatalf("%+v", done)
	}
	if want := []string{"delete-refresh atlassian-oss", "update atlassian-oss ATLASSIAN_ACCESS_TOKEN", "configure atlassian-oss ATLASSIAN_ACCESS_TOKEN", "rotate atlassian-oss"}; !slices.Equal(o.rt.log(), want) {
		t.Fatal(o.rt.log())
	}
	if sb, _ := o.s.lookupSandbox("dev"); sb.ProvidersChangedAt.IsZero() {
		t.Fatal("not marked 再起動待ち")
	}
	if p, _ := provider(listProviders(t, o.do), "atlassian-oss"); p.Refresh.Status != "refreshed" {
		t.Fatalf("%+v", p.Refresh)
	}
	// 今すぐ更新: the gateway rotates now; only for refreshed providers.
	if code, body := o.do("POST", "/api/providers/atlassian-work/rotate", `{}`); code != 200 || !strings.Contains(body, `"status":"refreshed"`) {
		t.Fatal(code, body)
	}
	if code, body := o.do("POST", "/api/providers/github-work/rotate", `{}`); code != 409 {
		t.Fatal("rotated a static provider:", code, body)
	}
	o.rt.rotateErr = errors.New("rejected")
	if code, body := o.do("POST", "/api/providers/atlassian-work/rotate", `{}`); code != 409 || !strings.Contains(body, "configuration_required oauth_invalid_target") {
		t.Fatal(code, body)
	}
}

// Atlassian refuses the gateway's refresh (it cannot send resource): the
// provider stays, with a warning and the gateway's state.
func TestAtlassianFirstRefreshFails(t *testing.T) {
	o := newOAuthTest(t)
	o.rt.rotateErr = errors.New("token endpoint rejected the refresh")
	v := o.start(`{"name":"atlassian-x","scopes":[]}`)
	callbackGet(t, v.RedirectURI, "code=good-code&state="+stateOf(t, v.AuthorizeURL))
	done := o.wait(v.ID, "done", "failed")
	if done.Status != "done" || !strings.Contains(done.Warning, "最初の更新が失敗") || !strings.Contains(done.Warning, "oauth_invalid_target") || done.Refresh.Recovery != "fix_configuration" {
		t.Fatalf("%+v", done)
	}
	// No refresh token: no provider.
	o.fake.noRefresh = true
	v = o.start(`{"name":"atlassian-y","scopes":[]}`)
	callbackGet(t, v.RedirectURI, "code=good-code&state="+stateOf(t, v.AuthorizeURL))
	if done := o.wait(v.ID, "done", "failed"); done.Status != "failed" || !strings.Contains(done.Message, "offline_access") {
		t.Fatalf("%+v", done)
	}
	if _, ok := provider(listProviders(t, o.do), "atlassian-y"); ok {
		t.Fatal("created without a refresh token")
	}
}

type oauthTest struct {
	*oauthFlowTest
	rt   *spyRuntime
	fake *fakeAtlassian
	logs *bytes.Buffer
}
