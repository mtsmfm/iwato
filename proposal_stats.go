package main

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"sort"
	"time"

	v1 "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1"
)

// ProposalStats is the size of a sandbox's draft (every proposal OpenShell
// ever drafted, decided ones included: the gateway never deletes those) in
// numbers alone, to see what makes it large. It names rules, never hosts,
// paths, programs or rationales. Sizes are each chunk marshalled as JSON
// (the SDK gives no protobuf message to measure), so they track the wire
// size without matching it.
type ProposalStats struct {
	Chunks   int             `json:"chunks"`
	Bytes    int             `json:"bytes"`
	Parts    ChunkParts      `json:"parts"`
	ByStatus []StatGroup     `json:"byStatus"`
	BySource []StatGroup     `json:"bySource"`
	ByStage  []StatGroup     `json:"byStage"`
	Decided  []StatGroup     `json:"decidedAge"`
	Size     SizeSpread      `json:"size"`
	Largest  []ChunkStat     `json:"largest"`
	Rules    RuleNameStats   `json:"ruleNames"`
	Policy   *PolicySizeStat `json:"policy,omitempty"`
}

type StatGroup struct {
	Key    string `json:"key"`
	Chunks int    `json:"chunks"`
	Bytes  int    `json:"bytes"`
}

// ChunkParts is where a chunk's bytes go: its proposed rule, its texts
// (rationale and security notes), its denial summary IDs, and the whole
// effective policies (current and candidate) it may carry for review.
type ChunkParts struct {
	Rule            int `json:"rule"`
	Text            int `json:"text"`
	Denials         int `json:"denials"`
	EffectivePolicy int `json:"effectivePolicy"`
}

type SizeSpread struct {
	Median int `json:"median"`
	P90    int `json:"p90"`
	Max    int `json:"max"`
}

type ChunkStat struct {
	Status    string `json:"status"`
	Source    string `json:"source"`
	Stage     string `json:"stage"`
	RuleName  string `json:"ruleName"`
	CreatedAt int64  `json:"createdAt"`
	DecidedAt int64  `json:"decidedAt"`
	Bytes     int    `json:"bytes"`
	ChunkParts
	Endpoints  int `json:"endpoints"`
	Binaries   int `json:"binaries"`
	AllowRules int `json:"allowRules"`
	DenyRules  int `json:"denyRules"`
	DenialIDs  int `json:"denialIds"`
}

// RuleNameStats: how many rule names the chunks have, how many of them
// more than one chunk, and the names with the most chunks.
type RuleNameStats struct {
	Distinct int         `json:"distinct"`
	Repeated int         `json:"repeated"`
	Top      []StatGroup `json:"top"`
}

type PolicySizeStat struct {
	Rules     int `json:"rules"`
	Endpoints int `json:"endpoints"`
	Binaries  int `json:"binaries"`
	Bytes     int `json:"bytes"`
}

const (
	statsTop     = 10
	statsNameMax = 128
)

var decidedAges = []struct {
	key string
	max time.Duration
}{{"lt1d", 24 * time.Hour}, {"1to7d", 7 * 24 * time.Hour}, {"7to30d", 30 * 24 * time.Hour}, {"gt30d", 0}}

func jsonSize(v any) int {
	b, err := json.Marshal(v)
	if err != nil {
		return 0
	}
	return len(b)
}

// oneOf keeps a gateway's word among the known ones.
func oneOf(s string, known ...string) string {
	if s == "" {
		return "none"
	}
	if slices.Contains(known, s) {
		return s
	}
	return "other"
}

func chunkSource(c v1.PolicyChunk) string {
	if mechanisticRationale.MatchString(c.Rationale) {
		return "auto"
	}
	return "agent"
}

