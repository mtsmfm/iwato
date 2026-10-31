package main

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeXAI is auth.x.ai as Grok Build uses it: the device authorization
// (pending a few polls, slow_down once) and the token endpoint, which
// also takes the gateway's refresh, rotating the refresh token on every
// use and accepting only the current one.
type fakeXAI struct {
	mu sync.Mutex
	*httptest.Server
	// polls before the owner approves; principal is the access token's
	// principal_type claim ("" for an opaque token); deny refuses.
	pending   int
	principal string
	deny      bool
	polls     int
	current   string
	n         int
	forms     []url.Values
}

func newFakeXAI(t *testing.T) *fakeXAI {
	f := &fakeXAI{pending: 2}
	f.Server = httptest.NewServer(f)
	t.Cleanup(f.Close)
	return f
}

func (f *fakeXAI) oauth() *xaiOAuth {
	x := newXAIOAuth()
	x.DeviceURL, x.TokenURL = f.URL+"/oauth2/device/code", f.URL+"/oauth2/token"
	return x
}

func (f *fakeXAI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" || r.ParseForm() != nil {
		http.Error(w, "bad", 400)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	form := r.PostForm
	f.forms = append(f.forms, form)
	if form.Get("client_id") != xaiClientID {
		jsonResponse(w, 401, map[string]string{"error": "invalid_client"})
		return
	}
	switch {
	case r.URL.Path == "/oauth2/device/code":
		if form.Get("scope") != xaiScopes || form.Get("referrer") != "grok-build" {
			jsonResponse(w, 400, map[string]string{"error": "invalid_scope"})
			return
		}
		f.polls = 0
		jsonResponse(w, 200, map[string]any{"device_code": "dc-SECRET", "user_code": "ABCD-1234", "verification_uri": "https://accounts.x.example/device",
			"verification_uri_complete": "https://accounts.x.example/device?user_code=ABCD-1234", "expires_in": 600, "interval": 1})
	case r.URL.Path == "/oauth2/token" && form.Get("grant_type") == "urn:ietf:params:oauth:grant-type:device_code":
		if form.Get("device_code") != "dc-SECRET" {
			jsonResponse(w, 400, map[string]string{"error": "invalid_grant"})
			return
		}
		f.polls++
		switch {
		case f.polls == 1:
			jsonResponse(w, 400, map[string]string{"error": "slow_down"})
		case f.polls <= f.pending:
			jsonResponse(w, 400, map[string]string{"error": "authorization_pending"})
		case f.deny:
			jsonResponse(w, 400, map[string]string{"error": "access_denied"})
		default:
			f.current = "rt-SECRET-1"
			access := "at-SECRET-device"
			if f.principal != "" {
				claims, _ := json.Marshal(map[string]string{"principal_type": f.principal, "principal_id": "p-1"})
				access = "e30." + base64.RawURLEncoding.EncodeToString(claims) + ".SECRET"
			}
			jsonResponse(w, 200, map[string]any{"access_token": access, "token_type": "Bearer", "expires_in": 900, "refresh_token": f.current, "id_token": "e30.e30.x", "scope": xaiScopes})
		}
	case r.URL.Path == "/oauth2/token" && form.Get("grant_type") == "refresh_token":
		if form.Get("refresh_token") != f.current {
			jsonResponse(w, 400, map[string]string{"error": "invalid_grant"})
			return
		}
		f.n++
		f.current = "rt-SECRET-rotated-" + string(rune('0'+f.n))
		jsonResponse(w, 200, map[string]any{"access_token": "at-SECRET-minted", "token_type": "Bearer", "expires_in": 900, "refresh_token": f.current})
	default:
		jsonResponse(w, 400, map[string]string{"error": "unsupported_grant_type"})
	}
}

type xaiTest struct {
	t  *testing.T
	s  *Server
	do func(method, path, body string) (int, string)
	// route is the service's (xai when empty).
	route string
}

func (x *xaiTest) base() string {
	if x.route == "" {
		return "/api/oauth/xai"
	}
	return "/api/oauth/" + x.route
}

func (x *xaiTest) start(body string) deviceFlowView {
	x.t.Helper()
	code, resp := x.do("POST", x.base()+"/start", body)
	var v deviceFlowView
	if code != 200 || json.Unmarshal([]byte(resp), &v) != nil {
		x.t.Fatal(code, resp)
	}
	return v
}

// wait polls the flow as the page does; no value may reach the page.
func (x *xaiTest) wait(id string, statuses ...string) deviceFlowView {
	x.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		code, resp := x.do("GET", x.base()+"/flows/"+id, "")
		var v deviceFlowView
		if code != 200 || json.Unmarshal([]byte(resp), &v) != nil {
			x.t.Fatal(code, resp)
		}
		if strings.Contains(resp, "SECRET") {
			x.t.Fatal("a value reached the page:", resp)
		}
		if slices.Contains(statuses, v.Status) {
			return v
		}
		if time.Now().After(deadline) {
			x.t.Fatalf("flow stays %s: %+v", v.Status, v)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func shortXAIPolls(t *testing.T) {
	old := devicePollUnit
	devicePollUnit = 5 * time.Millisecond
	t.Cleanup(func() { devicePollUnit = old })
}
