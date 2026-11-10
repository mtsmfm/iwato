package main

import (
	"bufio"
	"net"
	"net/http"
	"testing"
)

// Connections from the admin node reach the page once the node is up, on
// its name only; -public-origin keeps its own.
func TestAdminNodeOrigin(t *testing.T) {
	s := newServer(testConfig(), &idleRuntime{}, false, "127.0.0.1:17600")
	h := &http.Server{Handler: s.Handler()}
	go h.Serve(s.ingress.adminLn)
	t.Cleanup(func() { h.Close() })
	get := func(host string) int {
		t.Helper()
		client, server := net.Pipe()
		defer client.Close()
		go s.ingress.adminLn.put(server)
		go client.Write([]byte("GET /manifest.webmanifest HTTP/1.1\r\nHost: " + host + "\r\nConnection: close\r\n\r\n"))
		res, err := http.ReadResponse(bufio.NewReader(client), nil)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	if got := get("iwato.tail0000.ts.net"); got != 403 {
		t.Fatalf("before the node is up: %d", got)
	}
	s.adminUp("iwato.tail0000.ts.net")
	if s.publicOrigin() != "https://iwato.tail0000.ts.net" || get("iwato.tail0000.ts.net") != 200 || get("evil.example") != 403 {
		t.Fatal(s.publicOrigin())
	}
	s.adminUp("")
	if s.publicOrigin() != "" || get("iwato.tail0000.ts.net") != 403 {
		t.Fatal("still public after the node closed")
	}
	s.setPublic("pc.tail0000.ts.net", "https://pc.tail0000.ts.net")
	s.fixedPublic = true
	s.adminUp("iwato.tail0000.ts.net")
	if s.publicOrigin() != "https://pc.tail0000.ts.net" {
		t.Fatal("the admin node replaced -public-origin")
	}
	if s.ingress.adminNode(testConfig().Ingress) != nil {
		t.Fatal("an admin node with -public-origin")
	}
}

func TestAdminHostname(t *testing.T) {
	for prefix, want := range map[string]string{"iwato-": "iwato", "": "iwato", "lab": "lab"} {
		if got := adminHostname(prefix); got != want {
			t.Errorf("%q: %q", prefix, got)
		}
	}
	c := testConfig()
	c.Ingress = &IngressConfig{Provider: providerTailscale, Tailscale: &TailscaleIngressConfig{HostnamePrefix: new(string)}}
	c.Sandboxes = []Sandbox{{Name: "iwato"}}
	if c.Validate() == nil {
		t.Fatal("a sandbox named like the admin node")
	}
}
