package main

import (
	"context"
	"path/filepath"
	"sync"
	"time"

	"iwato/internal/openshell/wslc"
)

// runtimeImages are the supervisor and sandbox runtime images: refs as
// the WSLc driver is configured with, or image IDs.
type runtimeImages struct{ Supervisor, Sandbox string }

// runtimeChanged reports whether a sandbox's runtime images (created)
// differ from the driver's current ones. An unknown ID ("") on either
// side is no evidence.
func runtimeChanged(created, current runtimeImages) bool {
	differs := func(a, b string) bool { return a != "" && b != "" && a != b }
	return differs(created.Supervisor, current.Supervisor) || differs(created.Sandbox, current.Sandbox)
}

// runtimeCheck tells which WSLc sandboxes run older runtime images than
// the driver's: the driver keeps each sandbox's until it is recreated
// (docs/dev/openshell.md). Upstream's Docker driver starts the current
// supervisor and openshell-sandbox on every start, so it needs no check.
type runtimeCheck struct {
	mu   sync.Mutex
	refs runtimeImages
	ids  map[string]cachedImageID
}

type cachedImageID struct {
	id string
	at time.Time
}

func (c *runtimeCheck) setRefs(refs runtimeImages) {
	c.mu.Lock()
	c.refs = refs
	c.mu.Unlock()
}

// imageID is ref's local image ID, "" when it is not present or cannot be
// read; looked up again after a while (a pull, or a moved tag).
func (c *runtimeCheck) imageID(ctx context.Context, r wslc.Runner, ref string) string {
	if ref == "" {
		return ""
	}
	c.mu.Lock()
	e, ok := c.ids[ref]
	c.mu.Unlock()
	keep := time.Minute
	if e.id != "" {
		keep = 10 * time.Minute
	}
	if ok && time.Since(e.at) < keep {
		return e.id
	}
	id, err := wslc.ImageID(ctx, r, ref)
	if err != nil && ctx.Err() != nil {
		return e.id
	}
	c.mu.Lock()
	if c.ids == nil {
		c.ids = map[string]cachedImageID{}
	}
	c.ids[ref] = cachedImageID{id, time.Now()}
	c.mu.Unlock()
	return id
}

// recreateToUpdate reports whether the sandbox (OpenShell's id) was
// created with other runtime images than the WSLc driver now uses.
func (o *OpenShell) recreateToUpdate(ctx context.Context, id string) bool {
	if o.e.docker || id == "" {
		return false
	}
	var created runtimeImages
	created.Supervisor, created.Sandbox = wslc.CreatedImages(filepath.Join(o.dir, "sandboxes"), o.e.config.Namespace, id)
	if created == (runtimeImages{}) {
		return false
	}
	o.runtime.mu.Lock()
	refs := o.runtime.refs
	o.runtime.mu.Unlock()
	r := o.gatewayCLI()
	var current runtimeImages
	if created.Supervisor != "" {
		current.Supervisor = o.runtime.imageID(ctx, r, refs.Supervisor)
	}
	if created.Sandbox != "" {
		current.Sandbox = o.runtime.imageID(ctx, r, refs.Sandbox)
	}
	return runtimeChanged(created, current)
}

// PinsRuntime: sandboxes keep the supervisor they were created with
// (update.go).
func (o *OpenShell) PinsRuntime() bool { return !o.e.docker }
