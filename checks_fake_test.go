//go:build uitest

package main

import (
	"context"
	"strings"
	"testing"
)

// Without a usable engine serve waits: operations that need it are
// refused with engine_unavailable, the list says why, and once the checks
// pass the preparation goes on.
func TestEngineWait(t *testing.T) {
	up := false
	s, _ := newTestServer(t)
	s.checks = newCheckState(func(context.Context) []check {
		if up {
			return []check{{Level: "ok", Name: "docker", Detail: "docker_version"}}
		}
		return []check{{Level: "fail", Name: "docker", Detail: "docker_down", Hint: "docker_start"}}
	})
	s.checks.refresh(context.Background())
	started := make(chan struct{})
	go s.checks.whenReady(context.Background(), func() { close(started) })
	cookie, csrf := login(t, s)
	do := func(method, path string) (int, string) {
		res, b := request(t, s, path, method, "{}", cookie, csrf)
		return res.StatusCode, string(b)
	}
	if code, body := do("POST", "/api/sandboxes/dev/start"); code != 409 || !strings.Contains(body, `"code":"engine_unavailable"`) || !strings.Contains(body, `"engine":"Docker"`) {
		t.Fatal(code, body)
	}
	if code, body := do("GET", "/api/sandboxes"); code != 200 || !strings.Contains(body, `"engine":{"level":"fail","name":"docker"`) || !strings.Contains(body, `"phase":"engine"`) {
		t.Fatal(code, body)
	}
	select {
	case <-started:
		t.Fatal("started without an engine")
	default:
	}
	up = true
	if code, body := do("GET", "/api/checks"); code != 200 || !strings.Contains(body, `"level":"ok"`) {
		t.Fatal(code, body)
	}
	<-started
	if code, body := do("GET", "/api/sandboxes"); code != 200 || strings.Contains(body, `"engine"`) {
		t.Fatal(code, body)
	}
}
