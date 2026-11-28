package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The subscriptions' usage (docs/adr/0026): Claude's, Codex's and Gemini's
// (agy's) windows (5 hours, the week), read inside a running sandbox the
// provider is attached to, with one exec of curl. The token is the provider's
// placeholder in that sandbox; the sandbox's proxy puts the gateway's
// credential in on the way out, so Iwato never holds it.

// usageWindow is one limit window: how much of it is used and when it
// resets.
type usageWindow struct {
	// Kind is 5h, 7d, 7d-opus, 7d-sonnet, 5h-model or 7d-model (Model
	// names the model or group; others: the window's hours, 12h).
	Kind     string    `json:"kind"`
	Model    string    `json:"model,omitempty"`
	Used     float64   `json:"used"` // percent, 0-100
	ResetsAt time.Time `json:"resetsAt,omitzero"`
}

// usageReport is a service's last reading.
type usageReport struct {
	Service string        `json:"service"`
	Windows []usageWindow `json:"windows"`
	// At is when Windows were read (zero: never); Sandbox where.
	At      time.Time `json:"at,omitzero"`
	Sandbox string    `json:"sandbox,omitempty"`
	// Code says why the last try failed (the windows are older then):
	// usage_no_sandbox, usage_no_token, usage_http, usage_failed.
	Code   string         `json:"code,omitempty"`
	Params map[string]any `json:"params,omitempty"`
}

// usageService reads one subscription.
type usageService struct {
	id      string
	profile string // the provider profile whose placeholder the script uses
	script  string
	parse   func(body []byte, now time.Time) ([]usageWindow, error)
}

// The scripts print the body, then the HTTP status on a line of its own,
// or usageNoToken when the provider's variable is missing.
const usageNoToken = "iwato-no-token"

var usageServices = []usageService{
	{id: "claude", profile: claudeSubscriptionProfileID, parse: parseClaudeUsage, script: `[ -n "${` + claudeOAuthCredentialKey + `-}" ] || { echo ` + usageNoToken + `; exit 0; }
exec curl -sS --max-time 20 -w '\n%{http_code}' -H "Authorization: Bearer $` + claudeOAuthCredentialKey + `" -H 'anthropic-beta: oauth-2025-04-20' https://api.anthropic.com/api/oauth/usage`},
	{id: "codex", profile: codexSubscriptionProfileID, parse: parseCodexUsage, script: `[ -n "${` + codexOAuthCredentialKey + `-}" ] && [ -n "${` + codexAccountKey + `-}" ] || { echo ` + usageNoToken + `; exit 0; }
exec curl -sS --max-time 20 -w '\n%{http_code}' -H "Authorization: Bearer $` + codexOAuthCredentialKey + `" -H "chatgpt-account-id: $` + codexAccountKey + `" https://chatgpt.com/backend-api/wham/usage`},
	{id: "gemini", profile: antigravityProfileID, parse: parseGeminiUsage, script: geminiUsageScript},
}

// geminiUsageScript asks agy's API (Cloud Code's v1internal) as agy does,
// whose User-Agent it needs: loadCodeAssist for the project, then
// retrieveUserQuotaSummary (the 5-hour and weekly windows of each group of
// models) and retrieveUserQuota (each model's own 5-hour window: Google AI
// Ultra's summary leaves Gemini out). It prints
// {"summary":...,"quota":...} and 200, or the answer that failed.
var geminiUsageScript = `[ -n "${` + antigravityCredentialKey + `-}" ] || { echo ` + usageNoToken + `; exit 0; }
c() { curl -sS --max-time 12 -w '\n%{http_code}' -X POST -A antigravity -H "Authorization: Bearer $` + antigravityCredentialKey + `" -H 'Content-Type: application/json' -d "$2" "https://daily-cloudcode-pa.googleapis.com/v1internal:$1"; }
ok() { [ "$(printf '%s' "$1" | tail -n 1)" = 200 ]; }
body() { printf '%s' "$1" | sed '$d'; }
a=$(c loadCodeAssist '{}'); ok "$a" || { printf '%s\n' "$a"; exit 0; }
p=$(body "$a" | tr -d '\n' | sed -n 's/.*"cloudaicompanionProject": *\({ *"id": *\)\{0,1\}"\([A-Za-z0-9._:-]*\)".*/\2/p')
b='{}'; [ -z "$p" ] || b="{\"project\":\"$p\"}"
s=$(c retrieveUserQuotaSummary "$b"); ok "$s" || { printf '%s\n' "$s"; exit 0; }
q=$(c retrieveUserQuota "$b"); ok "$q" && q=$(body "$q") || q=null
printf '{"summary":%s,"quota":%s}\n200\n' "$(body "$s")" "$q"`

