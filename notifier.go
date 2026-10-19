package main

import (
	"context"
	"fmt"
	"hash/fnv"
	"maps"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
)

// notifier polls the sandboxes' proposals waiting for the owner and
// forwards new ones an agent made as Web Push messages, and tells when a
// terminal stopped changing (an agent finished its task: see idleAfter).
// It pushes only while at least one device is subscribed and no page is
// open and in use (presence.go); without subscriptions it still watches the
// terminals, for the marks on the page (stopped).
//
// A push goes out at once after a quiet spell; what comes within window of
// it is held and goes out as one push when the window is over, so a burst
// of proposals is one notification.
type notifier struct {
	s        *Server
	interval time.Duration
	window   time.Duration
	now      func() time.Time
	mu       sync.Mutex
	seen     map[string]map[string]bool
	pending  map[string]int                   // agent proposals waiting per sandbox, as last read
	held     []PushMessage                    // new messages not pushed yet (poll only)
	sent     time.Time                        // when the last push went out (poll only)
	terms    map[string]map[string]*termWatch // sandbox → terminal
}

func newNotifier(s *Server) *notifier {
	return &notifier{s: s, interval: 15 * time.Second, window: time.Minute, now: time.Now, seen: map[string]map[string]bool{}, pending: map[string]int{}, terms: map[string]map[string]*termWatch{}}
}

func (n *notifier) run(ctx context.Context) {
	t := time.NewTicker(n.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if n.s.push != nil && n.s.push.Count() > 0 {
				n.poll(ctx)
			} else {
				list := n.s.sandboxList()
				for _, w := range list {
					n.watchTerms(w)
				}
				n.forget(list)
			}
		}
	}
}

func (n *notifier) poll(ctx context.Context) {
	list := n.s.sandboxList()
	n.push(ctx, list, n.batch(n.gather(ctx, list)))
}

// batch adds the new messages to those held and returns what to push now:
// nothing within window of the last push, else one message for all held.
// While a page is open and in use, the owner sees there what is new: the
// messages are dropped, held ones too, and count as delivered.
func (n *notifier) batch(messages []PushMessage) []PushMessage {
	if n.s.presence.active() {
		n.held = nil
		return nil
	}
	n.held = append(n.held, messages...)
	if len(n.held) == 0 || n.now().Sub(n.sent) < n.window {
		return nil
	}
	held := n.held
	n.held = nil
	n.sent = n.now()
	return []PushMessage{merge(held)}
}

// merge makes one message of several: the counts, and a line per message.
// A click opens what the first one opens.
func merge(messages []PushMessage) PushMessage {
	if len(messages) == 1 {
		return messages[0]
	}
	first := messages[0]
	var sandboxes, lines []string
	for _, m := range messages {
		if !slices.Contains(sandboxes, m.sandbox) {
			sandboxes = append(sandboxes, m.sandbox)
		}
	}
	for _, m := range messages {
		line := "・" + m.summary
		if len(sandboxes) > 1 {
			line = "・" + m.sandbox + ": " + m.summary
		}
		lines = append(lines, line)
	}
	// The push service refuses payloads over 3000 bytes and sw.js shows
	// 300 characters.
	if len(lines) > 5 {
		lines = append(lines[:5], fmt.Sprintf("…ほか %d 件", len(lines)-5))
	}
	m := PushMessage{Body: truncate(strings.Join(lines, "\n"), 300), Tag: first.Tag, Path: first.Path, sandbox: first.sandbox}
	idle := 0
	for _, m := range messages {
		if m.idle {
			idle++
		}
	}
	var counts []string
	if n := len(messages) - idle; n > 0 {
		counts = append(counts, fmt.Sprintf("承認待ち %d 件", n))
	}
	if idle > 0 {
		counts = append(counts, fmt.Sprintf("止まったターミナル %d 件", idle))
	}
	m.Title = strings.Join(counts, "、")
	if len(sandboxes) == 1 {
		m.Title = first.sandbox + ": " + m.Title
	} else {
		m.sandbox = ""
	}
	return m
}

// gather collects the new messages of every sandbox.
func (n *notifier) gather(ctx context.Context, list []Sandbox) []PushMessage {
	var messages []PushMessage
	for _, w := range list {
		messages = append(messages, n.collect(ctx, w)...)
	}
	n.forget(list)
	return messages
}

// forget drops what it knows of the sandboxes that are gone.
func (n *notifier) forget(list []Sandbox) {
	n.mu.Lock()
	maps.DeleteFunc(n.seen, func(name string, _ map[string]bool) bool { return !listed(list, name) })
	maps.DeleteFunc(n.pending, func(name string, _ int) bool { return !listed(list, name) })
	maps.DeleteFunc(n.terms, func(name string, _ map[string]*termWatch) bool { return !listed(list, name) })
	n.mu.Unlock()
}