func chunkStat(c v1.PolicyChunk) ChunkStat {
	s := ChunkStat{Status: oneOf(c.Status, "pending", "approved", "rejected"), Source: chunkSource(c), Stage: oneOf(c.Stage, "initial", "refined"),
		RuleName: truncateRunes(c.RuleName, statsNameMax), CreatedAt: unixOrZero(c.CreatedAt), DecidedAt: unixOrZero(c.DecidedAt), Bytes: jsonSize(c), DenialIDs: len(c.DenialSummaryIDs)}
	s.Text = len(c.Rationale) + len(c.SecurityNotes)
	if len(c.DenialSummaryIDs) > 0 {
		s.Denials = jsonSize(c.DenialSummaryIDs)
	}
	if c.CurrentEffectivePolicy != nil {
		s.EffectivePolicy += jsonSize(c.CurrentEffectivePolicy)
	}
	if c.CandidateEffectivePolicy != nil {
		s.EffectivePolicy += jsonSize(c.CandidateEffectivePolicy)
	}
	if r := c.ProposedRule; r != nil {
		s.ChunkParts.Rule = jsonSize(r)
		s.Endpoints, s.Binaries = len(r.Endpoints), len(r.Binaries)
		for _, e := range r.Endpoints {
			s.AllowRules += len(e.Rules)
			s.DenyRules += len(e.DenyRules)
		}
	}
	return s
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// proposalStats aggregates the chunks; policy may be nil.
func proposalStats(chunks []v1.PolicyChunk, policy map[string]v1.NetworkPolicyRule, now time.Time) ProposalStats {
	st := ProposalStats{Largest: []ChunkStat{}, Rules: RuleNameStats{Top: []StatGroup{}}}
	groups := func(keys ...string) []StatGroup {
		g := make([]StatGroup, len(keys))
		for i, k := range keys {
			g[i].Key = k
		}
		return g
	}
	st.ByStatus = groups("pending", "approved", "rejected", "other")
	st.BySource = groups("auto", "agent")
	st.ByStage = groups("initial", "refined", "none", "other")
	st.Decided = groups()
	for _, a := range decidedAges {
		st.Decided = append(st.Decided, StatGroup{Key: a.key})
	}
	add := func(g []StatGroup, key string, n int) {
		for i := range g {
			if g[i].Key == key {
				g[i].Chunks++
				g[i].Bytes += n
				return
			}
		}
	}
	sizes := make([]int, 0, len(chunks))
	all := make([]ChunkStat, 0, len(chunks))
	names := map[string]*StatGroup{}
	for _, c := range chunks {
		s := chunkStat(c)
		all = append(all, s)
		sizes = append(sizes, s.Bytes)
		st.Chunks++
		st.Bytes += s.Bytes
		st.Parts.Rule += s.ChunkParts.Rule
		st.Parts.Text += s.Text
		st.Parts.Denials += s.Denials
		st.Parts.EffectivePolicy += s.EffectivePolicy
		add(st.ByStatus, s.Status, s.Bytes)
		add(st.BySource, s.Source, s.Bytes)
		add(st.ByStage, s.Stage, s.Bytes)
		if c.Status != "pending" {
			at := c.DecidedAt
			if at.IsZero() {
				at = c.CreatedAt
			}
			age := now.Sub(at)
			for _, a := range decidedAges {
				if a.max == 0 || age < a.max {
					add(st.Decided, a.key, s.Bytes)
					break
				}
			}
		}
		g := names[s.RuleName]
		if g == nil {
			g = &StatGroup{Key: s.RuleName}
			names[s.RuleName] = g
		}
		g.Chunks++
		g.Bytes += s.Bytes
	}
	st.ByStatus = slices.DeleteFunc(st.ByStatus, func(g StatGroup) bool { return g.Key == "other" && g.Chunks == 0 })
	st.ByStage = slices.DeleteFunc(st.ByStage, func(g StatGroup) bool { return g.Key == "other" && g.Chunks == 0 })
	if len(sizes) > 0 {
		sort.Ints(sizes)
		st.Size = SizeSpread{Median: sizes[len(sizes)/2], P90: sizes[min(len(sizes)-1, len(sizes)*9/10)], Max: sizes[len(sizes)-1]}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].Bytes > all[j].Bytes })
	st.Largest = append(st.Largest, all[:min(statsTop, len(all))]...)
	st.Rules.Distinct = len(names)
	for _, g := range names {
		if g.Chunks > 1 {
			st.Rules.Repeated++
			st.Rules.Top = append(st.Rules.Top, *g)
		}
	}
	sort.Slice(st.Rules.Top, func(i, j int) bool {
		a, b := st.Rules.Top[i], st.Rules.Top[j]
		if a.Chunks != b.Chunks {
			return a.Chunks > b.Chunks
		}
		if a.Bytes != b.Bytes {
			return a.Bytes > b.Bytes
		}
		return a.Key < b.Key
	})
	st.Rules.Top = st.Rules.Top[:min(statsTop, len(st.Rules.Top))]
	if policy != nil {
		p := &PolicySizeStat{Rules: len(policy), Bytes: jsonSize(policy)}
		for _, r := range policy {
			p.Endpoints += len(r.Endpoints)
			p.Binaries += len(r.Binaries)
		}
		st.Policy = p
	}
	return st
}

// ProposalStats reads every chunk of the sandbox's draft and the latest
// policy revision's rules.
func (o *OpenShell) ProposalStats(ctx context.Context, w Sandbox) (ProposalStats, error) {
	pol, err := o.policy(ctx)
	if err != nil {
		return ProposalStats{}, err
	}
	chunks, err := o.draftChunks(ctx, pol, w.Name)
	if notFound(err) {
		return ProposalStats{}, errCode("sandbox_missing", nil, "サンドボックスがありません")
	}
	if err != nil {
		return ProposalStats{}, err
	}
	var rules map[string]v1.NetworkPolicyRule
	if st, err := o.latestPolicy(ctx, w); err == nil && st.Revision.Policy != nil {
		rules = st.Revision.Policy.NetworkPolicies
		if rules == nil {
			rules = map[string]v1.NetworkPolicyRule{}
		}
	}
	return proposalStats(chunks, rules, time.Now()), nil
}

type proposalStatsRuntime interface {
	ProposalStats(context.Context, Sandbox) (ProposalStats, error)
}

// handleProposalStats answers GET /api/sandboxes/NAME/proposals/stats.
func (s *Server) handleProposalStats(w http.ResponseWriter, r *http.Request, parts []string) {
	sb, ok := s.sandboxFor(w, parts, 3)
	if !ok || !s.require(w, r, false) {
		return
	}
	rt, ok := s.rt.(proposalStatsRuntime)
	if !ok {
		fail(w, 409, "proposals unavailable")
		return
	}
	ctx, cancel := timeout(r.Context(), 30*time.Second)
	defer cancel()
	st, err := rt.ProposalStats(ctx, sb)
	if err != nil {
		s.failGateway(ctx, w, sb, err)
		return
	}
	jsonResponse(w, 200, st)
}
