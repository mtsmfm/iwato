package ingress

import (
	"bufio"
	"encoding/hex"
	"net/netip"
	"slices"
	"strconv"
	"strings"
)

// ListenPort is a TCP port a sandbox listens on, as the page shows it.
type ListenPort struct {
	Port uint16 `json:"port"`
	// Addresses it listens on (0.0.0.0, 127.0.0.1, ::, ::1).
	Addresses []string `json:"addresses"`
	// IPv6Only: only ::1 (Node's localhost); the ingress tries ::1 when
	// 127.0.0.1 refuses.
	IPv6Only bool `json:"ipv6Only,omitempty"`
}

// ListenScript (sh -c, in the sandbox) prints /proc/net/tcp and
// /proc/net/tcp6, after making OpenShell sweep stale listeners.
//
// OpenShell's network broker (the supervisor, PID 1) keeps its own copy of
// the sockets it creates for the workload (NVIDIA/OpenShell#4331): a
// listening socket stays in LISTEN, and accepts, after its process exited.
// v0.1.4 fixed it (NVIDIA/OpenShell#4444), but a sandbox created before
// keeps its v0.1.2 runtime, and the sweep costs v0.1.4 one failed bind.
// The broker closes the copies no workload process holds (it reads every
// /proc/PID/fd itself) when a workload bind fails with EADDRINUSE, so the
// script first binds to the supervisor's DNS relay, 127.0.0.53:53, which
// is always taken and never one of the user's servers. Iwato's exec cannot
// do the holder check itself: it cannot read the /proc/PID/fd of
// processes started from other sessions (the terminal). Without python3
// there is no sweep and stale listeners count too.
const ListenScript = `command -v python3 >/dev/null 2>&1 && python3 -I -c 'import socket; socket.socket().bind(("127.0.0.53", 53))' >/dev/null 2>&1
cat /proc/net/tcp /proc/net/tcp6 2>/dev/null
exit 0
`

// ParseProcNetTCP reads /proc/net/tcp and /proc/net/tcp6 (concatenated,
// as ListenScript prints them) and returns the ports in LISTEN (state 0A)
// that the sandbox's loopback reaches: those on any address, 127.0.0.1 or
// ::1. Others (the sandbox's DNS relay on 127.0.0.53, a single non-loopback
// address) are left out.
func ParseProcNetTCP(text string) []ListenPort {
	byPort := map[uint16]map[string]bool{}
	sc := bufio.NewScanner(strings.NewReader(text))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 4 || f[3] != "0A" {
			continue
		}
		addr, port, ok := parseHexAddr(f[1])
		if !ok || port == 0 {
			continue
		}
		var name string
		switch {
		case addr.IsUnspecified():
			name = addr.String()
		case addr == netip.MustParseAddr("127.0.0.1"), addr == netip.IPv6Loopback():
			name = addr.String()
		case addr.Is4In6() && addr.Unmap() == netip.MustParseAddr("127.0.0.1"):
			name = "127.0.0.1"
		default:
			continue
		}
		if byPort[port] == nil {
			byPort[port] = map[string]bool{}
		}
		byPort[port][name] = true
	}
	ports := make([]ListenPort, 0, len(byPort))
	for port, addrs := range byPort {
		lp := ListenPort{Port: port}
		for a := range addrs {
			lp.Addresses = append(lp.Addresses, a)
		}
		slices.Sort(lp.Addresses)
		lp.IPv6Only = len(lp.Addresses) == 1 && lp.Addresses[0] == "::1"
		ports = append(ports, lp)
	}
	slices.SortFunc(ports, func(a, b ListenPort) int { return int(a.Port) - int(b.Port) })
	return ports
}

// Listed tells whether the port is among the ports.
func Listed(ports []ListenPort, port uint16) bool {
	return slices.ContainsFunc(ports, func(p ListenPort) bool { return p.Port == port })
}

// parseHexAddr reads the kernel's "0100007F:1F90" (IPv4, each 32-bit word
// in host byte order, little-endian here) or the 32-digit IPv6 form.
func parseHexAddr(s string) (netip.Addr, uint16, bool) {
	h, p, ok := strings.Cut(s, ":")
	if !ok {
		return netip.Addr{}, 0, false
	}
	port, err := strconv.ParseUint(p, 16, 16)
	if err != nil {
		return netip.Addr{}, 0, false
	}
	raw, err := hex.DecodeString(h)
	if err != nil || (len(raw) != 4 && len(raw) != 16) {
		return netip.Addr{}, 0, false
	}
	// Each 4-byte word is little-endian.
	for i := 0; i < len(raw); i += 4 {
		raw[i], raw[i+1], raw[i+2], raw[i+3] = raw[i+3], raw[i+2], raw[i+1], raw[i]
	}
	addr, _ := netip.AddrFromSlice(raw)
	return addr, uint16(port), true
}
