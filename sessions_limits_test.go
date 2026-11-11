package main

import (
	"testing"
	"time"
)

// A sign-in ends after its idle limit without use or its longest limit
// after it began, under the limits there are now; -session-hours fixes it.
func TestSessionEnd(t *testing.T) {
	s := newServer(testConfig(), &idleRuntime{}, false, "127.0.0.1:17600")
	day := 24 * time.Hour
	now := time.Now()
	fresh := authSession{created: now.Add(-time.Hour), used: now.Add(-time.Hour)}
	if end := s.sessionEndLocked(fresh); !end.Equal(fresh.used.Add(7 * day)) {
		t.Fatalf("default idle 7 days: %v", end)
	}
	used := authSession{created: now.Add(-25 * day), used: now}
	if end := s.sessionEndLocked(used); !end.Equal(used.created.Add(30 * day)) {
		t.Fatalf("use extends it only up to 30 days: %v", end)
	}
	idle := authSession{created: now.Add(-10 * day), used: now.Add(-8 * day)}
	if now.Before(s.sessionEndLocked(idle)) {
		t.Fatal("8 days unused must have ended it")
	}
	s.config.Sessions = &SessionLimits{IdleDays: 10, MaxDays: 20}
	if !now.Before(s.sessionEndLocked(idle)) {
		t.Fatal("a longer limit applies to the sign-ins there are")
	}
	s.sessionTTL = 8 * time.Hour
	if end := s.sessionEndLocked(used); !end.Equal(used.created.Add(8 * time.Hour)) {
		t.Fatalf("-session-hours fixes it from the sign-in: %v", end)
	}
	if (&SessionLimits{IdleDays: 31, MaxDays: 30}).Validate() == nil || (&SessionLimits{MaxDays: 366}).Validate() == nil || (&SessionLimits{IdleDays: 3}).Validate() != nil {
		t.Fatal("limits validation")
	}
}
