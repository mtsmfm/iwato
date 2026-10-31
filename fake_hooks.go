package main

import (
	"flag"
	"net/http"
)

// fakeHooks connects the fake backend (fake*.go: the UI backend the
// browser checks drive, which runs no command and no sandbox) to the rest.
// The fake is built only with the build tag uitest, whose init sets it.
// Without the tag it is nil: serve has no -fake flag, and newServer never
// marks a server fake, so no fake route or backend is used.
var fakeHooks *fakeBackend

type fakeBackend struct {
	// flags adds -fake and -fake-engine-down to serve's flags. After
	// Parse, the returned func gives the fake's run, or nil without -fake.
	flags func(*flag.FlagSet) func() (*fakeRun, error)
	// routes adds the fake's stand-ins for Atlassian's, xAI's and Claude's
	// pages (/fake/...).
	routes func(*Server, *http.ServeMux)
	// The sign-in backends standing in for the real ones; origin is the
	// page's.
	oauth  func(s *Server, origin string) oauthBackend
	claude func(s *Server, origin string) claudeBackend
	xai    func(s *Server, origin string) deviceBackend
	codex  func(s *Server, origin string) deviceBackend
}

// fakeRun is serve -fake: its configuration and runtime, and setup, which
// gives the server the fake's checks, ingress, presets and images.
type fakeRun struct {
	config Config
	rt     Runtime
	setup  func(*Server)
}
