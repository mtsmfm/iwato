package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/publicsuffix"
)

// The approval's host checks (ADR 0022): warnings, never blocks. They run
// on the host with short timeouts and are cached; a lookup that fails says
// "unknown" and nothing waits for it.

// HostCheck is what Iwato found about a host. Filter is the malware-
// filtering resolver's answer ("blocked", "clean", "unknown", or
// "skipped" for an address or a wildcard); Registered is the registrable
// domain's registration time by RDAP (0: unknown, RDAP "found",
// "unknown" or "skipped").
type HostCheck struct {
	Host       string `json:"host"`
	Domain     string `json:"domain,omitempty"`
	Exfil      string `json:"exfil,omitempty"`
	Filter     string `json:"filter"`
	Registered int64  `json:"registered,omitempty"`
	RDAP       string `json:"rdap"`
}

// exfilServices are services made to receive or expose arbitrary data:
// request catchers, tunnels, out-of-band testing, pastes and file drops.
// A host matches one or a subdomain of one.
var exfilServices = []string{
	"webhook.site",
	"requestbin.com", "requestbin.net", "requestcatcher.com",
	"m.pipedream.net",
	"ngrok.io", "ngrok-free.app", "ngrok.app",
	"oast.fun", "oast.me", "oast.pro", "oast.live", "oast.site", "oast.online", "interact.sh",
	"burpcollaborator.net",
	"pastebin.com", "transfer.sh", "file.io", "0x0.st", "temp.sh",
}

// exfilService is the listed service host belongs to ("" when none). A
// wildcard host (*.ngrok.io) matches by what follows the wildcard.
func exfilService(host string) string {
	h := strings.TrimSuffix(strings.ToLower(host), ".")
	h = h[strings.LastIndex(h, "*")+1:]
	h = strings.TrimPrefix(h, ".")
	for _, s := range exfilServices {
		if h == s || strings.HasSuffix(h, "."+s) {
			return s
		}
	}
	return ""
}

// registeredDomain is the domain a registry has on record for name: the
// label before its ICANN suffix ("" when there is none). Private suffixes
// (ngrok.io, github.io) do not count: their subdomains are not registered.
func registeredDomain(name string) string {
	suffix, icann := publicsuffix.PublicSuffix(name)
	for !icann && strings.Contains(suffix, ".") {
		suffix, icann = publicsuffix.PublicSuffix(suffix[strings.Index(suffix, ".")+1:])
	}
	if suffix == name || !strings.HasSuffix(name, "."+suffix) {
		return ""
	}
	rest := strings.TrimSuffix(name, "."+suffix)
	return rest[strings.LastIndex(rest, ".")+1:] + "." + suffix
}

// hostLookups are the network lookups: the filtering resolver's verdict
// and the registration time (zero: unknown).
type hostLookups interface {
	filter(ctx context.Context, host string) string
	registered(ctx context.Context, domain string) (time.Time, bool)
}

// hostChecker checks hosts and caches the results.
type hostChecker struct {
	look  hostLookups
	mu    sync.Mutex
	cache map[string]hostCheckEntry
}

type hostCheckEntry struct {
	check HostCheck
	until time.Time
}

const (
	hostCheckTTL     = 6 * time.Hour
	hostCheckRetry   = 10 * time.Minute
	hostCheckEntries = 2000
)

func newHostChecker(look hostLookups) *hostChecker {
	return &hostChecker{look: look, cache: map[string]hostCheckEntry{}}
}

// Check answers from the cache or looks the host up (both lookups at once).
func (c *hostChecker) Check(ctx context.Context, host string) HostCheck {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	c.mu.Lock()
	if e, ok := c.cache[host]; ok && time.Now().Before(e.until) {
		c.mu.Unlock()
		return e.check
	}
	c.mu.Unlock()
	out := HostCheck{Host: host, Exfil: exfilService(host), Filter: "skipped", RDAP: "skipped"}
	if net.ParseIP(host) == nil {
		name := host[strings.LastIndex(host, "*")+1:]
		name = strings.TrimPrefix(name, ".")
		out.Domain = registeredDomain(name)
		var wg sync.WaitGroup
		if !strings.Contains(host, "*") {
			wg.Go(func() { out.Filter = c.look.filter(ctx, host) })
		}
		if out.Domain != "" {
			wg.Go(func() {
				out.RDAP = "unknown"
				if t, ok := c.look.registered(ctx, out.Domain); ok {
					out.RDAP, out.Registered = "found", t.Unix()
				}
			})
		}
		wg.Wait()
	}
	ttl := hostCheckTTL
	if out.Filter == "unknown" || out.RDAP == "unknown" {
		ttl = hostCheckRetry
	}
	c.mu.Lock()
	if len(c.cache) >= hostCheckEntries {
		clear(c.cache)
	}
	c.cache[host] = hostCheckEntry{check: out, until: time.Now().Add(ttl)}
	c.mu.Unlock()
	return out
}

