//go:build uitest

package main

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// staleList lists no providers, as if another client created them since:
// Iwato learns of them from Create.
type staleList struct {
	*Fake
	creates []string
	fail    error
}

func TestCreateProviderNamed(t *testing.T) {
	ctx := context.Background()
	d := NewFake()
	d.seedProviders()
	// A given name is used as is, and a taken one fails.
	if n, err := createProviderNamed(ctx, d, "gh", "iwato-github", nil); n != "gh" || err != nil {
		t.Fatal(n, err)
	}
	if _, err := createProviderNamed(ctx, d, "gh", "iwato-github", nil); !isAlreadyExists(err) {
		t.Fatal(err)
	}
	// None given: the profile's, skipping the names listed.
	for _, want := range []string{"github", "github-2"} {
		if n, err := createProviderNamed(ctx, d, "", "iwato-github", nil); n != want || err != nil {
			t.Fatal(n, err, want)
		}
	}
	if n, err := createProviderNamed(ctx, d, "", "iwato-claude", nil); n != "claude-2" || err != nil {
		t.Fatal(n, err)
	}
	// Taken meanwhile: the gateway says so and the next is tried.
	r := &staleList{Fake: d}
	if n, err := createProviderNamed(ctx, r, "", "iwato-github", nil); n != "github-3" || err != nil || !slices.Equal(r.creates, []string{"github", "github-2", "github-3"}) {
		t.Fatal(n, err, r.creates)
	}
	// Another error stops at once.
	r = &staleList{Fake: d, fail: errors.New("gateway down")}
	if _, err := createProviderNamed(ctx, r, "", "iwato-github", nil); err == nil || err.Error() != "gateway down" || len(r.creates) != 1 {
		t.Fatal(err, r.creates)
	}
	// All taken: a bounded number of tries.
	r = &staleList{Fake: d, fail: status.Error(codes.AlreadyExists, "provider already exists")}
	if _, err := createProviderNamed(ctx, r, "", "iwato-github", nil); err == nil || !strings.Contains(err.Error(), "名前を入力") || len(r.creates) != providerNameRetries {
		t.Fatal(err, len(r.creates))
	}
}

func (r *staleList) ListProviders(context.Context) ([]ProviderInfo, map[string][]string, error) {
	return nil, nil, nil
}

func (r *staleList) CreateProvider(ctx context.Context, name, profile string, creds map[string]string) error {
	r.creates = append(r.creates, name)
	if r.fail != nil {
		return r.fail
	}
	return r.Fake.CreateProvider(ctx, name, profile, creds)
}

// The API creates a provider without a name and answers with the name.
func TestProviderAPICreatesWithoutAName(t *testing.T) {
	_, _, do, _ := providerServerCookie(t)
	l := listProviders(t, do)
	if l.Profiles[5].DefaultName != "github" || l.Profiles[4].DefaultName != "atlassian" || l.Profiles[8].DefaultName != "comfy-cloud" || l.Profiles[1].DefaultName != "claude-subscription" || l.Profiles[2].DefaultName != "grok-subscription" {
		t.Fatalf("%+v", l.Profiles)
	}
	for _, want := range []string{"github", "github-2"} {
		code, body := do("POST", "/api/providers", `{"name":"  ","profile":"iwato-github","values":{"token":"x"}}`)
		var resp struct {
			Name string `json:"name"`
		}
		if code != 200 || json.Unmarshal([]byte(body), &resp) != nil || resp.Name != want {
			t.Fatal(code, body, want)
		}
		if p, ok := provider(listProviders(t, do), want); !ok || p.Profile != scopedProfileID(githubProfileID, want) {
			t.Fatalf("%+v", p)
		}
	}
	if code, body := do("POST", "/api/providers", `{"name":"x y","profile":"iwato-github","values":{"token":"x"}}`); code != 400 || !strings.Contains(body, "名前は") {
		t.Fatal(code, body)
	}
}

// The Atlassian flow without a name creates atlassian, then atlassian-2;
// the flow tells the page the name. Reconnecting still needs one.
func TestAtlassianFlowNamesTheProvider(t *testing.T) {
	o := newOAuthTest(t)
	if code, resp := o.do("POST", "/api/oauth/atlassian/start", `{"name":"","scopes":[],"reconnect":true}`); code != 400 || !strings.Contains(resp, "名前は") {
		t.Fatal(code, resp)
	}
	for _, want := range []string{"atlassian", "atlassian-2"} {
		v := o.start(`{"name":"","scopes":["read:me"]}`)
		if v.Name != "" {
			t.Fatalf("named before creation: %+v", v)
		}
		if code, _ := callbackGet(t, v.RedirectURI, "code=good-code&state="+stateOf(t, v.AuthorizeURL)); code != 200 {
			t.Fatal(code)
		}
		done := o.wait(v.ID, "done", "failed")
		if done.Status != "done" || done.Name != want || !strings.HasPrefix(done.Message, want+" として接続しました。") {
			t.Fatalf("%+v", done)
		}
		if p, ok := provider(listProviders(t, o.do), want); !ok || p.Profile != scopedProfileID(atlassianProfileID, want) || p.Base != atlassianProfileID || p.Refresh == nil {
			t.Fatalf("%+v", p)
		}
	}
	if log := o.rt.log(); !slices.Contains(log, "configure atlassian-2 ATLASSIAN_ACCESS_TOKEN") || !slices.Contains(log, "rotate atlassian-2") {
		t.Fatal(log)
	}
	if !strings.Contains(o.logs.String(), "atlassian oauth: (new): waiting") {
		t.Fatal(o.logs.String())
	}
}
