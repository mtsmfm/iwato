//go:build !uitest

package main

import (
	"strings"
	"testing"
)

// Without the build tag uitest there is no fake backend: serve has no
// -fake flag and a server is never fake, so no fake page is served.
func TestNoFakeWithoutTheTag(t *testing.T) {
	if e := run([]string{"serve", "-fake"}); e == nil || !strings.Contains(e.Error(), "flag provided but not defined: -fake") {
		t.Fatal(e)
	}
	s := newServer(testConfig(), &reviewRuntime{}, true, "127.0.0.1:29999")
	if s.fake {
		t.Fatal("a fake server without the tag")
	}
	if w := reviewRequest(s, "GET", "/fake/claude/code?code=c&state=s", "", "", "", s.host, nil, ""); w.Code != 404 {
		t.Fatal("the fake page without the tag", w.Code)
	}
}
