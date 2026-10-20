//go:build uitest

package main

import (
	"context"
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
}

// newFakeUpdate is the fake's update: an edge build whose next commit
// changes the gateway. Applying it takes a few seconds and then reports
// the new build as the running one, as a restarted server would.
func newFakeUpdate() *selfUpdate {
	next := buildMeta{Version: version, Commit: strings.Repeat("2", 40), Built: time.Now().UTC().Add(-time.Hour).Format(time.RFC3339), Channel: "edge",
		Images: map[string]string{"iwato-gateway": "sha256:" + strings.Repeat("2", 64), "iwato-sandbox": "sha256:" + strings.Repeat("3", 64)},
		Assets: map[string]string{}}
	for _, a := range updateAssets {
		next.Assets[a] = strings.Repeat("a", 64)
	}
	u := &selfUpdate{channel: "edge", current: BuildInfo{Version: version, Commit: "1111111", Built: time.Now().UTC().Add(-50 * time.Hour).Format(time.RFC3339)}}
	u.fetch = func(context.Context) (buildMeta, error) { return next, nil }
	u.install = func(ctx context.Context, m buildMeta) (string, error) {
		select {
		case <-time.After(1500 * time.Millisecond):
		case <-ctx.Done():
			return "", ctx.Err()
		}
		return "", nil
	}
	u.restart = func() {
		time.AfterFunc(time.Second, func() {
			u.mu.Lock()
			defer u.mu.Unlock()
			u.current = BuildInfo{Version: next.Version, Commit: next.Commit[:7], Built: next.Built}
			u.latest, u.phase, u.swapped = nil, "", ""
		})
	}
	return u
}

// GatewayImage: the fake's gateway runs another digest than the update's.
func (d *Fake) GatewayImage(context.Context) string {
	return "ghcr.io/mtsmfm/iwato-gateway:edge@sha256:" + strings.Repeat("1", 64)
}