func listed(list []Sandbox, name string) bool {
	return slices.ContainsFunc(list, func(w Sandbox) bool { return w.Name == name })
}

func (n *notifier) push(ctx context.Context, list []Sandbox, messages []PushMessage) {
	// Every message carries the current total, so the badge of an app that
	// is not running follows whenever a notification arrives.
	n.mu.Lock()
	badge := 0
	for _, w := range list {
		badge += n.pending[w.Name]
	}
	n.mu.Unlock()
	for _, m := range messages {
		m.Local = n.s.origin
		m.Badge = &badge
		sendCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		_ = n.s.push.Send(sendCtx, m)
		cancel()
	}
}

// pendingRuntime lists only the proposals waiting for the owner, which is
// all the notifier needs (Proposals also reads the policy and the logs).
type pendingRuntime interface {
	PendingProposals(context.Context, Sandbox) ([]PendingProposal, error)
}

// readPending reads the sandbox's pending proposals; ok is false when the
// runtime has none.
func (n *notifier) readPending(ctx context.Context, w Sandbox) (pending []PendingProposal, ok bool, err error) {
	c, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if rt, ok := n.s.rt.(pendingRuntime); ok {
		p, err := rt.PendingProposals(c, w)
		return p, true, err
	}
	if rt, ok := n.s.rt.(proposalRuntime); ok {
		v, err := rt.Proposals(c, w)
		return v.Pending, true, err
	}
	return nil, false, nil
}

// collect returns messages not yet delivered for one sandbox: each
// pending approval an agent proposed is delivered once. Those OpenShell
// drafted from blocked connections are not pushed (the policy tab lists
// them).
func (n *notifier) collect(ctx context.Context, w Sandbox) []PushMessage {
	current := map[string]bool{}
	var out []PushMessage
	n.mu.Lock()
	seen := n.seen[w.Name]
	n.mu.Unlock()
	if seen == nil {
		seen = map[string]bool{}
	}
	add := func(key string, m PushMessage) {
		current[key] = true
		m.sandbox = w.Name
		if !seen[key] {
			out = append(out, m)
		}
	}
	if pending, ok, e := n.readPending(ctx, w); ok {
		if e != nil {
			keep(current, seen, "proposal:")
		} else {
			// The badge counts what is pushed: the agent's proposals, not
			// the ones OpenShell drafts from blocked connections (as the
			// page's badges do, web/app.js agentPending).
			agent := 0
			for _, p := range pending {
				if p.State == "pending-approval" && p.Source == "agent" {
					agent++
					add("proposal:"+p.ID, PushMessage{Title: w.Name + ": 承認待ち", Body: fmt.Sprintf("ポリシーの提案が承認を待っています: %s", p.Summary), Tag: "approval-" + p.ID, Path: openPath(w.Name, "tab", "policy"), summary: p.Summary})
				}
			}
			n.mu.Lock()
			n.pending[w.Name] = agent
			n.mu.Unlock()
		}
	}
	// Keep only keys still present upstream, so the set stays bounded.
	n.mu.Lock()
	n.seen[w.Name] = current
	n.mu.Unlock()
	return append(out, n.watchTerms(w)...)
}

// A terminal is active once its screen has changed (output that left the
// screen as it was, a blinking cursor or a resize does not count); an
// active terminal whose screen has not changed for idleAfter has stopped,
// which is pushed once: an agent at work keeps its screen changing (Claude
// Code redraws a spinner and the elapsed time), so it finished its task or
// waits for the owner. The screens are compared at each poll.
//
// What changes while a page is in use (presence.go) is no news for a push:
// the owner sees it, and viewing a terminal redraws it (a resize, a focus
// report, an agent repainting its screen). Such a change still marks the
// terminal once it stops (the page's mark is what tells the owner, who is
// using the page, that an agent in another terminal finished), but nothing
// is pushed for it; and when the owner leaves (no page is in use any more),
// the screen of that moment is the new baseline: a terminal is active, and
// pushed about once it stops, only if it changes after that.
const (
	idleAfter = time.Minute
	// idleLines bounds the last screen lines a push about it shows.
	idleLines = 5
)

// termWatch is what the notifier knows of one terminal (poll only).
type termWatch struct {
	sum        uint64 // of the screen's text
	cols, rows int
	changed    time.Time // when a poll saw the screen change
	active     bool      // changed since it was pushed about (or first seen)
	stopped    time.Time // when it stopped, until it changes again
	present    bool      // a page was in use at the last poll
	uses       uint64    // presence.used at the last poll
	attached   time.Time // the screen's attachment (termScreen.attached)
}

