package main

import (
	"context"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	v1 "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1"
	pb "github.com/NVIDIA/OpenShell/sdk/go/proto/openshellv1"

	"iwato/internal/hostterm"
	"iwato/internal/openshell/client"
)

// TestOpenShellRecreateE2E recreates a sandbox on the gateway that
// internal/openshell/wslc/e2e.sh runs (MODE=tls), through the code the
// page's 作り直す runs (Server.recreateSandbox): the new sandbox (another
// OpenShell sandbox of the same name) mounts the same home volume, and has
// the provider attached to the old one and the rule added to its policy.
// The old sandbox's path outside the home (made before the image
// convention) is dropped: its volume stays, not mounted again. (Start's
// automatic re-creation of a failed sandbox keeps it:
// TestStartRemakesAFailedSandboxWithItsPersistedPaths.)
//
// The fake wslc mounts no volume (this machine's /home/dev stands for the
// home), so that the volumes are the same ones is read from the fake's
// calls: each was created once, and the containers that mount it.
func TestOpenShellRecreateE2E(t *testing.T) {
	addr := os.Getenv("OPENSHELL_E2E_ADDR")
	if addr == "" {
		t.Skip("run by internal/openshell/wslc/e2e.sh")
	}
	tlsDir := os.Getenv("OPENSHELL_E2E_TLS")
	ca, cert, key := filepath.Join(tlsDir, "ca.crt"), filepath.Join(tlsDir, "client", "tls.crt"), filepath.Join(tlsDir, "client", "tls.key")
	w := newSandbox("iwato-recreate-e2e")
	// A path outside the home that a sandbox made before the image
	// convention kept: the recreate drops it (only the home persists).
	w.LegacyPersist = []string{"/sandbox/recreate-keep"}
	c := defaultConfig()
	c.WSLC = os.Getenv("OPENSHELL_E2E_WSLC")
	c.Sandboxes = []Sandbox{w}
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
	s := newServer(c, o, false, "127.0.0.1:1")
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	if err := enablePolicyAdvisor(ctx, sdk); err != nil {
		t.Fatal("policy advisor:", err)
	}
	if err := ensureProfiles(ctx, pb.NewOpenShellClient(conn)); err != nil {
		t.Fatal("import the profiles:", err)
	}
	o.profilesDone = true
	// A provider with a made-up token: only its attachment is looked at
	// (never the sandbox's environment).
	gh, _ := profileByID("iwato-github")
	creds, err := profileCredentials(gh, map[string]string{"token": "ghp_e2eRECREATEmadeup"})
	if err != nil {
		t.Fatal(err)
	}
	const provider = "github-recreate"
	if err := o.CreateProvider(ctx, provider, "iwato-github", creds); err != nil {
		t.Fatal("create provider:", err)
	}
	defer func() { _ = o.DeleteProvider(context.Background(), provider) }()

	if err := o.Start(ctx, w); err != nil {
		t.Fatal("start:", err)
	}
	defer o.Delete(context.Background(), w, true)
	run := func(script string) string {
		t.Helper()
		p, err := client.Start(ctx, conn, openshellWorkspace, w.Name, []string{"sh", "-c", script}, client.Options{})
		if err != nil {
			t.Fatal("exec:", err)
		}
		out, _ := io.ReadAll(p)
		p.Wait()
		return string(out)
	}
	const file = homeDir + "/iwato-recreate-e2e.txt"
	if out := run("echo RECREATE-KEPT >" + file + " && cat " + file); !strings.Contains(out, "RECREATE-KEPT") {
		t.Fatalf("writing into the home: %q", out)
	}
	defer func() { os.Remove(file) }()
	// Attached in OpenShell only, as the provider panel does to a sandbox
	// that exists: the configuration's list is not what the recreate reads.
	if ok, _, err := o.AttachProvider(ctx, w.Name, provider); !ok || err != nil {
		t.Fatal("attach:", ok, err)
	}
	rule := PolicyEdit{Op: "add", Rule: "recreate_e2e", Binaries: []string{"/usr/bin/curl"}, Host: "recreate.example.com", Ports: []uint32{443}, Access: "read-only"}
	if _, err := o.EditPolicy(ctx, w, rule); err != nil {
		t.Fatal("add a rule:", err)
	}
	before, _, err := o.PolicyLayers(ctx, w)
	if _, ok := before["recreate_e2e"]; err != nil || !ok {
		t.Fatalf("the rule is not in the policy: %v", err)
	}
	api, _ := o.sandboxes(ctx)
	old, err := api.Get(ctx, openshellWorkspace, w.Name)
	if err != nil {
		t.Fatal(err)
	}
	volsBefore, err := o.Volumes(ctx)
	if err != nil {
		t.Fatal(err)
	}

	failed, err := s.recreateSandbox(ctx, w)
	if err != nil || len(failed) > 0 {
		t.Fatalf("recreate: %v, rules not carried over: %+v", err, failed)
	}

	now, err := api.Get(ctx, openshellWorkspace, w.Name)
	if err != nil {
		t.Fatal(err)
	}
	if now.ID == old.ID {
		t.Fatal("the sandbox was not made again: same ID", now.ID)
	}
	if st := o.Status(ctx, w); st.State != "running" {
		t.Fatalf("after the recreate: %+v", st)
	}
	if out := run("cat " + file); !strings.Contains(out, "RECREATE-KEPT") {
		t.Fatalf("the file in the home after the recreate: %q", out)
	}
	_, attached, err := o.ListProviders(ctx)
	if err != nil || !slices.Equal(attached[w.Name], []string{provider}) {
		t.Fatalf("providers after the recreate: %v %v", attached[w.Name], err)
	}
	after, effective, err := o.PolicyLayers(ctx, w)
	if err != nil {
		t.Fatal(err)
	}
	for name, r := range before {
		if isProviderRule(name) {
			continue
		}
		if !sameRule(after[name], r) {
			t.Errorf("rule %s after the recreate: %+v, was %+v", name, after[name], r)
		}
	}
	if _, ok := effective[providerRuleName(provider)]; !ok {
		t.Errorf("the provider's rule is not in the effective policy: %v", slices.Sorted(maps.Keys(effective)))
	}
	if sb, _ := s.lookupSandbox(w.Name); sb.Carry != nil || !slices.Equal(sb.Providers, []string{provider}) || sb.LegacyPersist != nil {
		t.Fatalf("configuration after the recreate: carry %v, providers %v, persist %v", sb.Carry != nil, sb.Providers, sb.LegacyPersist)
	}
	// The home's volume: not made again, and the new containers mount it.
	// The older sandbox's path outside the home (LegacyPersist): its volume
	// was mounted by the old sandbox only (the explicit recreate drops it),
	// and stays, unused, in the volumes list.
	volsAfter, err := o.Volumes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	calls, err := os.ReadFile(filepath.Join(os.Getenv("FAKE_WSLC_STATE"), "calls.log"))
	if err != nil {
		t.Fatal("the fake wslc's calls:", err)
	}
	for _, p := range w.volumePaths() {
		name := persistVolume(c, w, p)
		created, mounted := 0, 0
		for _, line := range strings.Split(string(calls), "\n") {
			if strings.HasPrefix(line, "volume create ") && strings.HasSuffix(line, " "+name) {
				created++
			}
			if strings.HasPrefix(line, "container ") && strings.Contains(line, "type=volume,source="+name+",target="+p) {
				mounted++
			}
		}
		if legacy := p != homeDir; created != 1 || (!legacy && mounted < 2) || (legacy && mounted != 1) {
			t.Errorf("volume %s (%s): created %d times, mounted by %d container calls", name, p, created, mounted)
		}
		at := func(list []Volume) string {
			i := slices.IndexFunc(list, func(v Volume) bool { return v.Name == name })
			if i < 0 {
				return "missing"
			}
			return list[i].CreatedAt
		}
		if a, b := at(volsBefore), at(volsAfter); a == "missing" || a != b {
			t.Errorf("volume %s: created at %s before, %s after", name, a, b)
		}
	}
	afterRecreate, _ := s.lookupSandbox(w.Name)
	for _, v := range describeVolumes(c, []Sandbox{afterRecreate}, volsAfter) {
		if v.Name == persistVolume(c, w, "/sandbox/recreate-keep") && v.UsedBy != "" {
			t.Errorf("the dropped path's volume is still used by %s", v.UsedBy)
		}
	}
	run("rm -f " + file)
}
