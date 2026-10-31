package main

import (
	"context"
	"testing"
	"time"
)

func TestExfilService(t *testing.T) {
	for host, want := range map[string]string{
		"webhook.site":             "webhook.site",
		"abc.webhook.site.":        "webhook.site",
		"eo1234.m.pipedream.net":   "m.pipedream.net",
		"pipedream.net":            "",
		"*.ngrok-free.app":         "ngrok-free.app",
		"x.oast.fun":               "oast.fun",
		"notwebhook.site":          "",
		"pastebin.com.example.org": "",
		"api.github.com":           "",
	} {
		if got := exfilService(host); got != want {
			t.Errorf("%s: %q, want %q", host, got, want)
		}
	}
}

func TestDOHVerdict(t *testing.T) {
	for in, want := range map[string]string{
		`{"Status":0,"Answer":[{"name":"x.top","type":1,"data":"0.0.0.0"}]}`:                       "blocked",
		`{"Status":0,"Answer":[{"type":5,"data":"a.example."},{"type":1,"data":"93.184.215.14"}]}`: "clean",
		`{"Status":3}`: "clean",
		`{"Status":2}`: "unknown",
		`{}`:           "unknown",
		`<html>`:       "unknown",
	} {
		if got := dohVerdict([]byte(in)); got != want {
			t.Errorf("%s: %s, want %s", in, got, want)
		}
	}
}

func TestRDAPRegistration(t *testing.T) {
	got, ok := rdapRegistration([]byte(`{"events":[{"eventAction":"expiration","eventDate":"2030-01-01T00:00:00Z"},{"eventAction":"registration","eventDate":"2026-10-24T08:11:52.123Z"}]}`))
	if !ok || !got.Equal(time.Date(2026, 10, 24, 8, 11, 52, 123e6, time.UTC)) {
		t.Fatal(got, ok)
	}
	if got, ok := rdapRegistration([]byte(`{"events":[{"eventAction":"registration","eventDate":"1997-09-15T04:00:00"}]}`)); !ok || got.Year() != 1997 {
		t.Fatal(got, ok)
	}
	if _, ok := rdapRegistration([]byte(`{"events":[{"eventAction":"last changed","eventDate":"2026-01-01T00:00:00Z"}]}`)); ok {
		t.Fatal("no registration event")
	}
	m, err := rdapBootstrap([]byte(`{"services":[[["com","net"],["http://x/","https://rdap.verisign.com/com/v1/"]],[["top"],["https://rdap.nic.top/"]]]}`))
	if err != nil || m["net"] != "https://rdap.verisign.com/com/v1/" || m["top"] != "https://rdap.nic.top/" {
		t.Fatal(m, err)
	}
}

type countingLookups struct{ calls int }

func (c *countingLookups) filter(context.Context, string) string { c.calls++; return "unknown" }
func (c *countingLookups) registered(context.Context, string) (time.Time, bool) {
	c.calls++
	return time.Time{}, false
}

// The registrable domain is looked up, addresses and wildcards skip what
// does not apply, and results are cached.
func TestHostCheckerLookups(t *testing.T) {
	look := &countingLookups{}
	c := newHostChecker(look)
	got := c.Check(context.Background(), "API.Example.co.uk")
	if got.Domain != "example.co.uk" || got.Filter != "unknown" || got.RDAP != "unknown" || look.calls != 2 {
		t.Fatal(got, look.calls)
	}
	c.Check(context.Background(), "api.example.co.uk")
	if look.calls != 2 {
		t.Fatal("not cached")
	}
	if got := c.Check(context.Background(), "*.x.ngrok.io"); got.Domain != "ngrok.io" || got.Filter != "skipped" || got.RDAP != "unknown" || got.Exfil != "ngrok.io" {
		t.Fatal(got)
	}
	if got := c.Check(context.Background(), "100.64.0.1"); got.Filter != "skipped" || got.RDAP != "skipped" {
		t.Fatal(got)
	}
}