// observe takes the terminal's screen at now, with present telling whether
// a page is in use and uses the count of presence reports of use, and
// reports whether it has just stopped.
func (t *termWatch) observe(now time.Time, scr termScreen, present bool, uses uint64) bool {
	sum := screenSum(scr)
	// A page used since the last poll and closed again (a look at a push
	// between two polls) is a leave too.
	left := (t.present || t.uses != uses) && !present
	t.present, t.uses = present, uses
	switch {
	case left:
		// The owner just left: what they saw and what their viewing redrew
		// is no change; only what comes after is.
		if sum != t.sum {
			t.stopped = time.Time{}
		}
		t.sum, t.cols, t.rows, t.active = sum, scr.cols, scr.rows, false
	case scr.cols != t.cols || scr.rows != t.rows:
		// A resize redraws the screen; it is not the program's doing.
		t.sum, t.cols, t.rows = sum, scr.cols, scr.rows
	case sum != t.sum:
		t.sum, t.changed, t.active, t.stopped = sum, now, true, time.Time{}
	case t.active && now.Sub(t.changed) >= idleAfter:
		t.active, t.stopped = false, now
		return true
	}
	return false
}

func screenSum(scr termScreen) uint64 {
	h := fnv.New64a()
	for _, l := range scr.lines {
		h.Write([]byte(l))
		h.Write([]byte{'\n'})
	}
	return h.Sum64()
}

// watchTerms returns a message for each of the sandbox's terminals that
// has just stopped (none while a page is in use: the page marks them).
// Terminals that ended are forgotten.
func (n *notifier) watchTerms(w Sandbox) []PushMessage {
	rt, ok := n.s.rt.(screensRuntime)
	if !ok {
		return nil
	}
	screens := rt.TerminalScreens(w)
	now := n.now()
	uses := n.s.presence.used() // before active: a use in between shows in one of them
	present := n.s.presence.active()
	n.mu.Lock()
	defer n.mu.Unlock()
	old := n.terms[w.Name]
	current := map[string]*termWatch{}
	var out []PushMessage
	for _, name := range slices.Sorted(maps.Keys(screens)) {
		scr := screens[name]
		t := old[name]
		if t == nil || !t.attached.Equal(scr.attached) {
			// First seen, or attached again (after an update of Iwato, a
			// lost connection): what is on the screen already is no
			// change. A stop stays marked until the screen changes.
			fresh := &termWatch{sum: screenSum(scr), cols: scr.cols, rows: scr.rows, present: present, uses: uses, attached: scr.attached}
			if t != nil {
				fresh.stopped = t.stopped
			}
			t = fresh
		} else if t.observe(now, scr, present, uses) && !present {
			out = append(out, idleMessage(w.Name, name, scr.lines))
		}
		current[name] = t
	}
	if len(current) == 0 {
		delete(n.terms, w.Name)
	} else {
		n.terms[w.Name] = current
	}
	return out
}

// stopped returns, for each of the sandbox's terminals that stopped and has
// not changed since, when it stopped (the page marks it until the owner
// views it). n may be nil (no Web Push).
func (n *notifier) stopped(sandbox string) map[string]int64 {
	if n == nil {
		return nil
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	var out map[string]int64
	for name, t := range n.terms[sandbox] {
		if !t.stopped.IsZero() {
			if out == nil {
				out = map[string]int64{}
			}
			out[name] = t.stopped.Unix()
		}
	}
	return out
}

// idleMessage tells that a terminal stopped, with the last lines of its
// screen that say something (not the borders and blank lines an agent's
// input box draws). A newer one about the terminal replaces it on the device.
func idleMessage(sandbox, term string, lines []string) PushMessage {
	var tail []string
	for i := len(lines) - 1; i >= 0 && len(tail) < idleLines; i-- {
		if l := strings.TrimSpace(lines[i]); strings.IndexFunc(l, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) >= 0 {
			tail = append(tail, l)
		}
	}
	slices.Reverse(tail)
	label := sandbox
	if term != mainTerm {
		label += " (" + term + ")"
	}
	summary := "止まりました"
	if term != mainTerm {
		summary = "(" + term + ") " + summary
	}
	if len(tail) > 0 {
		summary += ": " + truncate(tail[len(tail)-1], 60)
	}
	body := truncate(strings.Join(tail, "\n"), 300)
	if body == "" {
		body = "画面が変わらなくなりました"
	}
	return PushMessage{Title: label + ": 止まりました", Body: body, Tag: "idle-" + sandbox + "-" + term,
		Path: openPath(sandbox, "term", term), sandbox: sandbox, summary: summary, idle: true}
}

// openPath is the page a click on a sandbox's notification opens: the
// sandbox with a terminal session or tab shown (key "term" or "tab").
func openPath(sandbox, key, value string) string {
	q := url.Values{"sandbox": {sandbox}}
	if value != "" {
		q.Set(key, value)
	}
	return "/?" + q.Encode()
}

// keep carries delivered keys over a failed read, so a transient error does
// not cause the same approval to be pushed again.
func keep(current, seen map[string]bool, prefixes ...string) {
	for k := range seen {
		for _, p := range prefixes {
			if strings.HasPrefix(k, p) {
				current[k] = true
			}
		}
	}
}