// scriptRunner runs a shell script in a running sandbox as its user and
// returns what it printed (OpenShell, Fake).
type scriptRunner interface {
	RunScript(ctx context.Context, w Sandbox, script string) ([]byte, error)
}

// Readings are refreshed this often while a page is in use, else rarely
// (the tray's tooltip): the endpoints limit how often they are asked.
const (
	usageEvery     = 5 * time.Minute
	usageIdleEvery = 30 * time.Minute
)

type usageMonitor struct {
	mu      sync.Mutex
	reports map[string]usageReport
	tried   map[string]time.Time
}

func (m *usageMonitor) list() []usageReport {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []usageReport{}
	for _, svc := range usageServices {
		if r, ok := m.reports[svc.id]; ok {
			out = append(out, r)
		}
	}
	return out
}

// loop reads each service whose provider exists, every usageEvery while a
// page is in use and every usageIdleEvery otherwise.
func (s *Server) usageLoop(ctx context.Context) {
	for {
		s.readUsage(ctx, false)
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Minute):
		}
	}
}

// readUsage reads the services due (all with force).
func (s *Server) readUsage(ctx context.Context, force bool) {
	pr, ok := s.rt.(providerRuntime)
	run, ok2 := s.rt.(scriptRunner)
	if !ok || !ok2 {
		return
	}
	if _, ready := s.imagesReady(); !ready {
		return
	}
	every := usageIdleEvery
	if s.presence.active() {
		every = usageEvery
	}
	m := &s.usage
	m.mu.Lock()
	if m.reports == nil {
		m.reports, m.tried = map[string]usageReport{}, map[string]time.Time{}
	}
	due := []usageService{}
	for _, svc := range usageServices {
		if force || time.Since(m.tried[svc.id]) >= every {
			due = append(due, svc)
		}
	}
	m.mu.Unlock()
	if len(due) == 0 {
		return
	}
	lctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	providers, attached, err := pr.ListProviders(lctx)
	cancel()
	if err != nil {
		return
	}
	for _, svc := range due {
		names := []string{}
		for _, p := range providers {
			if providerBase(p) == svc.profile {
				names = append(names, p.Name)
			}
		}
		m.mu.Lock()
		m.tried[svc.id] = time.Now()
		if len(names) == 0 {
			delete(m.reports, svc.id)
			m.mu.Unlock()
			continue
		}
		m.mu.Unlock()
		r := s.readService(ctx, run, svc, names, attached)
		m.mu.Lock()
		if r.Code != "" {
			old := m.reports[svc.id]
			old.Service, old.Code, old.Params = svc.id, r.Code, r.Params
			r = old
		}
		m.reports[svc.id] = r
		m.mu.Unlock()
	}
}

// readService runs the service's script in the first running sandbox with
// one of its providers attached.
func (s *Server) readService(ctx context.Context, run scriptRunner, svc usageService, names []string, attached map[string][]string) usageReport {
	fail := func(code string, params map[string]any) usageReport {
		return usageReport{Service: svc.id, Code: code, Params: params}
	}
	for _, sb := range s.sandboxList() {
		if !slices.ContainsFunc(attached[sb.Name], func(p string) bool { return slices.Contains(names, p) }) {
			continue
		}
		sctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		running := s.rt.Status(sctx, sb).State == "running"
		cancel()
		if !running {
			continue
		}
		rctx, cancel := context.WithTimeout(ctx, 45*time.Second)
		out, err := run.RunScript(rctx, sb, svc.script)
		cancel()
		if err != nil {
			log.Printf("usage %s in %s: %v", svc.id, sb.Name, english(err))
			return fail("usage_failed", map[string]any{"sandbox": sb.Name})
		}
		body, status, err := splitUsageOutput(out)
		switch {
		case err != nil:
			log.Printf("usage %s in %s: %v", svc.id, sb.Name, err)
			return fail("usage_failed", map[string]any{"sandbox": sb.Name})
		case status == 0:
			return fail("usage_no_token", map[string]any{"sandbox": sb.Name})
		case status != http.StatusOK:
			log.Printf("usage %s in %s: HTTP %d", svc.id, sb.Name, status)
			return fail("usage_http", map[string]any{"sandbox": sb.Name, "status": strconv.Itoa(status)})
		}
		windows, err := svc.parse(body, time.Now())
		if err != nil {
			log.Printf("usage %s in %s: %v", svc.id, sb.Name, err)
			return fail("usage_failed", map[string]any{"sandbox": sb.Name})
		}
		return usageReport{Service: svc.id, Windows: windows, At: time.Now().UTC(), Sandbox: sb.Name}
	}
	return fail("usage_no_sandbox", nil)
}

