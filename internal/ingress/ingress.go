// Package ingress exposes sandbox ports to the owner's devices. It is split
// in five layers so that the way of exposing (the provider) can change
// while the rest stays:
//
//  1. Route: (sandbox, port) and its overrides from the configuration
//     (Routes). Routes are computed, never listed.
//  2. Backend dialer: a net.Conn to the sandbox's port (Dialer, Backend),
//     through OpenShell's ForwardTcp, with a per-sandbox cap.
//  3. Auth policy: who may connect (Authorizer), per connection and per
//     HTTP request.
//  4. Exposure provider ("class"): claims names, listens, accepts and hands
//     connections to the core (Provider: local, the default, routes
//     <port>.<sandbox>.localhost on Iwato's own listener; tailscale gives
//     each sandbox a tsnet node).
//  5. Protocol: HTTP goes through a reverse proxy (WebSocket, Host
//     rewrite, keep-alive to the sandbox), anything else is relayed as TCP.
//
// Core (core.go) runs layers 3, 5 and 2 in that order for every provider.
package ingress

import (
	"context"
	"errors"
	"net"
	"net/http"
)

// Target is a port on a sandbox's loopback.
type Target struct {
	Sandbox string
	Port    uint16
}

// Mode is how a port's connections are handled.
type Mode string

const (
	ModeAuto Mode = ""     // decided from the connection's first bytes
	ModeHTTP Mode = "http" // always HTTP
	ModeTCP  Mode = "tcp"  // always relayed as TCP
)

// HostPolicy is the Host an HTTP request reaches the sandbox with.
type HostPolicy string

const (
	// HostLocalhost (default): Host localhost:PORT and an Origin of the
	// public URL rewritten to http://localhost:PORT, as dev servers that
	// check them (Vite's allowedHosts, webpack-dev-server) expect.
	HostLocalhost HostPolicy = ""
	// HostForwarded: Host localhost:PORT, Origin as sent, and
	// X-Forwarded-Host with the public host (Rails with config.hosts).
	HostForwarded HostPolicy = "forwarded"
	// HostPreserve: the public Host and Origin as sent.
	HostPreserve HostPolicy = "preserve"
)

// PortOptions override a port's defaults (open, auto).
type PortOptions struct {
	Disabled bool
	Mode     Mode
	Host     HostPolicy
}

// Routes looks a target up in the configuration; it never lists ports.
type Routes interface {
	// Options is false when the sandbox is not exposed at all.
	Options(t Target) (PortOptions, bool)
}

// Errors from a Dialer.
var (
	ErrStopped = errors.New("sandbox is not running")  // HTTP 503, TCP closed
	ErrRefused = errors.New("nothing listens on port") // HTTP 502, TCP closed
	ErrBusy    = errors.New("connection limit")        // HTTP 503 Retry-After, TCP closed
)

// Refusals the core answers with a page on HTTP-only connections (others
// are closed).
var (
	ErrDisabled = errors.New("port not exposed")                      // HTTP 404
	ErrTCPOnly  = errors.New("port is TCP only (mode tcp)")           // HTTP 501
	ErrNotHere  = errors.New("request for another host on this conn") // HTTP 421
)

// Dialer connects to a target.
type Dialer interface {
	DialContext(ctx context.Context, t Target) (net.Conn, error)
}

// Identity is who connects, when the provider knows.
type Identity struct {
	Provider string   // "tailscale"
	Login    string   // "alice@github" (empty for a tagged node)
	Node     string   // the device's name
	Tags     []string // a tagged node's tags
	// Shared: a node shared into the tailnet from another one.
	Shared bool
}

// Authorizer decides who may connect.
type Authorizer interface {
	// AllowConn is asked once per connection (raw TCP included), with the
	// provider's identity (nil when it has none).
	AllowConn(ctx context.Context, id *Identity, t Target) error
	// AllowHTTP is asked per HTTP request. When it returns false it has
	// written the response (a redirect to log in, 401…).
	AllowHTTP(w http.ResponseWriter, r *http.Request, id *Identity, t Target) bool
	// Strip removes the authorizer's own credentials (cookies) from a
	// request before it goes to the sandbox.
	Strip(r *http.Request)
}

// Conn is a connection a provider accepted.
type Conn struct {
	net.Conn // TLS may be terminated by the provider already
	// Target is the connection's target: from the node's name (tailscale)
	// or the first request's Host (local).
	Target *Target
	// Resolve maps a Host to a target, for providers that route by Host
	// (local): every request on the connection must resolve to Target
	// (else 421).
	Resolve func(host string) (Target, bool)
	// Auth overrides the Server's for this connection's provider (local:
	// no identity, only direct connections from this PC).
	Auth Authorizer
	// AdminURL overrides the Server's on the error pages (local: this PC's
	// address of the admin UI).
	AdminURL string
	// Identity is nil when the provider has none.
	Identity *Identity
	// Secure: the browser sees https.
	Secure bool
	// HTTPOnly: the provider passes HTTP only (local: Host routing). Ports
	// the core refuses get a page instead of a closed connection.
	HTTPOnly bool
	// PublicHost is the name the client used without the port
	// (iwato-dev.tail0000.ts.net), for the Host rewrite and the error pages.
	PublicHost string
}

// Core serves a provider's connections; it is the only thing a provider
// calls.
type Core interface {
	// Admit is asked before a provider accepts a connection to t (it may
	// take a moment); false: refuse it (tailscale resets it, so the port
	// looks closed).
	Admit(t Target) bool
	Serve(c Conn)
}

// SandboxState is what a provider needs to know of a sandbox.
type SandboxState struct {
	Name    string
	Running bool
	// Exposed: the sandbox is to be reachable (not disabled in its
	// configuration).
	Exposed bool
}

// Status is a provider's state for the page.
type Status struct {
	// State is off, starting, connected or error.
	State   string `json:"state"`
	DNSName string `json:"dnsName,omitempty"`
	Message string `json:"message,omitempty"`
	// Code names Message for the page (web/i18n.js ingress.status.<code>),
	// with Params for its slots (values only).
	Code   string         `json:"code,omitempty"`
	Params map[string]any `json:"params,omitempty"`
}

// LogoutError is a node Forget could not log out: the owner removes it in
// the provider's console.
type LogoutError struct {
	Node string
	Err  error
}

func (e *LogoutError) Error() string {
	return "could not log node " + e.Node + " out (remove it in the Tailscale admin console): " + e.Err.Error()
}

func (e *LogoutError) Unwrap() error { return e.Err }

// Provider is a way of exposing sandboxes (an IngressClass).
type Provider interface {
	Name() string
	// Start begins accepting connections for core.
	Start(ctx context.Context, core Core) error
	// Sync is called with every sandbox whenever their list or states
	// change (idempotent): tailscale brings nodes up, keeps them and
	// forgets those of sandboxes no longer configured.
	Sync(ctx context.Context, sbs []SandboxState) error
	// Forget releases a deleted sandbox's name (tailscale: Logout and the
	// node's state removed).
	Forget(ctx context.Context, sandbox string) error
	// URL is the address the page shows (false while unknown).
	URL(t Target) (string, bool)
	Status(sandbox string) Status
	// HasIdentity tells whether connections carry an Identity (the
	// "provider" auth needs one).
	HasIdentity() bool
	Close() error
}
