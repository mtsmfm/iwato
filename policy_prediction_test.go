package main

import (
	"strings"
	"testing"

	v1 "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1"
)

// The refusal from a real gateway (WSLc), as Iwato showed it.
const realAmbiguityError = "Conflict: proposal is not applicable: candidate invalid: network endpoint ambiguity validation failed:; network policies '_provider_github' endpoint[0] (api.github.com:443) and 'allow_api_github_com_443' endpoint[0] (api.github.com:443) overlap on port(s) 443 with conflicting metadata: enforcement=1 vs 2"

func TestTranslateGatewayError(t *testing.T) {
	got, ok := translateGatewayError(realAmbiguityError, map[string]RuleProvider{"_provider_github": {Name: "github", Label: "GitHub"}})
	want := "api.github.com:443 で プロバイダー github (GitHub) の規則 _provider_github と 規則 allow_api_github_com_443 の設定が食い違います (検査のモード: _provider_github は 遮断 (enforce)、allow_api_github_com_443 は 監査 (audit))"
	if !ok || !strings.HasPrefix(got.text, "OpenShell が拒否しました: ") || !strings.Contains(got.text, want) {
		t.Fatal(got)
	}
	// The pair as the page reads it.
	if p := got.pairs; len(p) != 1 || p[0].Rule != "_provider_github" || p[0].Provider == nil || p[0].Provider.Name != "github" || p[0].With != "allow_api_github_com_443" ||
		p[0].Endpoint != "api.github.com:443" || len(p[0].Diffs) != 1 || p[0].Diffs[0] != (AmbiguityDiff{"enforcement", "enforce", "audit"}) {
		t.Fatalf("%+v", got.pairs)
	}
	// Two pairs, with several conflicts each, flattened by "; ".
	two := "network endpoint ambiguity validation failed:; network policies 'a' endpoint[0] (x.example.com:443) and 'b' endpoint[1] (x.example.com:443,8443/v1) overlap on port(s) 443 with conflicting metadata: tls=1 vs 0; protocol=\"rest\" vs \"graphql\"; network policies 'c' endpoint[0] (<any-host>:80) and 'd' endpoint[0] (y:80) overlap on port(s) 80 with conflicting metadata: allowed_ips=[\"10.0.0.0/8\"] vs []"
	got, ok = translateGatewayError(two, nil)
	for _, w := range []string{"x.example.com:443 で 規則 a と 規則 b", "TLS の扱い: a は tls: skip (中を見ない)、b は 既定 (中を見る)", "検査の種類 (protocol): a は rest、b は graphql", "<any-host>:80 で 規則 c と 規則 d", "解決先 IP の限定 (allowed_ips): c は [\"10.0.0.0/8\"]"} {
		if !ok || !strings.Contains(got.text, w) {
			t.Fatal(w, "\n", got.text)
		}
	}
	if _, ok := translateGatewayError("some other failure", nil); ok {
		t.Fatal("translated another error")
	}
}

// The merge keeps an existing endpoint's metadata: an audited endpoint
// added to a rule that enforces there stays enforced, so nothing is
// ambiguous; added as a rule of its own, it is.
func TestPredictMergeKeepsExistingMetadata(t *testing.T) {
	enforced := restEndpoint("api.example.com", 443)
	enforced.Access = v1.NetworkAccessPresetReadOnly
	base := map[string]v1.NetworkPolicyRule{"api": {Name: "api", Binaries: policyBinaries("/usr/bin/curl"), Endpoints: []v1.PolicyNetworkEndpoint{enforced}}}
	audited := restEndpoint("api.example.com", 443)
	audited.Enforcement = v1.NetworkEnforcementModeAudit
	audited.Rules = []v1.L7Rule{{Allow: &v1.L7Allow{Method: "POST", Path: "/x"}}}
	into := []v1.PolicyMergeOperation{{AddRule: &v1.AddNetworkRule{RuleName: "api", Rule: v1.NetworkPolicyRule{Endpoints: []v1.PolicyNetworkEndpoint{audited}}}}}
	if a, err := predictAmbiguities(base, base, into); err != nil || len(a) != 0 {
		t.Fatal(a, err)
	}
	merged, _ := simulateMerge(base, into)
	if e := merged["api"].Endpoints[0]; e.Enforcement != v1.NetworkEnforcementModeEnforce || len(e.Rules) != 1 {
		t.Fatalf("%+v", e)
	}
	// A hand rule of another name for the same programs folds into the
	// rule with that host too (OpenShell's endpoint-overlap fallback) ...
	other := []v1.PolicyMergeOperation{{AddRule: &v1.AddNetworkRule{RuleName: "other", Rule: v1.NetworkPolicyRule{Binaries: policyBinaries("/usr/bin/curl"), Endpoints: []v1.PolicyNetworkEndpoint{audited}}}}}
	if a, _ := predictAmbiguities(base, base, other); len(a) != 0 {
		t.Fatal("fold:", a)
	}
	// ... for another program, OpenShell keeps it apart (the program
	// would inherit the rule's endpoints), and it is ambiguous.
	wget := []v1.PolicyMergeOperation{{AddRule: &v1.AddNetworkRule{RuleName: "other", Rule: v1.NetworkPolicyRule{Binaries: policyBinaries("/usr/bin/wget"), Endpoints: []v1.PolicyNetworkEndpoint{audited}}}}}
	if a, _ := predictAmbiguities(base, base, wget); len(a) != 1 {
		t.Fatal("kept apart:", a)
	}
	// ... but an approval (the advisor's endpoints) keeps its own name.
	if a, _ := predictAmbiguities(base, base, approvalOps("other", v1.NetworkPolicyRule{Endpoints: []v1.PolicyNetworkEndpoint{audited}})); len(a) != 1 || a[0].Fields()[0] != "enforcement" {
		t.Fatal("approval:", a)
	}
}
