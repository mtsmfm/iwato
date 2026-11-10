package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"time"

	"iwato/internal/ingress"
	"iwato/internal/ingress/tailscale"
)

// The admin UI's own tailnet node (docs/adr/0023): the tailscale provider
// brings it up, admits the owner logins' devices, terminates TLS and hands
// the connections to the page's http.Server through connListener.

// adminHostname is the admin node's name: the nodes' prefix without its
// final hyphens (iwato by default). A sandbox's node is the prefix plus a
// name of one character or more, so it never has this name.
func adminHostname(prefix string) string {
	if h := strings.TrimRight(prefix, "-"); h != "" {
		return h
	}
	return "iwato"
}

// adminNode is the tailscale provider's Admin for this server.
func (m *ingressManager) adminNode(cfg *IngressConfig) *tailscale.Admin {
	if m.s.fixedPublic {
		return nil
	}
	auth := ingress.ProviderAuth{Owners: m.owners}
	return &tailscale.Admin{
		Hostname: adminHostname(cfg.prefix()),
		Allow: func(id *ingress.Identity) error {
			return auth.AllowConn(context.Background(), id, ingress.Target{})
		},
		Serve: m.adminLn.put,
		Up:    m.s.adminUp,
	}
}

// adminUp makes the admin node's address the page's public origin, or
// drops it once the node is closed.
func (s *Server) adminUp(dnsName string) {
	if s.fixedPublic {
		return
	}
	if dnsName == "" {
		if s.publicHost() != "" {
			log.Println("tailnet: the page's node is closed")
		}
		s.setPublic("", "")
		return
	}
	origin := "https://" + dnsName
	s.setPublic(dnsName, origin)
	log.Printf("tailnet: the page is at %s/", origin)
	fmt.Printf("On the tailnet (owner logins' devices): %s/\n", origin)
}

// waitPublic waits until the page has a public origin.
func (s *Server) waitPublic(d time.Duration) string {
	for deadline := time.Now().Add(d); ; time.Sleep(250 * time.Millisecond) {
		if o := s.publicOrigin(); o != "" || time.Now().After(deadline) {
			return o
		}
	}
}

// adminExpected: the settings publish with Tailscale and a secret is set,
// so the admin node is coming up.
func (m *ingressManager) adminExpected() bool {
	return !m.s.fixedPublic && m.config().provider() == providerTailscale && m.secretValue() != ""
}

// adminView is the admin node for the settings.
type adminView struct {
	Hostname string `json:"hostname"`
	URL      string `json:"url,omitempty"`
	// Fixed is -public-origin's origin, used instead of the node.
	Fixed string `json:"fixed,omitempty"`
	ingress.Status
}

type adminStatuser interface {
	AdminStatus() (ingress.Status, bool)
}

func (m *ingressManager) adminView() *adminView {
	cfg := m.config()
	if cfg.provider() != providerTailscale {
		return nil
	}
	v := &adminView{Hostname: adminHostname(cfg.prefix()), Status: ingress.Status{State: "off"}}
	if m.s.fixedPublic {
		v.Fixed = m.s.publicOrigin()
		return v
	}
	m.mu.Lock()
	p := m.provider
	m.mu.Unlock()
	if a, ok := p.(adminStatuser); ok {
		if st, ok := a.AdminStatus(); ok {
			v.Status = st
			if st.State == "connected" && st.DNSName != "" {
				v.URL = "https://" + st.DNSName + "/"
			}
		}
	}
	return v
}

// connListener is a net.Listener fed with connections from elsewhere.
type connListener struct {
	ch   chan net.Conn
	done chan struct{}
	once sync.Once
}

func newConnListener() *connListener {
	return &connListener{ch: make(chan net.Conn), done: make(chan struct{})}
}

func (l *connListener) put(c net.Conn) {
	select {
	case l.ch <- c:
	case <-l.done:
		_ = c.Close()
	}
}

func (l *connListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.ch:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *connListener) Close() error {
	l.once.Do(func() { close(l.done) })
	return nil
}

func (l *connListener) Addr() net.Addr { return adminAddr{} }

type adminAddr struct{}

func (adminAddr) Network() string { return "tailnet" }
func (adminAddr) String() string  { return "admin node" }
