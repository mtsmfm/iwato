package main

import (
	"context"
	"sync"

	v1 "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1"
)

// gatewayReads keeps what one policy change has read from the gateway
// (the latest revision, the effective policy, the providers), so that its
// checks and the change itself read each once. A write through applyOps
// or ReplacePolicyRules forgets the sandbox's policy.
type gatewayReads struct {
	mu     sync.Mutex
	status map[string]*v1.PolicyStatusResult
	config map[string]*v1.SandboxConfig
	// The providers and where they are attached (providerState).
	provRead bool
	provList []ProviderInfo
	attached map[string][]string
}

type gatewayReadsKey struct{}

// withGatewayReads starts keeping the reads made with the context.
func withGatewayReads(ctx context.Context) context.Context {
	return context.WithValue(ctx, gatewayReadsKey{}, &gatewayReads{status: map[string]*v1.PolicyStatusResult{}, config: map[string]*v1.SandboxConfig{}})
}

func readsOf(ctx context.Context) *gatewayReads {
	r, _ := ctx.Value(gatewayReadsKey{}).(*gatewayReads)
	return r
}

// readOnce answers from the context's reads, or reads and keeps the answer.
func readOnce[T any](ctx context.Context, pick func(*gatewayReads) map[string]T, name string, read func() (T, error)) (T, error) {
	r := readsOf(ctx)
	if r == nil {
		return read()
	}
	r.mu.Lock()
	v, ok := pick(r)[name]
	r.mu.Unlock()
	if ok {
		return v, nil
	}
	v, err := read()
	if err == nil {
		r.mu.Lock()
		pick(r)[name] = v
		r.mu.Unlock()
	}
	return v, err
}

// forgetPolicy drops the sandbox's policy from the context's reads.
func forgetPolicy(ctx context.Context, name string) {
	if r := readsOf(ctx); r != nil {
		r.mu.Lock()
		delete(r.status, name)
		delete(r.config, name)
		r.mu.Unlock()
	}
}
