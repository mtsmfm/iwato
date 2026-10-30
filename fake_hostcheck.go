//go:build uitest

package main

import (
	"context"
	"strings"
	"time"
)

// fakeHostLookups stand in for the resolver and RDAP: fakeMalwareHost is
// blocked and registered 6 days ago, a host with "unknown" in its name
// gets no answers, and every other domain is old and clean.
type fakeHostLookups struct{}

const fakeMalwareHost = "cdn-x7k2q.top"

func (fakeHostLookups) filter(_ context.Context, host string) string {
	switch {
	case host == fakeMalwareHost:
		return "blocked"
	case strings.Contains(host, "unknown"):
		return "unknown"
	}
	return "clean"
}

func (fakeHostLookups) registered(_ context.Context, domain string) (time.Time, bool) {
	switch {
	case domain == fakeMalwareHost:
		return time.Now().Add(-6 * 24 * time.Hour), true
	case strings.Contains(domain, "unknown"):
		return time.Time{}, false
	}
	return time.Date(2008, 3, 1, 0, 0, 0, 0, time.UTC), true
}