// netLookups asks Cloudflare's malware-filtering resolver (1.1.1.2, over
// DNS over HTTPS) and the registries' RDAP servers (found through IANA's
// bootstrap file).
type netLookups struct {
	client    *http.Client
	dohURL    string
	bootstrap string
	mu        sync.Mutex
	rdap      map[string]string // TLD → RDAP base URL
	rdapUntil time.Time
}

func newNetLookups() *netLookups {
	return &netLookups{
		client:    &http.Client{Timeout: 4 * time.Second},
		dohURL:    "https://security.cloudflare-dns.com/dns-query",
		bootstrap: "https://data.iana.org/rdap/dns.json",
	}
}

func (n *netLookups) get(ctx context.Context, u, accept string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)
	res, err := n.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", u, res.Status)
	}
	return io.ReadAll(io.LimitReader(res.Body, 1<<20))
}

func (n *netLookups) filter(ctx context.Context, host string) string {
	b, err := n.get(ctx, n.dohURL+"?type=A&name="+url.QueryEscape(host), "application/dns-json")
	if err != nil {
		return "unknown"
	}
	return dohVerdict(b)
}

// dohVerdict reads a DNS JSON answer: the filtering resolver answers a
// blocked domain with 0.0.0.0. A name that does not exist is not blocked.
func dohVerdict(b []byte) string {
	var a struct {
		Status *int
		Answer []struct {
			Type int    `json:"type"`
			Data string `json:"data"`
		}
	}
	if json.Unmarshal(b, &a) != nil || a.Status == nil {
		return "unknown"
	}
	if *a.Status != 0 && *a.Status != 3 {
		return "unknown"
	}
	for _, x := range a.Answer {
		if x.Type == 1 && x.Data == "0.0.0.0" {
			return "blocked"
		}
	}
	return "clean"
}

func (n *netLookups) registered(ctx context.Context, domain string) (time.Time, bool) {
	base, err := n.rdapBase(ctx, domain[strings.LastIndex(domain, ".")+1:])
	if err != nil || base == "" {
		return time.Time{}, false
	}
	b, err := n.get(ctx, strings.TrimSuffix(base, "/")+"/domain/"+url.PathEscape(domain), "application/rdap+json")
	if err != nil {
		return time.Time{}, false
	}
	return rdapRegistration(b)
}

// rdapBase is the RDAP server for a TLD from IANA's bootstrap file, read
// once a day (after a failure, again after hostCheckRetry).
func (n *netLookups) rdapBase(ctx context.Context, tld string) (string, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if time.Now().After(n.rdapUntil) {
		b, err := n.get(ctx, n.bootstrap, "application/json")
		var m map[string]string
		if err == nil {
			m, err = rdapBootstrap(b)
		}
		if err != nil {
			n.rdapUntil = time.Now().Add(hostCheckRetry)
			return "", err
		}
		n.rdap, n.rdapUntil = m, time.Now().Add(24*time.Hour)
	}
	return n.rdap[tld], nil
}

// rdapBootstrap maps each TLD of IANA's DNS bootstrap file to its first
// HTTPS server.
func rdapBootstrap(b []byte) (map[string]string, error) {
	var f struct {
		Services [][][]string `json:"services"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, s := range f.Services {
		if len(s) != 2 {
			continue
		}
		for _, u := range s[1] {
			if strings.HasPrefix(u, "https://") {
				for _, tld := range s[0] {
					out[strings.ToLower(tld)] = u
				}
				break
			}
		}
	}
	if len(out) == 0 {
		return nil, errors.New("empty RDAP bootstrap")
	}
	return out, nil
}

// rdapRegistration is a domain object's registration event.
func rdapRegistration(b []byte) (time.Time, bool) {
	var d struct {
		Events []struct {
			Action string `json:"eventAction"`
			Date   string `json:"eventDate"`
		} `json:"events"`
	}
	if json.Unmarshal(b, &d) != nil {
		return time.Time{}, false
	}
	for _, e := range d.Events {
		if e.Action != "registration" {
			continue
		}
		for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05", "2006-01-02"} {
			if t, err := time.Parse(layout, e.Date); err == nil {
				return t, true
			}
		}
	}
	return time.Time{}, false
}

// handleHostCheck is GET /api/hostcheck?host=HOST.
func (s *Server) handleHostCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		fail(w, 405, "method not allowed")
		return
	}
	if !s.require(w, r, false) {
		return
	}
	host := r.URL.Query().Get("host")
	if !hostPattern.MatchString(host) {
		fail(w, 400, "invalid host")
		return
	}
	ctx, cancel := timeout(r.Context(), 6*time.Second)
	defer cancel()
	s.hostChecksOnce.Do(func() {
		if s.hostChecks == nil {
			s.hostChecks = newHostChecker(newNetLookups())
		}
	})
	jsonResponse(w, 200, s.hostChecks.Check(ctx, host))
}
