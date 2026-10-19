package ingress

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
)

// ProviderAuth lets in the owner's devices as the provider identifies them
// (auth "provider", the v1 default): a login in Owners on an untagged node
// of the tailnet itself. Tagged nodes (the sandboxes' own and other
// servers) and nodes shared in from other tailnets are refused.
type ProviderAuth struct {
	// Owners returns the allowed logins (read on every connection, so a
	// settings change applies at once).
	Owners func() []string
}

// ErrNotOwner refuses a connection.
var ErrNotOwner = errors.New("not one of the owner's devices")

func (a ProviderAuth) AllowConn(_ context.Context, id *Identity, _ Target) error {
	if id == nil {
		return fmt.Errorf("%w: the provider does not identify callers", ErrNotOwner)
	}
	if len(id.Tags) > 0 {
		return fmt.Errorf("%w: tagged node %s (%s)", ErrNotOwner, id.Node, strings.Join(id.Tags, ","))
	}
	if id.Shared {
		return fmt.Errorf("%w: node %s is shared from another tailnet", ErrNotOwner, id.Node)
	}
	var owners []string
	if a.Owners != nil {
		owners = a.Owners()
	}
	if id.Login == "" || !slices.ContainsFunc(owners, func(o string) bool { return strings.EqualFold(o, id.Login) }) {
		return fmt.Errorf("%w: %s on %s", ErrNotOwner, id.Login, id.Node)
	}
	return nil
}

func (ProviderAuth) AllowHTTP(http.ResponseWriter, *http.Request, *Identity, Target) bool {
	return true
}

func (ProviderAuth) Strip(*http.Request) {}
