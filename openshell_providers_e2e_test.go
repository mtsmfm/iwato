package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	v1 "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1"
	pb "github.com/NVIDIA/OpenShell/sdk/go/proto/openshellv1"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/encoding/prototext"
	"google.golang.org/protobuf/proto"

	"iwato/internal/hostterm"
	"iwato/internal/openshell/client"
)

// countingProfiles counts the profile writes that reach the gateway.
type countingProfiles struct {
	profileAPI
	imports, updates int
}

func (c *countingProfiles) ImportProviderProfiles(ctx context.Context, in *pb.ImportProviderProfilesRequest, opts ...grpc.CallOption) (*pb.ImportProviderProfilesResponse, error) {
	c.imports++
	return c.profileAPI.ImportProviderProfiles(ctx, in, opts...)
}

func (c *countingProfiles) UpdateProviderProfiles(ctx context.Context, in *pb.UpdateProviderProfilesRequest, opts ...grpc.CallOption) (*pb.UpdateProviderProfilesResponse, error) {
	c.updates++
	return c.profileAPI.UpdateProviderProfiles(ctx, in, opts...)
}

// TestOpenShellProvidersE2E imports Iwato's profiles into the gateway that
// internal/openshell/wslc/e2e.sh runs (MODE=tls), creates providers,
// attaches them to a sandbox and reads the environment the sandbox's
// processes get. Requests through OpenShell's proxy (the placeholders'
// replacement) cannot be tried here: the fake wslc gives the sandbox no
// network namespace of its own.
func TestOpenShellProvidersE2E(t *testing.T) {
	addr := os.Getenv("OPENSHELL_E2E_ADDR")
	if addr == "" {
		t.Skip("run by internal/openshell/wslc/e2e.sh")
	}
	tlsDir := os.Getenv("OPENSHELL_E2E_TLS")
	ca, cert, key := filepath.Join(tlsDir, "ca.crt"), filepath.Join(tlsDir, "client", "tls.crt"), filepath.Join(tlsDir, "client", "tls.key")
	c := defaultConfig()
	c.WSLC = os.Getenv("OPENSHELL_E2E_WSLC")
	b, err := NewEngine(c)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := client.Dial(addr, ca, cert, key)
	if err != nil {
		t.Fatal(err)
	}
	sdk, err := v1.NewClient(v1.Config{Address: addr, TLS: &v1.TLSConfig{CAFile: ca, CertFile: cert, KeyFile: key}})
	if err != nil {
		t.Fatal(err)
	}
	o := &OpenShell{e: b, ready: make(chan struct{}), sdk: sdk, conn: conn, env: termEnv, terms: map[string]map[string]*hostterm.Session{}}
	close(o.ready)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := enablePolicyAdvisor(ctx, sdk); err != nil {
		t.Fatal("policy advisor:", err)
	}

	// The profiles go in once; a second Prepare leaves them alone, and a
	// changed one is replaced.
	api := &countingProfiles{profileAPI: pb.NewOpenShellClient(conn)}
	if err := ensureProfiles(ctx, api); err != nil {
		t.Fatal("import the profiles:", err)
	}
	if api.imports != len(providerProfiles) || api.updates != 0 {
		t.Fatalf("first import: %d imports, %d updates", api.imports, api.updates)
	}
	if err := ensureProfiles(ctx, api); err != nil || api.imports != len(providerProfiles) || api.updates != 0 {
		t.Fatalf("the second import changed something: %v, %d imports, %d updates", err, api.imports, api.updates)
	}
	got, err := pb.NewOpenShellClient(conn).GetProviderProfile(ctx, &pb.GetProviderProfileRequest{Id: "iwato-github"})
	if err != nil {
		t.Fatal(err)
	}
	if gp := got.GetProfile(); len(gp.GetEndpoints()) != 4 || len(gp.GetEndpoints()[2].GetRules()) != 4 || gp.GetEndpoints()[1].GetPath() != "/graphql" || len(gp.GetBinaries()) != 4 {
		t.Fatalf("the gateway's iwato-github lost detail: %v", gp)
	}
	// The gateway holds what the YAML files say, and renders back to them.
	for _, p := range providerProfiles {
		got, err := pb.NewOpenShellClient(conn).GetProviderProfile(ctx, &pb.GetProviderProfileRequest{Id: p.ID})
		if err != nil {
			t.Fatal(err)
		}
		held := proto.Clone(got.GetProfile()).(*pb.ProviderProfile)
		held.ResourceVersion, held.Annotations, held.Source, held.Scope = 0, nil, "", ""
		if !proto.Equal(held, p.proto()) {
			t.Fatalf("the gateway's %s differs from its YAML:\n%v\n%v", p.ID, prototext.Format(held), prototext.Format(p.proto()))
		}
		y, err := profileYAML(got.GetProfile())
		if err != nil {
			t.Fatal(err)
		}
		if back, err := parseProfileYAML([]byte(y)); err != nil || !proto.Equal(back, held) {
			t.Fatalf("%s rendered from the gateway does not parse back: %v\n%s", p.ID, err, y)
		}
	}
	old := providerProfiles[1].yaml
	providerProfiles[1].yaml = bytes.Replace(old, []byte("\ndescription: "), []byte("\ndescription: (changed) "), 1)
	err = ensureProfiles(ctx, api)
	providerProfiles[1].yaml = old
	if err != nil || api.updates != 1 {
		t.Fatalf("a changed profile was not updated: %v, %d updates", err, api.updates)
	}
	if err := ensureProfiles(ctx, api); err != nil || api.updates != 2 {
		t.Fatalf("the profile was not restored: %v, %d updates", err, api.updates)
	}
	o.profilesDone = true

	gh, _ := profileByID("iwato-github")
	ghCreds, err := profileCredentials(gh, map[string]string{"token": "ghp_e2eSECRETvalue"})
	if err != nil {
		t.Fatal(err)
	}
	cl, _ := profileByID(claudeSubscriptionProfileID)
	clCreds, _ := profileCredentials(cl, map[string]string{"access_token": "sk-ant-oat01-e2eSECRET"})
	for name, p := range map[string]struct {
		profile string
		creds   map[string]string
	}{"github-e2e": {"iwato-github", ghCreds}, "github-other": {"iwato-github", ghCreds}, "claude-e2e": {claudeSubscriptionProfileID, clCreds}} {
		if err := o.CreateProvider(ctx, name, p.profile, p.creds); err != nil {
			t.Fatal("create provider", name, ":", err)
		}
	}
	defer func() {
		for _, n := range []string{"github-e2e", "github-other", "claude-e2e"} {
			_ = o.DeleteProvider(context.Background(), n)
		}
	}()
	list, _, err := o.ListProviders(ctx)
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(list, func(p ProviderInfo) bool { return p.Name == "github-e2e" })
	if i < 0 || list[i].Profile != "iwato-github" || !slices.Equal(list[i].Keys, []string{"GH_TOKEN", "GITHUB_TOKEN"}) {
		t.Fatalf("listed providers: %+v", list)
	}
	if err := o.UpdateProvider(ctx, "github-e2e", ghCreds); err != nil {
		t.Fatal("update:", err)
	}
	if kek := os.Getenv("OPENSHELL_E2E_KEK"); kek != "" {
		if st, err := os.Stat(kek); err != nil || st.Size() != 32 || st.Mode().Perm() != 0o600 {
			t.Fatalf("the key-encryption key is not where gateway.toml says: %v %v", st, err)
		}
	}

	// Created with a provider (Sandbox.Providers), as the page's create form.
	w := newSandbox("iwato-prov-e2e")
	w.Providers = []string{"github-e2e"}
	if ok, receipt, err := o.AttachProvider(ctx, w.Name, "claude-e2e"); ok || receipt != "" || err != nil {
		t.Fatal("attaching to a sandbox OpenShell does not have yet:", ok, err)
	}
	if err := o.Start(ctx, w); err != nil {
		t.Fatal("start:", err)
	}
	defer o.Delete(context.Background(), w, true)
	env := func() string {
		t.Helper()
		p, err := client.Start(ctx, conn, openshellWorkspace, w.Name, []string{"sh", "-c", "echo GH=$GH_TOKEN; echo GITHUB=$GITHUB_TOKEN; echo CLAUDE=$CLAUDE_CODE_OAUTH_TOKEN"}, client.Options{})
		if err != nil {
			t.Fatal("exec:", err)
		}
		out, _ := io.ReadAll(p)
		p.Wait()
		return string(out)
	}
	out := env()
	// Placeholders (placeholderPrefix + ":<revision>_KEY"), never the values.
	placeholder := func(out, name, key string) bool {
		return regexp.MustCompile(`(?m)^` + name + `=` + placeholderPrefix + `:(v\d+_)?` + key + `$`).MatchString(out)
	}
	if strings.Contains(out, "SECRET") || !placeholder(out, "GH", "GH_TOKEN") || !placeholder(out, "GITHUB", "GITHUB_TOKEN") || !strings.Contains(out, "CLAUDE=\n") {
		t.Fatalf("the sandbox's environment:\n%s", out)
	}
	_, attached, err := o.ListProviders(ctx)
	if err != nil || !slices.Equal(attached[w.Name], []string{"github-e2e"}) {
		t.Fatalf("attached: %v %v", attached, err)
	}
	// The policy tab shows the effective policy (GetSandboxConfig): the
	// provider's rule, which no revision holds, under OpenShell's name.
	providerRule := func(name string) *PolicyRuleView {
		t.Helper()
		v, err := o.Proposals(ctx, w)
		if err != nil || v.Policy == nil || v.Error != "" {
			t.Fatalf("proposal view: %v %+v", err, v)
		}
		for i, r := range v.Policy.Rules {
			if r.Name == name {
				return &v.Policy.Rules[i]
			}
		}
		return nil
	}
	if r := providerRule("_provider_github_e2e"); r == nil || r.Source != "provider" || !slices.Contains(r.Binaries, "/usr/bin/gh") || len(r.Chunks) != 0 {
		t.Fatalf("the effective policy lacks the provider's rule: %+v", r)
	}
	if rules, err := o.revisionRules(ctx, w); err != nil || rules["_provider_github_e2e"].Name != "" {
		t.Fatalf("a revision holds the provider's rule: %v", err)
	}
	// UndoDraftChunk reaches the gateway (no chunk can be made here: see
	// TestOpenShellE2E); an unknown one is refused, nothing changes.
	if pol, err := o.policy(ctx); err != nil {
		t.Fatal(err)
	} else if _, err := pol.UndoDraftChunk(ctx, openshellWorkspace, w.Name, "00000000-0000-0000-0000-000000000000"); !notFound(err) {
		t.Fatal("undo of an unknown chunk:", err)
	}
	if _, err := o.UndoProposal(ctx, w, requestID("none"), false); err == nil || !strings.Contains(err.Error(), "見つかりません") {
		t.Fatal("undo of an unknown proposal:", err)
	}
	// A second GitHub provider sets the same variables: OpenShell refuses
	// it too (Iwato checks first, providerConflict).
	if ok, _, err := o.AttachProvider(ctx, w.Name, "github-other"); ok || err == nil {
		t.Fatal("OpenShell attached a second GitHub provider")
	}
	// Attach returns at once with a receipt; the running sandbox applies
	// the change on its next settings poll.
	applied := func(provider, receipt, want string) {
		t.Helper()
		deadline := time.Now().Add(60 * time.Second)
		for {
			state, reason, err := o.ProviderReadiness(ctx, w.Name, provider, receipt)
			if err != nil {
				t.Fatal("readiness:", err)
			}
			if state == want {
				return
			}
			if state == "withheld" || state == "failed" || time.Now().After(deadline) {
				t.Fatalf("%s: %s (%s), want %s", provider, state, reason, want)
			}
			time.Sleep(500 * time.Millisecond)
		}
	}
	start := time.Now()
	ok, receipt, err := o.AttachProvider(ctx, w.Name, "claude-e2e")
	if !ok || receipt == "" || err != nil {
		t.Fatal("attach:", ok, receipt, err)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatal("attach waited for the sandbox:", took)
	}
	if state, _, _ := o.ProviderReadiness(ctx, w.Name, "claude-e2e", receipt); state != "pending" && state != "persisted" && state != "ready" {
		t.Fatal("readiness right after attaching:", state)
	}
	applied("claude-e2e", receipt, "ready")
	// New processes get it.
	if out := env(); !placeholder(out, "CLAUDE", "CLAUDE_CODE_OAUTH_TOKEN") || strings.Contains(out, "SECRET") {
		t.Fatalf("after attaching:\n%s", out)
	}
	if providerRule("_provider_claude_e2e") == nil {
		t.Fatal("the attached provider's rule is not in the effective policy")
	}
	if err := o.DeleteProvider(ctx, "claude-e2e"); err == nil {
		t.Fatal("OpenShell deleted an attached provider")
	}
	ok, receipt, err = o.DetachProvider(ctx, w.Name, "claude-e2e")
	if !ok || receipt == "" || err != nil {
		t.Fatal("detach:", ok, err)
	}
	applied("claude-e2e", receipt, "revoked")
	if out := env(); !strings.Contains(out, "CLAUDE=\n") {
		t.Fatalf("after detaching:\n%s", out)
	}
	if providerRule("_provider_claude_e2e") != nil {
		t.Fatal("a detached provider's rule stayed in the effective policy")
	}
	if err := o.DeleteProvider(ctx, "claude-e2e"); err != nil {
		t.Fatal("delete a detached provider:", err)
	}
}
