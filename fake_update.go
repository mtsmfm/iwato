//go:build uitest

package main

import (
	"context"
	"errors"
	"os"
	"strings"
	"time"
)

// IWATO_UPDATE_BASE_URL points the update check at a test server (in
// uitest builds only).
func init() {
	if v := os.Getenv("IWATO_UPDATE_BASE_URL"); v != "" {
		updateBaseOverride = v
	}
	if failStartBuild != "" {
		failStart = errors.New("this build fails to start (failStartBuild)")
	}
}

// failStartBuild (-ldflags -X) makes serve fail before listening.
var failStartBuild string

// newFakeUpdate is the fake's update: an edge build whose next commit
// changes the gateway, and two pull requests' builds (pr-12 keeps the
// gateway). Applying one takes a few seconds and then reports it as the
// running build, as a restarted server would.
func newFakeUpdate() *selfUpdate {
	built := func(h time.Duration) string { return time.Now().UTC().Add(-h).Format(time.RFC3339) }
	meta := func(channel, commit, gateway string, pr int, title, when string) buildMeta {
		m := buildMeta{Version: version, Commit: strings.Repeat(commit, 40), Built: when, Channel: channel, PR: pr, Title: title,
			Images: map[string]string{"iwato-gateway": "sha256:" + strings.Repeat(gateway, 64), "iwato-sandbox": "sha256:" + strings.Repeat("3", 64)},
			Assets: map[string]string{}}
		for _, a := range updateAssets {
			m.Assets[a] = strings.Repeat("a", 64)
		}
		return m
	}
	releases := map[string]buildMeta{
		"edge":  meta("edge", "2", "2", 0, "", built(time.Hour)),
		"pr-12": meta("pr", "4", "1", 12, "Terminal: faster scrolling", built(2*time.Hour)),
		"pr-9":  meta("pr", "5", "2", 9, "Gateway: a newer OpenShell", built(30*time.Hour)),
	}
	u := &selfUpdate{channel: "edge", current: BuildInfo{Version: version, Commit: "1111111", Built: built(50 * time.Hour)}}
	u.fetch = func(_ context.Context, tag string) (buildMeta, error) {
		m, ok := releases[tag]
		if !ok {
			return buildMeta{}, errReleaseGone
		}
		return m, nil
	}
	u.list = func(context.Context) ([]buildMeta, error) {
		return []buildMeta{releases["pr-12"], releases["pr-9"]}, nil
	}
	var installed buildMeta
	u.install = func(ctx context.Context, _ string, m buildMeta) (string, error) {
		select {
		case <-time.After(1500 * time.Millisecond):
		case <-ctx.Done():
			return "", ctx.Err()
		}
		installed = m
		return "", nil
	}
	u.restart = func() {
		time.AfterFunc(time.Second, func() {
			u.mu.Lock()
			u.current = BuildInfo{Version: installed.Version, Commit: installed.Commit[:7], Built: installed.Built, PR: installed.PR}
			u.channel, u.pr = installed.Channel, installed.PR
			u.latest, u.phase, u.swapped = nil, "", ""
			u.mu.Unlock()
			_ = u.check(context.Background())
		})
	}
	return u
}

// GatewayImage: the fake's gateway runs another digest than the update's.
func (d *Fake) GatewayImage(context.Context) string {
	return "ghcr.io/mtsmfm/iwato-gateway:edge@sha256:" + strings.Repeat("1", 64)
}
