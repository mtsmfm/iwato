package main

import (
	"context"
	"fmt"
	"maps"
	"math/bits"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"iwato/internal/termsync"
)

// notifier polls the sandboxes' proposals waiting for the owner and
// forwards new ones an agent made as Web Push messages, and tells when a
// terminal is done (an agent finished its task: see idleAfter).
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
	t := time.NewTicker(termPoll)
	defer t.Stop()
	var read time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			proposals := n.now().Sub(read) >= n.interval
			if proposals {
				read = n.now()
			}
			if n.s.push != nil && n.s.push.Count() > 0 {
				n.poll(ctx, proposals)
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

// poll pushes what is new: the terminals at every call, the proposals too
// when asked (they are read from the gateway, less often).
func (n *notifier) poll(ctx context.Context, proposals bool) {
	list := n.s.sandboxList()
	var messages []PushMessage
	if proposals {
		messages = n.gather(ctx, list)
	} else {
		for _, w := range list {
			messages = append(messages, n.watchTerms(w)...)
		}
		n.forget(list)
	}
	n.push(ctx, list, n.batch(messages))
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

// Terminals (ADR 0017, 0018). Each poll compares a terminal's screen with the
// last one, by the letters of each row (counters and spinner glyphs are no
// change). Changes within ownerWindow after a client's input are the
// owner's doing (a TUI redraws on a resize, a scroll, an attach). Rows that
// change alone again and again (timers, spinners) are volatile: no change.
// A terminal worked when it changed in workPolls polls over workSpan, and is
// done idleAfter after its last change, or at once when an agent's working
// hint goes away. A raw change (digits and glyphs too) in aliveTicks of the
// last alivePolls polls makes it alive: idleAfter counts from when that
// ticking stops. Done is pushed unless the screen is the one the owner saw
// when they last left, volatile rows aside. A bell or a notification from
// the program outside an input window is pushed at once, once per
// alertEvery.
//
// While a page is in use (presence.go) nothing is pushed: the page marks
// the terminal instead.
const (
	termPoll    = 5 * time.Second
	ownerWindow = 5 * time.Second
	workPolls   = 3
	workSpan    = 20 * time.Second
	idleAfter   = time.Minute
	alertEvery  = time.Minute
	// A clock that changes once a minute stays well below this.
	alivePolls = 4
	aliveTicks = 3
	// A row is volatile after volatileHits polls within volatileWindow
	// changed it alone.
	volatileHits   = 3
	volatileWindow = 2 * time.Minute
	// hintRows are the last rows searched for workingHints.
	hintRows = 12
	// idleLines bounds the last screen lines a push about it shows.
	idleLines = 5
)

// workingHints are what agents show near the bottom of the screen while
// they work.
var workingHints = []string{
	"esc to interrupt", // Claude Code, Codex
	"• Working (",      // Codex
}

// termWatch is what the notifier knows of one terminal.
type termWatch struct {
	lines              []string  // at the last poll (screenKeys)
	raw                []string  // at the last poll, as shown
	ticks              uint      // a bit per poll, newest lowest: the raw screen changed
	ticked, alive      time.Time // the last raw change; the last one while alive
	cols, rows         int
	seen               []string // the screen when the owner last left, or when first seen (screenKeys)
	polled             time.Time
	hits               map[int][]time.Time // row → its last small changes
	runStart, lastWork time.Time           // the current run of changes
	runPolls           int
	hinted             bool      // a working hint showed at the last poll
	stopped            time.Time // when it was done, until it changes again
	present            bool      // a page was in use at the last poll
	uses               uint64    // presence.used at the last poll
	attached           time.Time // the screen's attachment (termScreen.attached)
	alerts             uint64    // termsync.Activity.Alerts seen
	alerted            time.Time // the last alert pushed
}

type termEvent int

const (
	termNone termEvent = iota
	termDone
	termAlert
)

func newTermWatch(now time.Time, scr termScreen, present bool, uses uint64) *termWatch {
	keys := screenKeys(scr.lines)
	return &termWatch{lines: keys, raw: scr.lines, seen: keys, cols: scr.cols, rows: scr.rows, polled: now,
		present: present, uses: uses, attached: scr.attached, alerts: scr.activity.Alerts}
}

// observe takes the terminal's screen at now and tells whether it is done
// or rang (with the notification's text). present tells whether a page is
// in use, uses is presence.used().
func (t *termWatch) observe(now time.Time, scr termScreen, present bool, uses uint64) (termEvent, string) {
	// A page used since the last poll and closed again (a look at a push
	// between two polls) is a leave too.
	left := (t.present || t.uses != uses) && !present
	t.present, t.uses = present, uses
	in := scr.activity.Input
	owner := !in.IsZero() && in.After(t.polled.Add(-ownerWindow))
	t.polled = now
	alert, text := t.takeAlerts(scr.activity)
	done := t.compare(now, scr, left, owner)
	switch {
	case alert:
		t.runPolls, t.stopped = 0, now
		if now.Sub(t.alerted) >= alertEvery {
			t.alerted = now
			return termAlert, text
		}
	case done:
		t.stopped = now
		return termDone, ""
	}
	return termNone, ""
}

func (t *termWatch) takeAlerts(a termsync.Activity) (bool, string) {
	n := 0
	if a.Alerts > t.alerts {
		n = int(min(a.Alerts-t.alerts, uint64(len(a.Recent))))
	}
	t.alerts = a.Alerts
	found, text := false, ""
	for _, al := range a.Recent[len(a.Recent)-n:] {
		if !al.Input.IsZero() && al.At.Sub(al.Input) < ownerWindow {
			continue
		}
		found = true
		if al.Text != "" {
			text = al.Text
		}
	}
	return found, text
}

// compare takes the screen and tells whether the terminal is done.
func (t *termWatch) compare(now time.Time, scr termScreen, left, owner bool) bool {
	prev, prevRaw := t.lines, t.raw
	t.lines, t.raw = screenKeys(scr.lines), scr.lines
	hinted := t.hinted
	t.hinted = workingHint(scr.lines)
	if left {
		if !slices.Equal(prev, t.lines) {
			t.stopped = time.Time{}
		}
		t.seen, t.runPolls = t.lines, 0
		return false
	}
	if scr.cols != t.cols || scr.rows != t.rows {
		t.cols, t.rows, t.hits = scr.cols, scr.rows, nil
		return false
	}
	t.ticks <<= 1
	if !owner && !slices.Equal(prevRaw, scr.lines) {
		t.ticks |= 1
		t.ticked = now
	}
	if bits.OnesCount(t.ticks&(1<<alivePolls-1)) >= aliveTicks {
		t.alive = t.ticked
	}
	work := t.hinted && !owner
	if !owner {
		var changed []int
		for y, l := range t.lines {
			if y >= len(prev) || prev[y] != l {
				changed = append(changed, y)
			}
		}
		for _, y := range changed {
			if len(changed) == 1 {
				t.hit(y, now)
			}
			if !t.volatile(y, now) {
				work = true
			}
		}
	}
	if work {
		if t.runPolls == 0 || now.Sub(t.lastWork) >= idleAfter {
			t.runStart, t.runPolls = now, 0
		}
		t.runPolls++
		t.lastWork, t.stopped = now, time.Time{}
	}
	worked := t.runPolls >= workPolls && t.lastWork.Sub(t.runStart) >= workSpan
	gone := hinted && !owner // the working hint went away by itself
	if !worked || t.hinted || !gone && (now.Sub(t.lastWork) < idleAfter || now.Sub(t.alive) < idleAfter) {
		return false
	}
	t.runPolls = 0
	return !t.asSeen(now)
}

func (t *termWatch) hit(y int, now time.Time) {
	if t.hits == nil {
		t.hits = map[int][]time.Time{}
	}
	h := append(t.hits[y], now)
	if len(h) > volatileHits {
		h = h[len(h)-volatileHits:]
	}
	t.hits[y] = h
}

func (t *termWatch) volatile(y int, now time.Time) bool {
	h := t.hits[y]
	return len(h) >= volatileHits && now.Sub(h[0]) < volatileWindow
}

// asSeen tells whether the screen is the one the owner saw, volatile rows
// aside.
func (t *termWatch) asSeen(now time.Time) bool {
	if len(t.seen) != len(t.lines) {
		return false
	}
	for y := range t.lines {
		if t.lines[y] != t.seen[y] && !t.volatile(y, now) {
			return false
		}
	}
	return true
}

func screenKeys(lines []string) []string {
	keys := make([]string, len(lines))
	for y, l := range lines {
		keys[y] = strings.Map(func(r rune) rune {
			if unicode.IsLetter(r) {
				return r
			}
			return -1
		}, l)
	}
	return keys
}

func workingHint(lines []string) bool {
	for _, l := range lines[max(0, len(lines)-hintRows):] {
		for _, h := range workingHints {
			if strings.Contains(l, h) {
				return true
			}
		}
	}
	return false
}

// watchTerms returns a message for each of the sandbox's terminals that is
// done or rang (none while a page is in use: the page marks them).
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
			// lost connection): what is on the screen already, and the
			// replayed bells, are no news. A stop stays marked until the
			// screen changes.
			fresh := newTermWatch(now, scr, present, uses)
			if t != nil {
				fresh.stopped = t.stopped
			}
			t = fresh
		} else if ev, text := t.observe(now, scr, present, uses); ev != termNone && !present {
			out = append(out, termMessage(w.Name, name, scr.lines, ev, text))
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

// termMessage tells that a terminal is done (or rang: alert, with the
// notification's text), with the last lines of its screen that say
// something (not the borders and blank lines an agent's input box draws). A
// newer one about the terminal replaces it on the device.
func termMessage(sandbox, term string, lines []string, ev termEvent, text string) PushMessage {
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
	what := "止まりました"
	if ev == termAlert {
		what = "通知"
	}
	summary := what
	if term != mainTerm {
		summary = "(" + term + ") " + summary
	}
	body := strings.Join(tail, "\n")
	if text = strings.TrimSpace(text); text != "" {
		body, tail = text, strings.Split(text, "\n")
	}
	if len(tail) > 0 {
		summary += ": " + truncate(tail[len(tail)-1], 60)
	}
	body = truncate(body, 300)
	if body == "" {
		body = "画面が変わらなくなりました"
		if ev == termAlert {
			body = "ベルが鳴りました"
		}
	}
	return PushMessage{Title: label + ": " + what, Body: body, Tag: "idle-" + sandbox + "-" + term,
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
