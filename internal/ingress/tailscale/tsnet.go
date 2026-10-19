package tailscale

import (
	"context"
	"crypto/tls"
	"net"
	"net/netip"
	"os"
	"strings"
	"sync"

	"tailscale.com/client/local"
	"tailscale.com/envknob"
	"tailscale.com/logtail"
	"tailscale.com/tsnet"

	"iwato/internal/ingress"
)

var noUploads sync.Once

// disableLogUploads keeps tsnet from sending its logs to Tailscale
// (log.tailscale.com), as tailscaled --no-logs-no-support. A tailnet that
// requires log streaming refuses such nodes; Iwato's own log keeps them.
func disableLogUploads() {
	noUploads.Do(func() {
		envknob.SetNoLogsNoSupport()
		logtail.Disable()
	})
}

// tunnelMTU is the WireGuard tunnel MTU of Iwato's nodes. Tailscale's default
// of 1280 (a wire packet of 1360) is dropped on some paths, seen when a home
// router hairpins the node's own traffic: full-size packets vanish, so TLS
// handshakes and downloads hang or crawl. 1200 leaves room (found with
// tailscaled's TS_DEBUG_MTU). The userspace netstack reads the knob each
// time it sizes a packet (tstun.DefaultTUNMTU).
const tunnelMTU = "1200"

var lowerMTU sync.Once

// lowerTunnelMTU sets TS_DEBUG_MTU for the nodes, unless the owner set it.
func lowerTunnelMTU() {
	lowerMTU.Do(setTunnelMTU)
}

func setTunnelMTU() {
	if _, set := os.LookupEnv("TS_DEBUG_MTU"); !set {
		envknob.Setenv("TS_DEBUG_MTU", tunnelMTU)
	}
}

type tsNode struct {
	s  *tsnet.Server
	lc *local.Client
}

func newTSNode(c NodeConfig) Node {
	disableLogUploads()
	lowerTunnelMTU()
	s := &tsnet.Server{
		Hostname:      c.Hostname,
		Dir:           c.Dir,
		AuthKey:       c.AuthKey,
		AdvertiseTags: c.Tags,
		Ephemeral:     false,
		Logf:          c.Logf,
		UserLogf:      c.Logf,
	}
	return &tsNode{s: s}
}

func (n *tsNode) Up(ctx context.Context) (string, error) {
	st, err := n.s.Up(ctx)
	if err != nil {
		return "", err
	}
	lc, err := n.s.LocalClient()
	if err != nil {
		return "", err
	}
	n.lc = lc
	name := ""
	if st.Self != nil {
		name = strings.TrimSuffix(st.Self.DNSName, ".")
	}
	if d := n.s.CertDomains(); name == "" && len(d) > 0 {
		name = d[0]
	}
	return name, nil
}

func (n *tsNode) Handle(fn func(src, dst netip.AddrPort) (func(net.Conn), bool)) func() {
	return n.s.RegisterFallbackTCPHandler(fn)
}

func (n *tsNode) WhoIs(ctx context.Context, src netip.AddrPort) (*ingress.Identity, error) {
	w, err := n.lc.WhoIs(ctx, src.String())
	if err != nil {
		return nil, err
	}
	id := &ingress.Identity{Provider: "tailscale"}
	if w.Node != nil {
		id.Node = w.Node.ComputedName
		if id.Node == "" {
			id.Node = strings.TrimSuffix(w.Node.Name, ".")
		}
		id.Tags = append([]string(nil), w.Node.Tags...)
		id.Shared = w.Node.Sharer != 0
	}
	if w.UserProfile != nil && len(id.Tags) == 0 {
		id.Login = w.UserProfile.LoginName
	}
	return id, nil
}

func (n *tsNode) GetCertificate(hi *tls.ClientHelloInfo) (*tls.Certificate, error) {
	return n.lc.GetCertificate(hi)
}

func (n *tsNode) Logout(ctx context.Context) error {
	lc, err := n.s.LocalClient()
	if err != nil {
		return err
	}
	return lc.Logout(ctx)
}

func (n *tsNode) Close() error { return n.s.Close() }
