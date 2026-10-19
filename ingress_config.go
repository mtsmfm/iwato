package main

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"iwato/internal/ingress"
)

// IngressConfig is how sandbox ports are exposed (internal/ingress). The
// secret (Tailscale's OAuth client secret or auth key) is not here: it is
// kept in ingress-secret.bin next to the configuration (ingress.go).
type IngressConfig struct {
	// Provider is "" or "local" (the default: *.localhost on this PC),
	// "tailscale" (opt-in: a tsnet node per sandbox) or "off".
	Provider string `json:"provider,omitempty"`
	// Auth is who may connect: "provider" (the default; the provider's
	// identity, Tailscale's WhoIs).
	Auth string `json:"auth,omitempty"`
	// Host is the default Host policy: "" or "localhost", "forwarded",
	// "preserve".
	Host      string                  `json:"host,omitempty"`
	Tailscale *TailscaleIngressConfig `json:"tailscale,omitempty"`
}

// TailscaleIngressConfig configures the tailscale provider.
type TailscaleIngressConfig struct {
	// Tag is the nodes' tag (the OAuth client's), tag:iwato by default.
	Tag string `json:"tag,omitempty"`
	// HostnamePrefix starts each node's name (prefix + sandbox).
	HostnamePrefix *string `json:"hostnamePrefix,omitempty"`
	// Owners are the logins (WhoIs) whose devices may connect.
	Owners []string `json:"owners,omitempty"`
}

// SandboxIngress overrides the ingress for one sandbox.
type SandboxIngress struct {
	Disabled bool `json:"disabled,omitempty"`
	// Auth overrides IngressConfig.Auth.
	Auth string `json:"auth,omitempty"`
	// Ports are per-port overrides, by port number.
	Ports map[string]PortIngress `json:"ports,omitempty"`
}

// PortIngress overrides one port.
type PortIngress struct {
	Disabled bool   `json:"disabled,omitempty"`
	Mode     string `json:"mode,omitempty"` // "", "http", "tcp"
	Host     string `json:"host,omitempty"` // "", "localhost", "forwarded", "preserve"
}

const (
	defaultTailscaleTag    = "tag:iwato"
	defaultHostnamePrefix  = "iwato-"
	maxIngressOwners       = 20
	maxIngressPortOverride = 64
)

var (
	tailscaleTag   = regexp.MustCompile(`^tag:[A-Za-z][A-Za-z0-9-]{0,62}$`)
	hostnamePrefix = regexp.MustCompile(`^([a-z0-9][a-z0-9-]{0,30})?$`)
	ownerLogin     = regexp.MustCompile(`^[^\s,;"'<>]{1,200}$`)
)

// tag is the nodes' tag.
func (c *IngressConfig) tag() string {
	if c != nil && c.Tailscale != nil && c.Tailscale.Tag != "" {
		return c.Tailscale.Tag
	}
	return defaultTailscaleTag
}

// prefix starts the nodes' names.
func (c *IngressConfig) prefix() string {
	if c != nil && c.Tailscale != nil && c.Tailscale.HostnamePrefix != nil {
		return *c.Tailscale.HostnamePrefix
	}
	return defaultHostnamePrefix
}

func (c *IngressConfig) owners() []string {
	if c == nil || c.Tailscale == nil {
		return nil
	}
	return c.Tailscale.Owners
}

// Ingress providers. A configuration without one (also those written
// before local existed, with Tailscale turned off) gets local.
const (
	providerLocal     = "local"
	providerTailscale = "tailscale"
	providerOff       = "off"
)

// provider is the effective provider: local, tailscale or off.
func (c *IngressConfig) provider() string {
	if c == nil || c.Provider == "" {
		return providerLocal
	}
	return c.Provider
}

// validAuth accepts the auth modes v1 implements.
func validAuth(a string) error {
	switch a {
	case "", "provider":
		return nil
	case "iwato", "both":
		return fmt.Errorf("auth %q is not supported yet (only \"provider\")", a)
	}
	return fmt.Errorf("unknown auth %q", a)
}

func validHostPolicy(h string) bool {
	switch h {
	case "", "localhost", "forwarded", "preserve":
		return true
	}
	return false
}

func (c *IngressConfig) Validate() error {
	if c == nil {
		return nil
	}
	switch c.Provider {
	case "", providerLocal, providerTailscale, providerOff:
	default:
		return fmt.Errorf("unknown ingress provider %q", c.Provider)
	}
	if err := validAuth(c.Auth); err != nil {
		return err
	}
	if !validHostPolicy(c.Host) {
		return fmt.Errorf("invalid ingress host %q", c.Host)
	}
	if t := c.Tailscale; t != nil {
		if t.Tag != "" && !tailscaleTag.MatchString(t.Tag) {
			return errors.New("tailscale tag must look like tag:iwato")
		}
		if t.HostnamePrefix != nil && !hostnamePrefix.MatchString(*t.HostnamePrefix) {
			return errors.New("tailscale hostnamePrefix: lowercase letters, digits and hyphens, at most 31")
		}
		if len(t.Owners) > maxIngressOwners {
			return fmt.Errorf("at most %d owners", maxIngressOwners)
		}
		for _, o := range t.Owners {
			if !ownerLogin.MatchString(o) {
				return fmt.Errorf("invalid owner login %q", o)
			}
		}
	}
	return nil
}

func (s *SandboxIngress) Validate() error {
	if s == nil {
		return nil
	}
	if err := validAuth(s.Auth); err != nil {
		return err
	}
	if len(s.Ports) > maxIngressPortOverride {
		return fmt.Errorf("at most %d port overrides", maxIngressPortOverride)
	}
	for k, p := range s.Ports {
		if n, err := strconv.ParseUint(k, 10, 16); err != nil || n == 0 || strconv.FormatUint(n, 10) != k {
			return fmt.Errorf("invalid port %q", k)
		}
		switch p.Mode {
		case "", "http", "tcp":
		default:
			return fmt.Errorf("port %s: invalid mode %q", k, p.Mode)
		}
		if !validHostPolicy(p.Host) {
			return fmt.Errorf("port %s: invalid host %q", k, p.Host)
		}
	}
	return nil
}

// portOptions are a target's effective options.
func portOptions(c *IngressConfig, sb Sandbox, port uint16) (ingress.PortOptions, bool) {
	if sb.Ingress != nil && sb.Ingress.Disabled {
		return ingress.PortOptions{}, false
	}
	o := ingress.PortOptions{Host: hostPolicy(c.hostDefault())}
	if sb.Ingress != nil {
		if p, ok := sb.Ingress.Ports[strconv.Itoa(int(port))]; ok {
			o.Disabled = p.Disabled
			o.Mode = ingress.Mode(p.Mode)
			if p.Host != "" {
				o.Host = hostPolicy(p.Host)
			}
		}
	}
	return o, true
}

func (c *IngressConfig) hostDefault() string {
	if c == nil {
		return ""
	}
	return c.Host
}

func hostPolicy(h string) ingress.HostPolicy {
	if h == "localhost" {
		return ingress.HostLocalhost
	}
	return ingress.HostPolicy(h)
}

// normalizeOwners trims, drops empty and repeated logins.
func normalizeOwners(list []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, o := range list {
		o = strings.TrimSpace(o)
		if o == "" || seen[strings.ToLower(o)] {
			continue
		}
		seen[strings.ToLower(o)] = true
		out = append(out, o)
	}
	return out
}
