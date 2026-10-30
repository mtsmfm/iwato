package main

import (
	"context"
	"encoding/json"
	"path"
	"slices"
	"strings"
	"testing"

	pb "github.com/NVIDIA/OpenShell/sdk/go/proto/openshellv1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// fakeProfiles is the gateway's profile store.
type fakeProfiles struct {
	stored           map[string]*pb.ProviderProfile
	imports, updates []string
	reject           bool
}

func (f *fakeProfiles) GetProviderProfile(_ context.Context, in *pb.GetProviderProfileRequest, _ ...grpc.CallOption) (*pb.ProviderProfileResponse, error) {
	if in.WorkspaceScope != nil {
		return nil, status.Error(codes.InvalidArgument, "Iwato's profiles are platform profiles")
	}
	p, ok := f.stored[in.Id]
	if !ok {
		return nil, status.Error(codes.NotFound, "provider profile not found")
	}
	return &pb.ProviderProfileResponse{Profile: p}, nil
}

func (f *fakeProfiles) ImportProviderProfiles(_ context.Context, in *pb.ImportProviderProfilesRequest, _ ...grpc.CallOption) (*pb.ImportProviderProfilesResponse, error) {
	if f.reject {
		return &pb.ImportProviderProfilesResponse{Diagnostics: []*pb.ProviderProfileDiagnostic{{Field: "binaries[0]", Message: "bad", Severity: "error"}}}, nil
	}
	for _, item := range in.Profiles {
		p := item.Profile
		if _, ok := f.stored[p.Id]; ok {
			return nil, status.Error(codes.AlreadyExists, "exists")
		}
		p.ResourceVersion = 1
		f.stored[p.Id] = p
		f.imports = append(f.imports, p.Id)
	}
	return &pb.ImportProviderProfilesResponse{Imported: true}, nil
}

func (f *fakeProfiles) UpdateProviderProfiles(_ context.Context, in *pb.UpdateProviderProfilesRequest, _ ...grpc.CallOption) (*pb.UpdateProviderProfilesResponse, error) {
	old := f.stored[in.Id]
	if old == nil || in.ExpectedResourceVersion != old.ResourceVersion {
		return nil, status.Error(codes.Aborted, "resource version")
	}
	p := in.Profile.Profile
	p.ResourceVersion = old.ResourceVersion + 1
	f.stored[in.Id] = p
	f.updates = append(f.updates, in.Id)
	return &pb.UpdateProviderProfilesResponse{Updated: true, Profile: p}, nil
}

// Prepare imports the profiles once, leaves current ones alone, and
// updates one an older Iwato imported.
func TestEnsureProfilesIsIdempotent(t *testing.T) {
	ctx := context.Background()
	f := &fakeProfiles{stored: map[string]*pb.ProviderProfile{}}
	if err := ensureProfiles(ctx, f); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, p := range allProfiles() {
		ids = append(ids, p.ID)
	}
	if !slices.Equal(f.imports, ids) || len(f.updates) != 0 {
		t.Fatal(f.imports, f.updates)
	}
	for range 2 {
		if err := ensureProfiles(ctx, f); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.imports) != len(ids) || len(f.updates) != 0 {
		t.Fatal("current profiles were written again:", f.imports, f.updates)
	}
	// An older version (another hash) is replaced at its resource version.
	f.stored[claudeSubscriptionProfileID].Annotations[profileHashKey] = "sha256:old"
	if err := ensureProfiles(ctx, f); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.updates, []string{claudeSubscriptionProfileID}) || f.stored[claudeSubscriptionProfileID].Annotations[profileHashKey] != profileHash(mustProfile(t, claudeSubscriptionProfileID).proto()) {
		t.Fatal(f.updates)
	}
	// The gateway's diagnostics are the error.
	f = &fakeProfiles{stored: map[string]*pb.ProviderProfile{}, reject: true}
	if err := ensureProfiles(ctx, f); err == nil || !strings.Contains(err.Error(), "binaries[0]: bad") {
		t.Fatal(err)
	}
}

