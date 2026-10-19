//go:build uitest

package main

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"
)

// The OAuth flows a reset cancels: the authorization that comes back
// afterwards is refused, and one already being exchanged creates no
// provider in the fresh gateway.
func TestResetCancelsOAuthFlows(t *testing.T) {
	o := newOAuthTest(t)
	v := o.start(`{"name":"atlassian-late","scopes":[]}`)
	o.s.oauth.mu.Lock()
	f := o.s.oauth.current
	o.s.oauth.mu.Unlock()
	o.s.cancelOAuthFlows(true)
	if code, _ := o.s.oauthAccept(f, url.Values{"state": {stateOf(t, v.AuthorizeURL)}, "code": {"good-code"}}); code != 409 {
		t.Fatal("the redirect after the reset taken:", code)
	}
	b, _ := json.Marshal(map[string]string{"url": v.RedirectURI + "?code=good-code&state=" + stateOf(t, v.AuthorizeURL)})
	if code, _ := o.do("POST", "/api/oauth/atlassian/flows/"+v.ID+"/callback", string(b)); code != 404 {
		t.Fatal("the pasted redirect after the reset taken:", code)
	}
	waitClosed(t, v.RedirectURI)
	// The exchange that had begun before.
	o.s.finishOAuth(f, "good-code")
	for _, call := range o.rt.log() {
		if strings.HasPrefix(call, "create ") || strings.HasPrefix(call, "configure ") {
			t.Fatal("handed to the gateway after the reset:", o.rt.log())
		}
	}
	// A flow started after the reset works again.
	if o.s.oauthLife().Err() != nil {
		t.Fatal("new flows start canceled")
	}
}