// splitUsageOutput takes the body and the status curl printed after it;
// status 0 is usageNoToken.
func splitUsageOutput(out []byte) ([]byte, int, error) {
	out = bytes.TrimRight(out, "\r\n")
	if string(out) == usageNoToken {
		return nil, 0, nil
	}
	i := bytes.LastIndexByte(out, '\n')
	if i < 0 {
		return nil, 0, fmt.Errorf("unexpected output (%d bytes)", len(out))
	}
	status, err := strconv.Atoi(strings.TrimSpace(string(out[i+1:])))
	if err != nil || status < 100 || status > 599 {
		return nil, 0, fmt.Errorf("unexpected output (%d bytes)", len(out))
	}
	return out[:i], status, nil
}

// parseClaudeUsage reads api.anthropic.com/api/oauth/usage: five_hour,
// seven_day and per-model weekly windows, each {utilization, resets_at}
// (a window may be null), and limits[]'s weekly_scoped rows, the other
// models' weeks ({percent, resets_at, scope.model.display_name}; Claude
// Code's /usage reads them the same way).
func parseClaudeUsage(body []byte, _ time.Time) ([]usageWindow, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	out := []usageWindow{}
	for _, k := range []struct{ key, kind string }{{"five_hour", "5h"}, {"seven_day", "7d"}, {"seven_day_opus", "7d-opus"}, {"seven_day_sonnet", "7d-sonnet"}} {
		var w *struct {
			Utilization *float64 `json:"utilization"`
			ResetsAt    string   `json:"resets_at"`
		}
		if json.Unmarshal(raw[k.key], &w) != nil || w == nil || w.Utilization == nil {
			continue
		}
		at, _ := time.Parse(time.RFC3339, w.ResetsAt)
		out = append(out, usageWindow{Kind: k.kind, Used: clampPercent(*w.Utilization), ResetsAt: at.UTC()})
	}
	var limits []struct {
		Kind     string          `json:"kind"`
		Percent  *float64        `json:"percent"`
		ResetsAt json.RawMessage `json:"resets_at"`
		Scope    *struct {
			Model *struct {
				Name string `json:"display_name"`
			} `json:"model"`
		} `json:"scope"`
	}
	_ = json.Unmarshal(raw["limits"], &limits)
	seen := map[string]bool{}
	for _, w := range out {
		seen[strings.TrimPrefix(w.Kind, "7d-")] = true
	}
	for _, l := range limits {
		if l.Kind != "weekly_scoped" || l.Percent == nil || l.Scope == nil || l.Scope.Model == nil {
			continue
		}
		name := strings.TrimSpace(l.Scope.Model.Name)
		if name == "" || len(name) > 40 || seen[strings.ToLower(name)] {
			continue
		}
		seen[strings.ToLower(name)] = true
		out = append(out, usageWindow{Kind: "7d-model", Model: name, Used: clampPercent(*l.Percent), ResetsAt: resetTime(l.ResetsAt)})
	}
	if len(out) == 0 {
		return nil, errors.New("no usage windows in the answer")
	}
	return out, nil
}

// resetTime reads a reset given as Unix seconds or RFC 3339 (zero: neither).
func resetTime(v json.RawMessage) time.Time {
	var unix int64
	var text string
	if json.Unmarshal(v, &unix) == nil && unix > 0 {
		return time.Unix(unix, 0).UTC()
	}
	if json.Unmarshal(v, &text) == nil {
		at, _ := time.Parse(time.RFC3339, text)
		return at.UTC()
	}
	return time.Time{}
}

