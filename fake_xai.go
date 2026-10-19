//go:build uitest

package main

import (
	"context"
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

// The fake's xAI: the device code flow without a network, and a
// verification page Iwato serves itself (/fake/xai/device) where the
// owner approves or denies the code, as at auth.x.ai.

// fakeDevices are the fake's device codes: user code → pending, approved
// or denied.
type fakeDevices struct {
	mu    sync.Mutex
	codes map[string]string
}

func (s *Server) fakeDevices() *fakeDevices {
	s.xai.mu.Lock()
	defer s.xai.mu.Unlock()
	if s.xai.fake == nil {
		s.xai.fake = &fakeDevices{codes: map[string]string{}}
	}
	return s.xai.fake.(*fakeDevices)
}

type fakeXAIBackend struct {
	d      *fakeDevices
	origin string
}

func (fakeXAIBackend) ClientID() string { return xaiClientID }

func (x fakeXAIBackend) DeviceCode(context.Context) (deviceCode, error) {
	code := strings.ToUpper(randomHex(2) + "-" + randomHex(2))
	x.d.mu.Lock()
	x.d.codes[code] = "pending"
	x.d.mu.Unlock()
	return deviceCode{DeviceCode: "fake-device-" + code, UserCode: code, VerificationURI: x.origin + "/fake/xai/device",
		CompleteURI: x.origin + "/fake/xai/device?user_code=" + url.QueryEscape(code), ExpiresIn: 600, Interval: 1}, nil
}

func (x fakeXAIBackend) Poll(_ context.Context, device string) (oauthTokens, error) {
	code, _ := strings.CutPrefix(device, "fake-device-")
	x.d.mu.Lock()
	st := x.d.codes[code]
	x.d.mu.Unlock()
	switch st {
	case "approved":
		return oauthTokens{AccessToken: "fake-access-" + randomHex(8), RefreshToken: "fake-refresh-" + randomHex(8), ExpiresIn: 900, Scope: xaiScopes,
			Fields: []string{"access_token", "expires_in", "id_token", "refresh_token", "scope", "token_type"}, Lifetimes: map[string]int64{"expires_in": 900}}, nil
	case "denied":
		return oauthTokens{}, errors.New("token request: HTTP 400: access_denied")
	case "pending":
		return oauthTokens{}, errAuthorizationPending
	}
	return oauthTokens{}, errors.New("token request: HTTP 400: invalid_grant")
}

var fakeDevicePage = template.Must(template.New("").Parse(`<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>xAI (テスト用の偽ページ)</title>
<body style="font:16px system-ui,sans-serif;margin:2em;max-width:36em">
<h1 style="font-size:1.3em">xAI (テスト用の偽ページ)</h1>
{{if .Done}}<p id="result">{{.Done}}</p>
{{else if .Code}}<p>Grok Build がアカウントへのアクセスを求めています。コード:</p>
<p><code id="code" style="font-size:1.4em">{{.Code}}</code></p>
<p><a id="approve" href="{{.Approve}}" style="display:inline-block;padding:.5em 1em;background:#111;color:#fff;border-radius:4px;text-decoration:none">許可する</a>
<a id="deny" href="{{.Deny}}" style="margin-left:1em">拒否する</a></p>
{{else}}<form><label>コード <input name="user_code" autocapitalize="characters"></label> <button>次へ</button></form>{{end}}
</body>`))

// handleFakeDevice is the fake's verification page (serve -fake only).
func (s *Server) handleFakeDevice(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	d := s.fakeDevices()
	q := r.URL.Query()
	code := strings.ToUpper(strings.TrimSpace(q.Get("user_code")))
	data := map[string]any{}
	if code != "" {
		d.mu.Lock()
		st, ok := d.codes[code]
		if ok && st == "pending" {
			switch q.Get("decision") {
			case "approve":
				d.codes[code] = "approved"
				data["Done"] = "許可しました。Iwato の画面に戻ってください。"
			case "deny":
				d.codes[code] = "denied"
				data["Done"] = "拒否しました。"
			default:
				link := func(v string) template.URL {
					return template.URL("?" + url.Values{"user_code": {code}, "decision": {v}}.Encode())
				}
				data["Code"], data["Approve"], data["Deny"] = code, link("approve"), link("deny")
			}
		} else {
			data["Done"] = "このコードは使えません。"
		}
		d.mu.Unlock()
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = fakeDevicePage.Execute(w, data)
}
