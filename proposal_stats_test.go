package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	v1 "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1"
)

func TestProposalStats(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	day := 24 * time.Hour
	ep := restEndpoint("secret-host.leak.test", 443)
	ep.Path = "/leak-path/**"
	ep.Rules = []v1.L7Rule{{Allow: &v1.L7Allow{Method: "GET", Path: "/leak-rule-path"}}, {Allow: &v1.L7Allow{Method: "POST", Path: "/leak-post"}}}
	ep.DenyRules = []v1.L7DenyRule{{Method: "DELETE", Path: "/leak-deny"}}
	big := &v1.NetworkPolicyRule{Name: "big", Binaries: policyBinaries("/opt/leak-bin/a", "/opt/leak-bin/b", "/opt/leak-bin/c"), Endpoints: []v1.PolicyNetworkEndpoint{ep, l4Endpoint("other.leak.test", 22)}}
	small := &v1.NetworkPolicyRule{Name: "small", Binaries: policyBinaries("/usr/bin/leak-curl"), Endpoints: []v1.PolicyNetworkEndpoint{l4Endpoint("small.leak.test", 443)}}
	chunk := func(id, status, rule, rationale, stage string, decided time.Duration, r *v1.NetworkPolicyRule) v1.PolicyChunk {
		c := v1.PolicyChunk{ID: id, Status: status, RuleName: rule, Rationale: rationale, Stage: stage, Binary: "/usr/bin/leak-curl", ProposedRule: r, CreatedAt: now.Add(-decided - time.Hour)}
		if status != "pending" {
			c.DecidedAt = now.Add(-decided)
		}
		return c
	}
	chunks := []v1.PolicyChunk{
		chunk("a", "pending", "small", "Allow curl to connect to small.leak.test:443 (HTTPS).", "initial", 0, small),
		chunk("b", "approved", "big", "agent wants leak-rationale-text", "refined", 2*day, big),
		chunk("c", "approved", "big", "Allow curl to connect to secret-host.leak.test:443", "initial", 10*day, big),
		chunk("d", "rejected", "small", "Allow curl to connect to small.leak.test:443 (HTTPS).", "", 40*day, small),
		chunk("e", "approved", "small", "x", "initial", time.Hour, small),
	}
	chunks[1].SecurityNotes = "leak-security-note"
	chunks[1].DenialSummaryIDs = []string{"leak-denial-1", "leak-denial-2"}
	chunks[1].CandidateEffectivePolicy = &v1.SandboxPolicy{NetworkPolicies: map[string]v1.NetworkPolicyRule{"big": *big}}
	policy := map[string]v1.NetworkPolicyRule{"big": *big, "small": *small}

	st := proposalStats(chunks, policy, now)
	group := func(g []StatGroup, key string) int {
		for _, x := range g {
			if x.Key == key {
				return x.Chunks
			}
		}
		return -1
	}
	if st.Chunks != 5 || group(st.ByStatus, "approved") != 3 || group(st.ByStatus, "pending") != 1 || group(st.ByStatus, "other") != -1 {
		t.Errorf("by status: %+v", st.ByStatus)
	}
	if group(st.BySource, "auto") != 3 || group(st.BySource, "agent") != 2 || group(st.ByStage, "refined") != 1 || group(st.ByStage, "none") != 1 {
		t.Errorf("by source %+v, stage %+v", st.BySource, st.ByStage)
	}
	if group(st.Decided, "lt1d") != 1 || group(st.Decided, "1to7d") != 1 || group(st.Decided, "7to30d") != 1 || group(st.Decided, "gt30d") != 1 {
		t.Errorf("ages: %+v", st.Decided)
	}
	sum := 0
	for _, g := range st.ByStatus {
		sum += g.Bytes
	}
	if sum != st.Bytes || st.Size.Max != st.Largest[0].Bytes || st.Size.Median > st.Size.P90 || st.Size.P90 > st.Size.Max {
		t.Errorf("sizes: total %d sum %d, %+v", st.Bytes, sum, st.Size)
	}
	top := st.Largest[0]
	if top.RuleName != "big" || top.Endpoints != 2 || top.Binaries != 3 || top.AllowRules != 2 || top.DenyRules != 1 || top.DenialIDs != 2 || top.EffectivePolicy == 0 || top.Text == 0 || top.ChunkParts.Rule == 0 {
		t.Errorf("largest: %+v", top)
	}
	if st.Rules.Distinct != 2 || st.Rules.Repeated != 2 || st.Rules.Top[0].Key != "small" || st.Rules.Top[0].Chunks != 3 {
		t.Errorf("rule names: %+v", st.Rules)
	}
	if st.Policy == nil || st.Policy.Rules != 2 || st.Policy.Binaries != 4 || st.Policy.Endpoints != 3 {
		t.Errorf("policy: %+v", st.Policy)
	}
	b, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"leak.test", "leak-path", "leak-rule-path", "leak-post", "leak-deny", "leak-bin", "leak-curl", "leak-rationale", "leak-security", "leak-denial", "/opt", "/usr"} {
		if strings.Contains(string(b), leak) {
			t.Errorf("%q in %s", leak, b)
		}
	}
}