// parseCodexUsage reads chatgpt.com/backend-api/wham/usage: rate_limit's
// primary_window and secondary_window, each {used_percent,
// limit_window_seconds, reset_after_seconds or reset_at} (either may be
// null: weekly-only plans).
func parseCodexUsage(body []byte, now time.Time) ([]usageWindow, error) {
	type window struct {
		UsedPercent       *float64        `json:"used_percent"`
		LimitSeconds      int64           `json:"limit_window_seconds"`
		ResetAfterSeconds *int64          `json:"reset_after_seconds"`
		ResetAt           json.RawMessage `json:"reset_at"`
	}
	var v struct {
		RateLimit struct {
			Primary   *window `json:"primary_window"`
			Secondary *window `json:"secondary_window"`
		} `json:"rate_limit"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return nil, err
	}
	out := []usageWindow{}
	for _, w := range []*window{v.RateLimit.Primary, v.RateLimit.Secondary} {
		if w == nil || w.UsedPercent == nil {
			continue
		}
		u := usageWindow{Kind: windowKind(w.LimitSeconds), Used: clampPercent(*w.UsedPercent)}
		switch {
		case w.ResetAfterSeconds != nil:
			u.ResetsAt = now.Add(time.Duration(*w.ResetAfterSeconds) * time.Second).UTC()
		default:
			u.ResetsAt = resetTime(w.ResetAt)
		}
		out = append(out, u)
	}
	if len(out) == 0 {
		return nil, errors.New("no usage windows in the answer")
	}
	return out, nil
}

// parseGeminiUsage reads geminiUsageScript's answer. The summary's Gemini
// group gives the plan's 5-hour and weekly windows; without them, the most
// used Gemini model's 5-hour window of retrieveUserQuota stands in. The
// other groups (Claude and GPT models) show once used: an unused window's
// reset is only now plus its length, so it is left out.
func parseGeminiUsage(body []byte, _ time.Time) ([]usageWindow, error) {
	type bucket struct {
		Window, BucketID, ModelID string
		RemainingFraction         *float64
		ResetTime                 string
		Disabled                  bool
	}
	var v struct {
		Summary struct {
			Groups []struct {
				DisplayName string
				Buckets     []bucket
			}
		}
		Quota *struct{ Buckets []bucket }
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return nil, err
	}
	used := func(b bucket) (usageWindow, bool) {
		if b.Disabled || b.RemainingFraction == nil {
			return usageWindow{}, false
		}
		w := usageWindow{Used: clampPercent((1 - *b.RemainingFraction) * 100)}
		if w.Used > 0 {
			at, _ := time.Parse(time.RFC3339, b.ResetTime)
			w.ResetsAt = at.UTC()
		}
		return w, true
	}
	kind := func(b bucket) string {
		switch {
		case strings.EqualFold(b.Window, "5h") || strings.Contains(strings.ToLower(b.BucketID), "5h"):
			return "5h"
		case strings.EqualFold(b.Window, "weekly") || strings.Contains(strings.ToLower(b.BucketID), "weekly"):
			return "7d"
		}
		return ""
	}
	var plan, others []usageWindow
	for _, g := range v.Summary.Groups {
		gemini := strings.Contains(strings.ToLower(g.DisplayName), "gemini")
		name := "Claude/GPT"
		if !strings.Contains(strings.ToLower(g.DisplayName), "claude") {
			name = strings.TrimSpace(strings.TrimSuffix(g.DisplayName, " models"))
		}
		for _, b := range g.Buckets {
			w, ok := used(b)
			k := kind(b)
			if !ok || k == "" || name == "" || len(name) > 40 {
				continue
			}
			if gemini {
				w.Kind = k
				plan = append(plan, w)
			} else if w.Used > 0 {
				w.Kind, w.Model = k+"-model", name
				others = append(others, w)
			}
		}
	}
	if len(plan) == 0 && v.Quota != nil {
		best, found := usageWindow{Kind: "5h"}, false
		for _, b := range v.Quota.Buckets {
			w, ok := used(b)
			if !ok || !strings.HasPrefix(strings.TrimPrefix(strings.ToLower(b.ModelID), "models/"), "gemini") {
				continue
			}
			if !found || w.Used > best.Used || (w.Used == best.Used && !w.ResetsAt.IsZero() && (best.ResetsAt.IsZero() || w.ResetsAt.Before(best.ResetsAt))) {
				best.Used, best.ResetsAt, found = w.Used, w.ResetsAt, true
			}
		}
		if found {
			plan = append(plan, best)
		}
	}
	out := append(plan, others...)
	if len(out) == 0 {
		return nil, errors.New("no usage windows in the answer")
	}
	return out, nil
}

func windowKind(seconds int64) string {
	switch seconds {
	case 5 * 3600:
		return "5h"
	case 7 * 24 * 3600:
		return "7d"
	}
	return strconv.FormatInt(seconds/3600, 10) + "h"
}

func clampPercent(v float64) float64 {
	if math.IsNaN(v) {
		return 0
	}
	return math.Max(0, math.Min(100, v))
}

// handleUsage: GET the readings (the page asks every minute; reading is
// the loop's).
func (s *Server) handleUsage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		fail(w, 405, "method not allowed")
		return
	}
	if !s.require(w, r, false) {
		return
	}
	jsonResponse(w, 200, map[string]any{"services": s.usage.list()})
}

// usageSummary is the readings in a line per service, for the tray's
// tooltip: "Claude 5h 62% · 7d 24%".
func (s *Server) usageSummary() string {
	var lines []string
	for _, r := range s.usage.list() {
		if len(r.Windows) == 0 {
			continue
		}
		parts := []string{}
		for _, w := range r.Windows {
			if w.Kind == "5h" || w.Kind == "7d" {
				parts = append(parts, fmt.Sprintf("%s %.0f%%", w.Kind, w.Used))
			}
		}
		name := map[string]string{"claude": "Claude", "codex": "Codex", "gemini": "Gemini"}[r.Service]
		lines = append(lines, name+" "+strings.Join(parts, " · "))
	}
	return strings.Join(lines, "\n")
}
