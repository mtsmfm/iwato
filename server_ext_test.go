package main

import (
	"context"
	"encoding/json"

	"strings"
	"testing"
	"time"
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

// A terminal is pushed about once when its screen, after changing, stays
// the same for idleAfter; again only after it changes again. What was on
// the screen when first seen, a resize and what changed while a page was
// in use (the owner's viewing redraws it) are no change for a push.
func TestNotifierPushesATerminalThatStopped(t *testing.T) {
	rt := &screensNotifyRuntime{}
	s := newServer(testConfig(), rt, false, "127.0.0.1:1")
	n := newNotifier(s)
	clock := time.Unix(1_000_000, 0)
	n.now = func() time.Time { return clock }
	w := s.config.Sandboxes[0]
	show := func(cols int, lines ...string) {
		rt.screens = map[string]termScreen{"2": {cols: cols, rows: 4, lines: lines}}
	}
	poll := func(d time.Duration) []PushMessage {
		clock = clock.Add(d)
		return n.collect(context.Background(), w)
	}
	show(80, "$ claude", "")
	if got := poll(0); len(got) != 0 {
		t.Fatalf("first seen: %+v", got)
	}
	if got := poll(2 * time.Minute); len(got) != 0 {
		t.Fatalf("never active: %+v", got)
	}
	show(80, "✻ Working… (3s)", "")
	poll(15 * time.Second)
	show(80, "Done: fixed the bug.", "╭──╮", "│ > │", "╰──╯")
	if got := poll(15 * time.Second); len(got) != 0 {
		t.Fatalf("still changing: %+v", got)
	}
	if got := poll(45 * time.Second); len(got) != 0 {
		t.Fatalf("too early: %+v", got)
	}
	got := poll(15 * time.Second)
	if len(got) != 1 || got[0].Title != w.Name+" (2): 止まりました" || got[0].Body != "Done: fixed the bug." || got[0].Path != "/?sandbox="+w.Name+"&term=2" {
		t.Fatalf("%+v", got)
	}
	if got := poll(2 * time.Minute); len(got) != 0 {
		t.Fatalf("pushed twice: %+v", got)
	}
	if at := n.stopped(w.Name)["2"]; at == 0 {
		t.Fatal("the page is not told the terminal stopped")
	}
	show(120, "Done: fixed the bug. (redrawn wider)")
	if got := poll(2 * time.Minute); len(got) != 0 {
		t.Fatalf("a resize made it active: %+v", got)
	}
	show(120, "next task")
	poll(15 * time.Second)
	if len(n.stopped(w.Name)) != 0 {
		t.Fatal("a terminal that changed again is still marked stopped")
	}
	if got := poll(time.Minute); len(got) != 1 {
		t.Fatalf("active again, then stopped: %+v", got)
	}

	// While a page is in use, changes are not pushed about: the owner
	// views the stopped terminal (which redraws it), then closes the page
	// within idleAfter. The screen at that moment is the baseline.
	s.presence.now = n.now
	page := strings.Repeat("a", 32)
	s.presence.report(page, true)
	show(120, "next task (redrawn on view)")
	if got := poll(15 * time.Second); len(got) != 0 {
		t.Fatalf("a change while in use: %+v", got)
	}
	s.presence.report(page, false)
	poll(15 * time.Second)
	if got := poll(2 * time.Minute); len(got) != 0 || len(n.stopped(w.Name)) != 0 {
		t.Fatalf("pushed or marked after the owner left with no change since: %+v %v", got, n.stopped(w.Name))
	}
	// A stop while the owner stays on the page is marked there, not pushed,
	// and not pushed after they leave either.
	s.presence.report(page, true)
	show(120, "work while the owner watches")
	poll(15 * time.Second)
	if got := poll(time.Minute); len(got) != 0 || len(n.stopped(w.Name)) != 1 {
		t.Fatalf("a stop while in use must be marked, not pushed: %+v %v", got, n.stopped(w.Name))
	}
	s.presence.report(page, false)
	if got := poll(2 * time.Minute); len(got) != 0 {
		t.Fatalf("pushed after leaving about a stop while in use: %+v", got)
	}
	// A change after the owner left, then idleAfter without one: one push.
	show(120, "the agent went on")
	poll(15 * time.Second)
	if got := poll(time.Minute); len(got) != 1 {
		t.Fatalf("a change after leaving, then stopped: %+v", got)
	}
	if got := poll(2 * time.Minute); len(got) != 0 {
		t.Fatalf("pushed twice: %+v", got)
	}
	// A look between two polls (tap the push, view, close): no poll sees
	// the page in use, but its redraw is no change either.
	s.presence.report(page, true)
	show(120, "the agent went on (redrawn on view)")
	s.presence.report(page, false)
	poll(15 * time.Second)
	if got := poll(2 * time.Minute); len(got) != 0 || len(n.stopped(w.Name)) != 0 {
		t.Fatalf("a look between polls made it active: %+v %v", got, n.stopped(w.Name))
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