// The profiles name the binaries of Iwato's sandbox image and the hosts.
func TestProfilesMatchTheImage(t *testing.T) {
	gh, _ := profileByID("iwato-github")
	p := gh.proto()
	var paths, hosts []string
	for _, b := range p.Binaries {
		paths = append(paths, b.Path)
	}
	for _, e := range initialEndpoints(t, "iwato-github") {
		hosts = append(hosts, e.Host+e.Path)
	}
	if !slices.Equal(paths, []string{"/usr/bin/gh", "/usr/bin/git", "/usr/lib/git-core/git-remote-http", "/usr/lib/git-core/git-remote-https"}) {
		t.Fatal(paths)
	}
	if !slices.Equal(hosts, []string{"api.github.com/**", "api.github.com/graphql", "github.com/**", "uploads.github.com/**"}) {
		t.Fatal(hosts)
	}
	// Push stays a policy proposal.
	for _, r := range initialEndpoints(t, "iwato-github")[2].Rules {
		if r.Allow.Method == "POST" && r.Allow.Path != "/**/git-upload-pack" {
			t.Fatal("github.com allows", r.Allow)
		}
	}
	cl, _ := profileByID(claudeSubscriptionProfileID)
	c := cl.proto()
	if len(c.Binaries) != 2 || c.Binaries[0].Path != "/home/dev/.local/share/claude/versions/*" || initialEndpoints(t, claudeSubscriptionProfileID)[0].Host != "api.anthropic.com" || c.Credentials[0].EnvVars[0] != "CLAUDE_CODE_OAUTH_TOKEN" || c.Credentials[0].AuthStyle != "bearer" {
		t.Fatal(c)
	}
	if profileHash(gh.proto()) != profileHash(gh.proto()) || profileHash(gh.proto()) == profileHash(c) {
		t.Fatal("the hash is not stable")
	}
}

// The claude globs match where its executable really runs from (OpenShell
// matches /proc/PID/exe with `*` within one path component, as path.Match).
func TestClaudeBinariesMatchTheRealPaths(t *testing.T) {
	cl, _ := profileByID(claudeSubscriptionProfileID)
	match := func(p string) bool {
		for _, b := range cl.proto().Binaries {
			if ok, _ := path.Match(b.Path, p); ok {
				return true
			}
		}
		return false
	}
	for p, want := range map[string]bool{
		// Claude's installer and auto-updater.
		"/home/dev/.local/share/claude/versions/2.1.293": true,
		// mise's aqua backend.
		"/home/dev/.local/share/mise/installs/claude/2.1.287/claude": true,
		"/home/dev/.local/share/mise/shims/claude":                   false,
		"/home/dev/.local/share/claude/versions/2.1.293/other":       false,
		"/usr/bin/curl": false,
	} {
		if match(p) != want {
			t.Errorf("%s: %v", p, !want)
		}
	}
}

type providerList struct {
	Profiles  []providerProfile
	Providers []ProviderView
	Error     string
}

func listProviders(t *testing.T, do func(string, string, string) (int, string)) providerList {
	t.Helper()
	code, body := do("GET", "/api/providers", "")
	var l providerList
	if code != 200 || json.Unmarshal([]byte(body), &l) != nil {
		t.Fatal(code, body)
	}
	return l
}

func provider(l providerList, name string) (ProviderView, bool) {
	i := slices.IndexFunc(l.Providers, func(p ProviderView) bool { return p.Name == name })
	if i < 0 {
		return ProviderView{}, false
	}
	return l.Providers[i], true
}

func TestConfigValidatesProviders(t *testing.T) {
	c := testConfig()
	c.Sandboxes[0].Providers = []string{"github-personal", "github-personal"}
	if c.Validate() == nil {
		t.Fatal("a provider twice")
	}
	c.Sandboxes[0].Providers = []string{"bad name"}
	if c.Validate() == nil {
		t.Fatal("a bad name")
	}
	c.Sandboxes[0].Providers = []string{"github-personal", "claude"}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}
