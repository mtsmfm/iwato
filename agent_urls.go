package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"strings"
	"time"

	"iwato/internal/agentsmd"
	"iwato/internal/ingress"
	"iwato/internal/openshell/client"
)

// The standard image's /AGENTS.md links to a file in the home
// (agentsmd.Generated): the image's text, which iwato-entrypoint writes at
// start, and a section on the sandbox's published URLs, which Iwato
// writes into running sandboxes whenever it changes (ADR 0016).

// urlsWriter is a runtime that can write the section (OpenShell).
type urlsWriter interface {
	WriteURLs(ctx context.Context, sandbox, section string) error
}

// urlsPort stands in for the port when asking the provider for a URL.
const urlsPort = 65535

// urlPattern is the provider's URL form with PORT for the port, and
// whether it reaches the owner's other devices ("" while unknown).
func urlPattern(p ingress.Provider, st ingress.SandboxState) (string, bool) {
	if p == nil || !st.Exposed {
		return "", false
	}
	s, ok := p.URL(ingress.Target{Sandbox: st.Name, Port: urlsPort})
	if !ok {
		return "", false
	}
	n := fmt.Sprint(urlsPort)
	switch {
	case p.Name() == "local" && strings.HasPrefix(s, "http://"+n+"."):
		return "http://PORT." + strings.TrimPrefix(s, "http://"+n+"."), false
	case p.Name() == "tailscale" && strings.HasSuffix(s, ":"+n+"/"):
		return strings.TrimSuffix(s, ":"+n+"/") + ":PORT/", true
	}
	return "", false
}

// urlsSection is the section of /AGENTS.md for a sandbox.
func urlsSection(p ingress.Provider, st ingress.SandboxState) string {
	var b strings.Builder
	b.WriteString(agentsmd.Marker + "\n## Your URLs\n\nIwato rewrites this section when the URLs change; read it again before giving the owner a URL.\n\n")
	u, tailnet := urlPattern(p, st)
	switch {
	case u == "":
		b.WriteString("Iwato publishes no port of this sandbox right now (publishing is off, or Tailscale is not connected yet). Ask the owner to open the port in Iwato's 「ポート」 (Ports) tab.\n")
	case tailnet:
		fmt.Fprintf(&b, "A server listening here on PORT opens at `%s` (Tailscale: on the owner's devices, phone included; HTTP and any TCP). For example, a server on port 8000 opens at %s. Give the owner this URL, not localhost.\n", u, strings.Replace(u, "PORT", "8000", 1))
	default:
		fmt.Fprintf(&b, "A server listening here on PORT opens at `%s` (only in a browser on the owner's PC; HTTP and WebSocket only). For example, a server on port 8000 opens at %s. Give the owner this URL, not localhost.\n", u, strings.Replace(u, "PORT", "8000", 1))
	}
	return b.String()
}

// refreshURLs writes the section into running sandboxes whose section
// changed since the last write (each start writes again).
func (m *ingressManager) refreshURLs(states []ingress.SandboxState) {
	if m.writeURLs == nil {
		return
	}
	m.mu.Lock()
	p := m.provider
	m.mu.Unlock()
	want := map[string]string{}
	for _, st := range states {
		if st.Running {
			want[st.Name] = urlsSection(p, st)
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for name := range m.urls {
		if _, ok := want[name]; !ok {
			delete(m.urls, name)
		}
	}
	for name, c := range want {
		if m.urls[name] == c || m.urlsBusy[name] {
			continue
		}
		m.urls[name] = c
		m.urlsBusy[name] = true
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			err := m.writeURLs(ctx, name, c)
			if err != nil {
				log.Printf("agents section %s: %v", name, english(err))
			}
			m.mu.Lock()
			if err != nil && m.urls[name] == c {
				delete(m.urls, name) // tried again on the next sync
			}
			delete(m.urlsBusy, name)
			m.mu.Unlock()
		}()
	}
}

// urlsScript writes the generated /AGENTS.md (the image's text and the
// section $1) as the sandbox user when it differs, replacing a link in its
// place, and only in an image whose /AGENTS.md links to it.
const urlsScript = exportHome + `set -u
umask 077
h=${HOME:-}
case $h in /*) ;; *) exit 0 ;; esac
f=$h/` + agentsmd.GeneratedRel + `
[ "$(readlink /AGENTS.md 2>/dev/null)" = "$f" ] && [ -f ` + agentsmd.Static + ` ] || exit 0
[ -d "$h" ] && [ ! -L "$h" ] && [ -O "$h" ] && [ -w "$h" ] || exit 0
t=$f.$$
rm -f "$t"
set -C
{ cat ` + agentsmd.Static + ` && printf '\n%s' "$1"; } >"$t" || { rm -f "$t"; exit 1; }
if [ -f "$f" ] && [ ! -L "$f" ] && cmp -s "$t" "$f" 2>/dev/null; then rm -f "$t"; exit 0; fi
# mv would move into a directory a link there points to.
[ -L "$f" ] && rm -f "$f"
mv -f "$t" "$f"
`

func (o *OpenShell) WriteURLs(ctx context.Context, name, section string) error {
	_, conn, err := o.clients(ctx)
	if err != nil {
		return err
	}
	if conn == nil { // a test's fake sandbox API
		return nil
	}
	p, err := client.Start(ctx, conn, openshellWorkspace, name, []string{"/bin/sh", "-c", urlsScript, "iwato-agents-md", section}, client.Options{Env: termEnv})
	if err != nil {
		return err
	}
	defer p.Close()
	p.CloseWrite()
	out, _ := io.ReadAll(p)
	code, err := p.Wait()
	if err == nil && code != 0 {
		err = fmt.Errorf("exit %d: %s", code, strings.TrimSpace(string(out)))
	}
	return err
}
