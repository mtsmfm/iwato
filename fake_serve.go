//go:build uitest

package main

import (
	"errors"
	"flag"
	"net/http"
)

func init() {
	fakeHooks = &fakeBackend{
		flags: fakeFlags,
		routes: func(s *Server, mux *http.ServeMux) {
			mux.HandleFunc("/fake/atlassian/authorize", s.handleFakeAuthorize)
			mux.HandleFunc("/fake/xai/device", s.handleFakeDevice)
			mux.HandleFunc("/fake/claude/authorize", s.handleFakeClaude)
			mux.HandleFunc("/fake/claude/code", s.handleFakeClaude)
		},
		oauth:  func(_ *Server, origin string) oauthBackend { return fakeOAuth{origin: origin} },
		claude: func(_ *Server, origin string) claudeBackend { return fakeClaudeOAuth{origin: origin} },
		xai: func(s *Server, origin string) deviceBackend {
			return fakeXAIBackend{d: s.fakeDevices(), origin: origin}
		},
	}
}

func fakeFlags(f *flag.FlagSet) func() (*fakeRun, error) {
	fake := f.Bool("fake", false, "fake UI backend for the browser checks: does not run commands or sandboxes")
	engineDown := f.Bool("fake-engine-down", false, "with -fake: the engine check fails until the page checks again")
	latency := f.Duration("fake-latency", 0, "with -fake: added to the terminals' round trip")
	return func() (*fakeRun, error) {
		if *engineDown && !*fake {
			return nil, errors.New("-fake-engine-down needs -fake")
		}
		if !*fake {
			return nil, nil
		}
		c := defaultConfig()
		dev := newSandbox("dev")
		dev.LegacyPersist = []string{"/sandbox/work"} // made before the image convention
		dev.Providers = []string{"github-personal"}   // fake_providers.go
		// With a GitHub provider of its own profile: proposals to GitHub
		// widen its scope (proposal_scope.go).
		exp := newSandbox("experiment")
		exp.Providers = []string{"github-work"}
		exp.GPU = true // the resources show its GPU (fake_resources.go)
		c.Sandboxes = []Sandbox{dev, exp}
		c.ProviderScopes = fakeProviderScopes()
		d := NewFake()
		d.latency = *latency
		d.oldRuntime = map[string]bool{"dev": true}
		d.seedVolumes(c)
		d.seedProviders()
		return &fakeRun{config: c, rt: d, setup: func(s *Server) {
			// With -fake-engine-down the engine check fails until the
			// page checks again (fake_checks.go).
			s.checks = newFakeChecks(*engineDown)
			s.ingress.newProvider = newFakeIngressProvider
			seedFakePresets()
			s.imageUpdates = &imageUpdates{src: newFakeImages()}
			s.update = newFakeUpdate()
			go s.update.loop(s.ctx)
		}}, nil
	}
}
