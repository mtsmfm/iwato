package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"iwato/internal/termsync"
)

func TestPublicOriginForTailscaleServe(t *testing.T) {
	if _, _, e := parsePublicOrigin("http://x.ts.net"); e == nil {
		t.Fatal("http accepted")
	}
	for _, bad := range []string{"https://x.ts.net/path", "https://u@x.ts.net", "https://x.ts.net?q", "x.ts.net"} {
		if _, _, e := parsePublicOrigin(bad); e == nil {
			t.Errorf("accepted %s", bad)
		}
	}
	host, origin, e := parsePublicOrigin("https://pc.tail1234.ts.net/")
	if e != nil || host != "pc.tail1234.ts.net" || origin != "https://pc.tail1234.ts.net" {
		t.Fatal(host, origin, e)
	}
	s := newServer(testConfig(), &reviewRuntime{}, false, "127.0.0.1:29999")
	s.publicHost, s.publicOrigin = host, origin
	w := reviewRequest(s, "POST", "/api/session", `{"token":"`+s.bootstrap+`"}`, origin, "same-origin", host, nil, "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	cookie := w.Result().Cookies()[0]
	if !cookie.Secure {
		t.Fatal("https session cookie must be Secure")
	}
	var a map[string]string
	json.Unmarshal(w.Body.Bytes(), &a)
	// Tailscale serve may forward either the public Host or the loopback Host.
	for _, h := range []string{host, s.host} {
		if code := reviewRequest(s, "POST", "/api/sandboxes/dev/start", "", origin, "same-origin", h, cookie, a["csrf"]).Code; code != 200 {
			t.Fatalf("host %s: %d", h, code)
		}
	}
	if code := reviewRequest(s, "GET", "/api/sandboxes", "", "https://other.ts.net", "", host, cookie, "").Code; code != 403 {
		t.Fatal("foreign origin accepted", code)
	}
	if code := reviewRequest(s, "GET", "/api/sandboxes", "", "", "", "other.ts.net", cookie, "").Code; code != 403 {
		t.Fatal("foreign host accepted", code)
	}
}

type notifyRuntime struct {
	reviewRuntime
	proposals ProposalView
	err       error
}

func (n *notifyRuntime) Proposals(context.Context, Sandbox) (ProposalView, error) {
	return n.proposals, n.err
}
func (n *notifyRuntime) DecideProposal(context.Context, Sandbox, string, string, string, *ProposalScope) (PolicyChange, error) {
	return PolicyChange{}, nil
}

func TestNotifierDeliversNewItemsOnce(t *testing.T) {
	rt := &notifyRuntime{}
	rt.proposals.Pending = []PendingProposal{{ID: strings.Repeat("a", 32), State: "pending-approval", Source: "agent", Summary: "/usr/bin/git → github.com:443"}}
	s := newServer(testConfig(), rt, false, "127.0.0.1:1")
	n := newNotifier(s)
	w := s.config.Sandboxes[0]
	first := n.collect(context.Background(), w)
	if len(first) != 1 || !strings.Contains(first[0].Body, "github.com") {
		t.Fatalf("%+v", first)
	}
	// A click opens the sandbox's policy tab.
	if first[0].Path != "/?sandbox="+w.Name+"&tab=policy" {
		t.Fatal(first[0].Path)
	}
	rt.proposals.Pending = append(rt.proposals.Pending, PendingProposal{ID: strings.Repeat("c", 32), State: "pending-approval", Source: "agent", Summary: "/usr/bin/curl → pypi.org:443"},
		PendingProposal{ID: strings.Repeat("d", 32), State: "pending-approval", Source: "auto", Summary: "/usr/bin/curl → example.com:443"})
	second := n.collect(context.Background(), w)
	if len(second) != 1 || !strings.Contains(second[0].Body, "pypi.org") {
		t.Fatalf("%+v", second)
	}
	// The badge counts the agent's proposals only, as the page does.
	if n.pending[w.Name] != 2 {
		t.Fatal(n.pending[w.Name])
	}
	rt.err = context.DeadlineExceeded
	if again := n.collect(context.Background(), w); len(again) != 0 {
		t.Fatalf("transient error caused redelivery: %+v", again)
	}
	rt.err = nil
	if again := n.collect(context.Background(), w); len(again) != 0 {
		t.Fatalf("redelivered after recovery: %+v", again)
	}
}

// The first push after a quiet spell goes out at once; what comes within the
// window is held and goes out as one push when it is over.
func TestNotifierBatchesWithinTheWindow(t *testing.T) {
	rt := &notifyRuntime{}
	s := newServer(testConfig(), rt, false, "127.0.0.1:1")
	n := newNotifier(s)
	clock := time.Unix(1_000_000, 0)
	n.now = func() time.Time { return clock }
	list := s.sandboxList()
	poll := func() []PushMessage { return n.batch(n.gather(context.Background(), list)) }
	add := func(id, summary string) {
		rt.proposals.Pending = append(rt.proposals.Pending, PendingProposal{ID: strings.Repeat(id, 32), State: "pending-approval", Source: "agent", Summary: summary})
	}
	add("a", "first")
	if got := poll(); len(got) != 1 {
		t.Fatalf("first push must go out at once: %+v", got)
	}
	for _, p := range []struct{ id, summary string }{{"b", "second"}, {"c", "third"}} {
		clock = clock.Add(20 * time.Second)
		add(p.id, p.summary)
		if got := poll(); len(got) != 0 {
			t.Fatalf("pushed within the window: %+v", got)
		}
	}
	clock = clock.Add(30 * time.Second)
	got := poll()
	if len(got) != 1 || !strings.Contains(got[0].Title, "承認待ち 2 件") || !strings.Contains(got[0].Body, "second") || !strings.Contains(got[0].Body, "third") || strings.Contains(got[0].Body, "first") {
		t.Fatalf("held messages must go out as one: %+v", got)
	}
}

type screensNotifyRuntime struct {
	notifyRuntime
	screens map[string]termScreen
}

func (r *screensNotifyRuntime) TerminalScreens(Sandbox) map[string]termScreen { return r.screens }

// Through collect: the messages, the marks, and no push while a page is in
// use (the rules are in notifier_test.go).
func TestNotifierPushesATerminalThatStopped(t *testing.T) {
	rt := &screensNotifyRuntime{}
	s := newServer(testConfig(), rt, false, "127.0.0.1:1")
	n := newNotifier(s)
	clock := time.Unix(1_000_000, 0)
	n.now = func() time.Time { return clock }
	s.presence.now = n.now
	w := s.config.Sandboxes[0]
	var activity termsync.Activity
	show := func(lines ...string) {
		rt.screens = map[string]termScreen{"2": {cols: 80, rows: 4, lines: lines, activity: activity}}
	}
	poll := func(d time.Duration) []PushMessage {
		clock = clock.Add(d)
		return n.collect(context.Background(), w)
	}
	work := func(text string) {
		for i := range 6 {
			show(fmt.Sprintf("%s %c", text, 'a'+i), fmt.Sprintf("output %c", 'b'+i), "╭──╮", "│ > │")
			poll(termPoll)
		}
	}
	show("$ claude", "")
	poll(0)
	work("Working")
	show("Done: fixed the bug.", "", "╭──╮", "│ > │")
	poll(termPoll)
	if got := poll(idleAfter - time.Second); len(got) != 0 {
		t.Fatalf("too early: %+v", got)
	}
	got := poll(time.Second)
	if len(got) != 1 || got[0].Title != w.Name+" (2): 止まりました" || got[0].Body != "Done: fixed the bug." || got[0].Path != "/?sandbox="+w.Name+"&term=2" {
		t.Fatalf("%+v", got)
	}
	if got := poll(2 * time.Minute); len(got) != 0 || n.stopped(w.Name)["2"] == 0 {
		t.Fatalf("pushed twice or not marked: %+v", got)
	}

	// While a page is in use, a stop is marked, not pushed.
	page := strings.Repeat("a", 32)
	s.presence.report(page, true)
	work("Again")
	show("Done again.")
	poll(termPoll)
	if got := poll(idleAfter); len(got) != 0 || n.stopped(w.Name)["2"] != clock.Unix() {
		t.Fatalf("a stop while in use must be marked, not pushed: %+v %v", got, n.stopped(w.Name))
	}
	s.presence.report(page, false)

	// A bell: pushed at once, with the notification's text.
	activity.Alerts++
	activity.Recent = append(activity.Recent, termsync.Alert{At: clock, Text: "Claude is waiting for your input"})
	show("Done again.")
	got = poll(termPoll)
	if len(got) != 1 || got[0].Title != w.Name+" (2): 通知" || got[0].Body != "Claude is waiting for your input" {
		t.Fatalf("%+v", got)
	}
}

// While a page is open and in use on some device, nothing is pushed (to any
// device), held messages included, and not later either. A page counts
// until it reports itself hidden or presenceActive (+ grace) after its last
// report of being in use.
func TestNotifierHoldsBackWhilePresent(t *testing.T) {
	rt := &notifyRuntime{}
	s := newServer(testConfig(), rt, false, "127.0.0.1:1")
	n := newNotifier(s)
	clock := time.Unix(1_000_000, 0)
	n.now = func() time.Time { return clock }
	s.presence.now = n.now
	list := s.sandboxList()
	poll := func(d time.Duration) []PushMessage {
		clock = clock.Add(d)
		return n.batch(n.gather(context.Background(), list))
	}
	add := func(id string) {
		rt.proposals.Pending = append(rt.proposals.Pending, PendingProposal{ID: strings.Repeat(id, 32), State: "pending-approval", Source: "agent", Summary: id})
	}
	pc, phone := strings.Repeat("a", 32), strings.Repeat("b", 32)
	add("a")
	if got := poll(0); len(got) != 1 {
		t.Fatalf("nobody present: %+v", got)
	}
	add("b") // held within the window
	poll(10 * time.Second)
	s.presence.report(pc, true)
	s.presence.report(phone, true)
	s.presence.report(phone, false) // the phone is put away
	add("c")
	if got := poll(time.Minute); len(got) != 0 {
		t.Fatalf("pushed while a page is in use: %+v", got)
	}
	// The PC is left alone (visible, untouched) past presenceActive.
	if got := poll(presenceActive + presenceGrace - time.Minute - time.Second); len(got) != 0 || !s.presence.active() {
		t.Fatalf("still in use: %+v", got)
	}
	add("d")
	got := poll(time.Second)
	if s.presence.active() || len(got) != 1 || !strings.HasSuffix(got[0].Body, ": d") {
		t.Fatalf("only what came after the owner left must be pushed: %+v", got)
	}
	// Hiding the page ends its use at once.
	s.presence.report(pc, true)
	s.presence.report(pc, false)
	add("e")
	if got := poll(time.Minute); len(got) != 1 {
		t.Fatalf("hidden page held the push back: %+v", got)
	}
}
