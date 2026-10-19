//go:build uitest

package main

import (
	"context"
	"sync"
	"time"
)

// fakeImages is the fake's standard image: an edge image made a few days
// ago, which the first update replaces (the next ones find it current).
type fakeImages struct {
	mu      sync.Mutex
	created time.Time
	at      time.Time
	pulls   int
}

func newFakeImages() *fakeImages {
	now := time.Now().UTC()
	return &fakeImages{created: now.Add(-75 * time.Hour), at: now.Add(-50 * time.Hour)}
}

func (d *fakeImages) ImageInfo(context.Context) ImageInfo {
	d.mu.Lock()
	defer d.mu.Unlock()
	return ImageInfo{Available: true, Image: "ghcr.io/mtsmfm/iwato-sandbox:edge", Source: "pull", Moves: true, Present: true,
		Created: d.created.Format(time.RFC3339Nano), At: d.at.Format(time.RFC3339)}
}

func (d *fakeImages) UpdateImage(ctx context.Context) (bool, error) {
	select {
	case <-time.After(1500 * time.Millisecond):
	case <-ctx.Done():
		return false, ctx.Err()
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.pulls++
	d.at = time.Now().UTC()
	if d.pulls > 1 {
		return false, nil
	}
	d.created = d.at.Add(-2 * time.Hour)
	return true, nil
}
