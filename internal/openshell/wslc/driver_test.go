package wslc

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"google.golang.org/protobuf/types/known/structpb"

	computev1 "iwato/internal/openshell/computev1"
	"iwato/internal/termd"
)

func launchAuthJSON(t *testing.T) []byte {
	t.Helper()
	b, _ := json.Marshal(map[string]any{
		"supervisor": map[string]any{
			"session_id": "6f1c1c6e-1111-4222-8333-944444444444", "runtime_generation": "gen-1",
			"session_rotation": 1, "auth_epoch": 2, "gateway_token": "SUP-SECRET", "gateway_expires_at": 1,
			"sandbox_token": "SBX-SECRET", "sandbox_expires_at": 1,
		},
		"gateway_id":        "gw",
		"verification_keys": []any{map[string]any{"key_id": "k1", "public_key_pem": []int{'P', 'E', 'M'}}},
	})
	return b
}

// The workload gets only the session coordinates and public keys; the
// supervisor bundle passes through verbatim.
func TestLaunchAuthSplit(t *testing.T) {
	a, err := decodeLaunchAuth(launchAuthJSON(t))
	if err != nil {
		t.Fatal(err)
	}
	f, err := buildBoundary("sb-1", a, "cid", "sha256:img", identity{UID: 1000, GID: 1000, SupplementaryGIDs: []uint32{}, Source: "image"}, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(f.Bootstrap, []byte("SECRET")) || bytes.Contains(f.Descriptor, []byte("SECRET")) {
		t.Fatal("supervisor tokens leaked into the workload's files")
	}
	if !bytes.Contains(f.SupervisorAuth, []byte("SUP-SECRET")) {
		t.Fatal("supervisor bundle lost")
	}
	var boot map[string]any
	_ = json.Unmarshal(f.Bootstrap, &boot)
	keys := boot["verification_keys"].([]any)
	if boot["generation"] != "gen-1" || boot["session_rotation"].(float64) != 1 || keys[0].(map[string]any)["public_key_pem"] != "PEM" {
		t.Fatalf("%v", boot)
	}
	for _, bad := range []string{``, `{}`, `{"supervisor":{"session_id":"x"},"gateway_id":"g","verification_keys":[{"key_id":"k","public_key_pem":[1]}]}`} {
		if _, err := decodeLaunchAuth([]byte(bad)); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}

// The digest binds the evidence to the generation like
// ProvisioningEvidence::from_enforcement_evidence.
func TestEvidenceDigest(t *testing.T) {
	e, err := wslcEvidence("gen-1", "cid", false)
	if err != nil {
		t.Fatal(err)
	}
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], 5)
	sum := sha256.Sum256(append(append(n[:], "gen-1"...), `{"container_id":"cid","network_mode":"none","unexpected_networks":[]}`...))
	if e.EvidenceDigest != hex.EncodeToString(sum[:]) || e.Established != nil {
		t.Fatalf("%+v", e)
	}
	if legacy, _ := wslcEvidence("gen-1", "cid", true); legacy.EvidenceDigest != e.EvidenceDigest || len(legacy.Established) != 4 {
		t.Fatalf("%+v", legacy)
	}
	if _, err := wslcEvidence("", "cid", false); err == nil {
		t.Fatal("empty generation accepted")
	}
}

// Each start is a new generation (OpenShell v0.1.5): rotate binds both
// files and their evidence to it, under the key the sandbox's runtime reads.
func TestRotateRebindsTheGeneration(t *testing.T) {
	a, err := decodeLaunchAuth(launchAuthJSON(t))
	if err != nil {
		t.Fatal(err)
	}
	f, err := buildBoundary("sb-1", a, "cid", "sha256:img", identity{UID: 1000, GID: 1000, SupplementaryGIDs: []uint32{}, Source: "image"}, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	next := bytes.Replace(launchAuthJSON(t), []byte(`"gen-1"`), []byte(`"gen-2"`), 1)
	for _, key := range []string{evidenceKey, legacyEvidenceKey} {
		boot, desc := f.Bootstrap, f.Descriptor
		if key == legacyEvidenceKey {
			boot = bytes.Replace(boot, []byte(`"`+evidenceKey+`"`), []byte(`"`+key+`"`), 1)
			desc = bytes.Replace(desc, []byte(`"`+evidenceKey+`"`), []byte(`"`+key+`"`), 1)
		}
		d := newDriver(Config{StateDir: t.TempDir()}, okRunner{})
		if err := d.writeState("sb-1", map[string][]byte{"boundary-bootstrap.json": boot, "runtime-descriptor.json": desc, "identity": []byte("1000:1000")}); err != nil {
			t.Fatal(err)
		}
		if err := d.rotate(context.Background(), &containerInfo{ID: "cid", Labels: map[string]string{labelID: "sb-1"}}, next); err != nil {
			t.Fatal(err)
		}
		st, _ := d.readState("sb-1", "boundary-bootstrap.json", "runtime-descriptor.json")
		want, _ := wslcEvidence("gen-2", "cid", key == legacyEvidenceKey)
		other := legacyEvidenceKey
		if key == legacyEvidenceKey {
			other = evidenceKey
		}
		for _, file := range []string{"boundary-bootstrap.json", "runtime-descriptor.json"} {
			var m map[string]json.RawMessage
			if err := json.Unmarshal(st[file], &m); err != nil {
				t.Fatal(err)
			}
			var got provisioningEvidence
			_ = json.Unmarshal(m[key], &got)
			if string(m["generation"]) != `"gen-2"` || got.Generation != want.Generation || got.EvidenceDigest != want.EvidenceDigest || len(got.Established) != len(want.Established) || m[other] != nil {
				t.Errorf("%s %s: %s", key, file, st[file])
			}
		}
	}
}

func TestResolveIdentity(t *testing.T) {
	passwd := []byte("root:x:0:0::/root:/bin/sh\n# comment\nubuntu:x:1000:1000::/home/ubuntu:/bin/bash\nsvc:x:1500:1600::/:/bin/sh\n")
	group := []byte("root:x:0:\nubuntu:x:1000:\nwheel:x:10:ubuntu\nsvcg:x:1700:\n")
	for _, c := range []struct {
		user, group, image string
		uid, gid           uint32
		source             string
		bad                bool
	}{
		{image: "", uid: 1000, gid: 1000, source: "default"},
		{image: "ubuntu", uid: 1000, gid: 1000, source: "image"},
		{image: "svc:svcg", uid: 1500, gid: 1700, source: "image"},
		{user: "svc", uid: 1500, gid: 1600, source: "policy"},
		{user: "2000", group: "2000", uid: 2000, gid: 2000, source: "policy"},
		{image: "root", bad: true},
		{user: "nobody-here", bad: true},
		{user: "2000", bad: true}, // numeric uid without passwd: group unknown
		// As upstream: the image's USER group applies to a policy user too,
		// selectors are trimmed, numeric uids match passwd numerically, and
		// a group alone with no user anywhere is the default identity.
		{user: "svc", image: "ubuntu:svcg", uid: 1500, gid: 1700, source: "policy"},
		{user: " svc ", uid: 1500, gid: 1600, source: "policy"},
		{user: "01500", uid: 1500, gid: 1600, source: "policy"},
		{group: "svcg", uid: 1000, gid: 1000, source: "default"},
	} {
		id, err := resolveIdentity(c.user, c.group, c.image, "sha256:i", passwd, group)
		if c.bad {
			if err == nil {
				t.Errorf("%+v resolved to %+v", c, id)
			}
			continue
		}
		if err != nil || id.UID != c.uid || id.GID != c.gid || id.Source != c.source || len(id.SupplementaryGIDs) != 0 {
			t.Errorf("%+v: %+v %v", c, id, err)
		}
	}
}

func TestAccountFilesAreParsedStrictly(t *testing.T) {
	group := []byte("svcg:x:1700:\n")
	for _, passwd := range []string{"svc:x:15oo:1600::/:/bin/sh\n", "svc:x:1500\n", ":x:1500:1600::/:/bin/sh\n", "svc:x:1500:16x0::/:/bin/sh\n", "svc:x:1500:1600\xff::/:/bin/sh\n"} {
		if id, err := resolveIdentity("svc", "", "", "sha256:i", []byte(passwd), group); err == nil {
			t.Errorf("%q resolved to %+v", passwd, id)
		}
	}
}

func TestWorkloadNameStaysWithinDockerLimits(t *testing.T) {
	long := strings.Repeat("w", 190)
	for _, sb := range []*computev1.DriverSandbox{
		{Id: "id-1", Name: "n", Workspace: long},
		{Id: "id-1", Name: strings.Repeat("n", 300), Workspace: "default"},
		{Id: "id-1", Workspace: "default"},
	} {
		got := workloadName(sb)
		if len(got) > 200 || strings.HasSuffix(got, "-") {
			t.Errorf("%d %q", len(got), got)
		}
	}
	if got := workloadName(&computev1.DriverSandbox{Id: "ID", Name: "a  b", Workspace: "ws"}); got != "openshell-ws--a--b-id" {
		t.Error(got)
	}
}

func TestImageTag(t *testing.T) {
	for ref, want := range map[string]string{"ghcr.io/x/img:1.2": "1.2", "ghcr.io/x/img": "latest", "localhost:5000/img": "latest",
		"localhost:5000/img:dev": "dev", "img@sha256:abc": "", "img:": ""} {
		if got := imageTag(ref); got != want {
			t.Errorf("%s: %q", ref, got)
		}
	}
	// The default runtime images are pinned (scripts/check-pins.sh checks
	// the digests against the registry), so they are never refreshed.
	pinned := regexp.MustCompile(`^ghcr\.io/nvidia/openshell/(supervisor|sandbox):` + regexp.QuoteMeta(OpenShellVersion) + `@sha256:[0-9a-f]{64}$`)
	for _, ref := range []string{DefaultSupervisorImage, DefaultSandboxImage} {
		if !pinned.MatchString(ref) || imageTag(ref) != "" {
			t.Errorf("%s is not pinned by digest", ref)
		}
	}
}

func driverConfigOf(t *testing.T, js string) *computev1.DriverSandboxTemplate {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(js), &m); err != nil {
		t.Fatal(err)
	}
	s, err := structpb.NewStruct(m)
	if err != nil {
		t.Fatal(err)
	}
	return &computev1.DriverSandboxTemplate{DriverConfig: s}
}

func TestDriverConfigValidation(t *testing.T) {
	for js, ok := range map[string]bool{
		`{"mounts":[{"type":"volume","source":"data","target":"/data"}]}`:                           true,
		`{"mounts":[{"type":"tmpfs","target":"/scratch","size_bytes":1024,"options":["noexec"]}]}`:  true,
		`{"mounts":[{"type":"bind","source":"/etc","target":"/x"}]}`:                                false,
		`{"mounts":[{"type":"image","source":"img","target":"/x"}]}`:                                false,
		`{"mounts":[{"type":"volume","source":"d","target":"/run/openshell/x"}]}`:                   false,
		`{"mounts":[{"type":"volume","source":"d","target":"relative"}]}`:                           false,
		`{"mounts":[{"type":"volume","source":"d","target":"/a"},{"type":"tmpfs","target":"/a/"}]}`: false,
		`{"mounts":[{"type":"volume","source":"d","target":"/a","subpath":"x"}]}`:                   false,
		`{"cdi_devices":["nvidia.com/gpu=all"]}`:                                                    false,
		`{"unknown":1}`:                                                                             false,
		// Per mount type, as upstream's one struct per variant.
		`{"mounts":[{"type":"volume","source":"d","target":"/a","size_bytes":1}]}`: false,
		`{"mounts":[{"type":"tmpfs","target":"/a","source":"x"}]}`:                 false,
		`{"mounts":[{"type":"tmpfs"}]}`:                                            false,
		`{"cdi_devices":[]}`:                                                       false,
		`{"mounts":[{"type":"tmpfs","target":"/a","size_bytes":1e300}]}`:           false,
		`{"mounts":[{"type":"tmpfs","target":"/a","options":[" size=1m "]}]}`:      true,
	} {
		_, err := parseDriverConfig(driverConfigOf(t, js))
		if (err == nil) != ok {
			t.Errorf("%s: %v", js, err)
		}
	}
}

func TestLimits(t *testing.T) {
	for in, want := range map[string]string{"500m": "0.5", "2": "2", "1.5": "1.5"} {
		if got, err := cpuLimit(in); err != nil || got != want {
			t.Errorf("cpu %s: %s %v", in, got, err)
		}
	}
	for in, want := range map[string]string{"256Mi": "268435456", "1G": "1000000000", "4096": "4096", "1K": "1000", "1Pi": "1125899906842624", " 2Gi ": "2147483648"} {
		if got, err := memoryLimit(in); err != nil || got != want {
			t.Errorf("memory %s: %s %v", in, got, err)
		}
	}
	// As the Docker driver: trailing junk and unknown suffixes are errors,
	// not silently dropped.
	if got, _ := memoryLimit("100E"); got != "9223372036854775807" {
		t.Errorf("100E: %s (saturates, as Rust's cast)", got)
	}
	for _, bad := range []string{"", "x", "-1", "0", "2x", "512Xi", "1k", "1.5m"} {
		if _, err := cpuLimit(bad); err == nil {
			t.Errorf("cpu %q accepted", bad)
		}
		if _, err := memoryLimit(bad); err == nil {
			t.Errorf("memory %q accepted", bad)
		}
	}
}

func TestWorkloadName(t *testing.T) {
	sb := &computev1.DriverSandbox{Id: "ID-1", Name: "My Sandbox!", Workspace: "default"}
	if got := workloadName(sb); got != "openshell-default--my-sandbox-id-1" {
		t.Fatal(got)
	}
	sb.Name = strings.Repeat("a", 300)
	if got := workloadName(sb); len(got) > 200 {
		t.Fatalf("%d", len(got))
	}
}

func TestConfigCheck(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "x.pem")
	_ = os.WriteFile(f, []byte("x"), 0o600)
	for _, c := range []struct {
		cfg Config
		ok  bool
	}{
		{Config{Endpoint: "http://gw:1"}, true},
		{Config{Endpoint: "https://gw:1", GuestTLSCA: f}, true},
		{Config{Endpoint: "https://gw:1"}, false},
		{Config{Endpoint: "http://gw:1", GuestTLSCA: f}, false},
		{Config{Endpoint: "gw:1"}, false},
		{Config{Endpoint: "http://gw:1", PullPolicy: "newer"}, false},
	} {
		if err := c.cfg.check(); (err == nil) != c.ok {
			t.Errorf("%+v: %v", c.cfg, err)
		}
	}
}

func TestTarCarriesOwnership(t *testing.T) {
	b, err := buildTar([]tarEntry{{name: "d", dir: true, mode: 0o711, uid: 1000, gid: 1000}, {name: "d/f", mode: 0o600, uid: 1000, gid: 1001, data: []byte("x")}})
	if err != nil {
		t.Fatal(err)
	}
	r := tar.NewReader(bytes.NewReader(b))
	var names []string
	for {
		h, err := r.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		names = append(names, h.Name)
		if h.Name == "d/f" && (h.Uid != 1000 || h.Gid != 1001 || h.Mode != 0o600) {
			t.Fatalf("%+v", h)
		}
	}
	if strings.Join(names, ",") != "d/,d/f" {
		t.Fatal(names)
	}
}

// fakeRunner answers wslc commands from a table, for snapshot and mount
// tests that need no containers.
type fakeRunner map[string]string

func (f fakeRunner) Run(_ context.Context, _ []byte, args ...string) ([]byte, error) {
	key := strings.Join(args, " ")
	if out, ok := f[key]; ok {
		return []byte(out), nil
	}
	return []byte("[]"), errors.New("not found: " + key)
}

func TestMountFlags(t *testing.T) {
	d := newDriver(Config{}, fakeRunner{"volume inspect --format json data": `[{"Name":"data"}]`})
	cfg, err := parseDriverConfig(driverConfigOf(t, `{"mounts":[{"type":"volume","source":"data","target":"/data","read_only":false},{"type":"tmpfs","target":"/t","size_bytes":1024,"mode":448}]}`))
	if err != nil {
		t.Fatal(err)
	}
	flags, err := d.mountFlags(context.Background(), cfg, "/sandbox", nil)
	if err != nil || strings.Join(flags, " ") != "--mount type=volume,source=data,target=/data --tmpfs /t:size=1024,mode=700" {
		t.Fatalf("%q %v", flags, err)
	}
	cfg, _ = parseDriverConfig(driverConfigOf(t, `{"mounts":[{"type":"volume","source":"missing","target":"/m"}]}`))
	if _, err := d.mountFlags(context.Background(), cfg, "/sandbox", nil); err == nil {
		t.Fatal("a missing volume was accepted")
	}
	cfg, _ = parseDriverConfig(driverConfigOf(t, `{"mounts":[{"type":"volume","source":"data","target":"/"}]}`))
	if _, err := d.mountFlags(context.Background(), cfg, "/sandbox", nil); err == nil {
		t.Fatal("a mount over the workspace was accepted")
	}
	// A writable volume may be the workspace itself (Iwato's home); a
	// read-only one or a tmpfs may not.
	cfg, _ = parseDriverConfig(driverConfigOf(t, `{"mounts":[{"type":"volume","source":"data","target":"/home/dev","read_only":false}]}`))
	if flags, err := d.mountFlags(context.Background(), cfg, "/home/dev", nil); err != nil || strings.Join(flags, " ") != "--mount type=volume,source=data,target=/home/dev" {
		t.Fatalf("a writable volume as the workspace: %q %v", flags, err)
	}
	for _, m := range []string{`{"type":"volume","source":"data","target":"/home/dev"}`, `{"type":"tmpfs","target":"/home/dev"}`, `{"type":"volume","source":"data","target":"/home","read_only":false}`} {
		cfg, _ = parseDriverConfig(driverConfigOf(t, `{"mounts":[`+m+`]}`))
		if _, err := d.mountFlags(context.Background(), cfg, "/home/dev", nil); err == nil {
			t.Fatalf("%s was accepted over the workspace", m)
		}
	}
	// An image volume below the workspace is fine; one masking it is not.
	if _, err := d.mountFlags(context.Background(), driverConfig{}, "/w", []string{"/w/data"}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.mountFlags(context.Background(), driverConfig{}, "/w/app", []string{"/w"}); err == nil {
		t.Fatal("a volume masking the workspace was accepted")
	}
	if _, err := d.mountFlags(context.Background(), driverConfig{}, "/sandbox", []string{"/run/openshell"}); err == nil {
		t.Fatal("a volume on a reserved path was accepted")
	}
	if _, err := d.mountFlags(context.Background(), driverConfig{}, "/sandbox", []string{"/sandbox"}); err == nil {
		t.Fatal("an image VOLUME over the workspace was accepted")
	}
}

func TestSnapshot(t *testing.T) {
	d := newDriver(Config{}, fakeRunner{})
	c := containerInfo{ID: "cid", Name: "/w", Labels: map[string]string{labelID: "id", labelName: "n", labelNamespace: "default", labelWorkspace: "ws"}}
	for status, want := range map[string]string{"running": "BackendReady", "created": "Starting", "removing": "Deleting", "exited": "ContainerExited"} {
		c.State.Status = status
		if got := d.snapshot(c).Status.Conditions[0].Reason; got != want {
			t.Errorf("%s: %s", status, got)
		}
	}
	c.State.Status, c.State.ExitCode = "exited", 137
	if got := d.snapshot(c).Status.Conditions[0].Reason; got != "ContainerRuntimeRestart" {
		t.Error(got)
	}
	// An OOM kill is not a runtime restart (the gateway would relaunch it).
	c.State.OOMKilled = true
	if got := d.snapshot(c).Status.Conditions[0].Reason; got != "ContainerExited" {
		t.Error(got)
	}
	c.State.OOMKilled = false
	c.State.Status = "running"
	d.failures["id"] = "supervisor exited"
	if s := d.snapshot(c); s.Status.Conditions[0].Status != "False" || s.Id != "id" || s.Status.InstanceId != "cid" {
		t.Errorf("%+v", s)
	}
}

// errRunner fails every command with the engine's text for a container
// that went away between list and inspect.
type errRunner struct{}

func (errRunner) Run(context.Context, []byte, ...string) ([]byte, error) {
	return nil, errors.New("wslc container inspect: exit status 1: Error from engine: No such container: abc")
}

func TestInspectGoneContainer(t *testing.T) {
	if _, err := (wslc{r: errRunner{}}).container(context.Background(), "abc"); !errors.Is(err, errNotFound) {
		t.Fatal(err)
	}
}

func TestFailedProvisioningStaysUntilDeleted(t *testing.T) {
	d := newDriver(Config{}, fakeRunner{"container list --all --quiet --no-trunc --filter label=openshell.ai/isolation-role=sandbox --filter label=openshell.ai/managed-by=openshell --filter label=openshell.ai/sandbox-namespace=default": ""})
	failed := &computev1.DriverSandbox{Id: "id", Name: "n", Status: &computev1.DriverSandboxStatus{Conditions: []*computev1.DriverCondition{condition(false, "ImagePullFailed", "no such image")}}}
	d.failed["id"] = failed
	got, err := d.GetSandbox(context.Background(), &computev1.GetSandboxRequest{SandboxId: "id"})
	if err != nil || got.Sandbox.Status.Conditions[0].Reason != "ImagePullFailed" {
		t.Fatal(got, err)
	}
	list, err := d.ListSandboxes(context.Background(), &computev1.ListSandboxesRequest{})
	if err != nil || len(list.Sandboxes) != 1 {
		t.Fatal(list, err)
	}
	if _, err := d.CreateSandbox(context.Background(), &computev1.CreateSandboxRequest{Sandbox: &computev1.DriverSandbox{Id: "id"}}); err == nil {
		t.Fatal("created over a failed sandbox")
	}
	if r, err := d.DeleteSandbox(context.Background(), &computev1.DeleteSandboxRequest{SandboxId: "id"}); err != nil || !r.Deleted {
		t.Fatal(r, err)
	}
	if _, ok := d.failed["id"]; ok {
		t.Fatal("still failed after delete")
	}
}

func TestTmpfsOptionsAreTrimmed(t *testing.T) {
	d := newDriver(Config{}, fakeRunner{})
	cfg, err := parseDriverConfig(driverConfigOf(t, `{"mounts":[{"type":"tmpfs","target":"/t","options":[" size = 1m ","noexec "]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	flags, err := d.mountFlags(context.Background(), cfg, "/sandbox", nil)
	if err != nil || strings.Join(flags, " ") != "--tmpfs /t:size=1m,noexec" {
		t.Fatalf("%q %v", flags, err)
	}
}

func TestWorkspaceOutsideTheChannel(t *testing.T) {
	for _, wd := range []string{"/.openshell", "/.openshell/channel/x"} {
		if ws, err := resolveWorkspace(wd); err == nil {
			t.Errorf("%s accepted as %s", wd, ws)
		}
	}
}

func TestStartChecksTheOuterFenceAndGeneration(t *testing.T) {
	c := containerInfo{}
	c.HostConfig.NetworkMode = "openshell"
	if checkOuterFence(c) == nil {
		t.Fatal("a networked workload passed the outer fence")
	}
	d := newDriver(Config{StateDir: t.TempDir()}, fakeRunner{})
	if err := d.adoptGeneration("id", "g1"); err != nil {
		t.Fatal(err)
	}
	if err := d.adoptGeneration("id", "g1"); err != nil {
		t.Fatal("a retry of the same start was refused:", err)
	}
	if d.adoptGeneration("id", "g2") == nil {
		t.Fatal("another start generation was accepted on a running sandbox")
	}
}

// The driver places the daemon only where an older sandbox runs it from
// (its legacy path, as its main process); a sandbox of the image
// convention, or one without the daemon, gets nothing.
func TestLegacyTermOnlyForLegacySandboxes(t *testing.T) {
	d := newDriver(Config{LegacyTerm: []byte("\x7fELF")}, fakeRunner{})
	spec := func(cmd ...string) []byte {
		return mainProcessSpec(&computev1.DriverSandboxSpec{Command: cmd})
	}
	got := d.legacyTermEntries(spec(termd.LegacyPath, "serve", "-key", "a"))
	if len(got) != 2 || got[1].name != strings.TrimPrefix(termd.LegacyPath, "/") || got[1].mode != 0o555 {
		t.Fatalf("legacy sandbox: %+v", got)
	}
	for _, main := range [][]byte{spec(termd.Path, "serve", "-key", "a"), spec(), spec("sleep", "1"), nil} {
		if got := d.legacyTermEntries(main); got != nil {
			t.Errorf("%s: placed %+v", main, got)
		}
	}
	if got := newDriver(Config{}, fakeRunner{}).legacyTermEntries(spec(termd.LegacyPath, "serve")); got != nil {
		t.Errorf("without a binary: %+v", got)
	}
}

type okRunner struct{}

func (okRunner) Run(context.Context, []byte, ...string) ([]byte, error) { return nil, nil }

func gpuSandbox(gpu *computev1.GpuResourceRequirements) *computev1.DriverSandbox {
	spec := &computev1.DriverSandboxSpec{Template: &computev1.DriverSandboxTemplate{}}
	if gpu != nil {
		spec.ResourceRequirements = &computev1.ResourceRequirements{Gpu: gpu}
	}
	return &computev1.DriverSandbox{Spec: spec}
}

// WSLc has only `--gpus all`: a request without a count or for one GPU is
// accepted, more are refused (as the Docker driver on WSL).
func TestGPUValidation(t *testing.T) {
	one, two := uint32(1), uint32(2)
	for name, c := range map[string]struct {
		gpu *computev1.GpuResourceRequirements
		ok  bool
	}{
		"none": {nil, true}, "default": {&computev1.GpuResourceRequirements{}, true},
		"one": {&computev1.GpuResourceRequirements{Count: &one}, true}, "two": {&computev1.GpuResourceRequirements{Count: &two}, false},
	} {
		err := validate(gpuSandbox(c.gpu))
		if (err == nil) != c.ok {
			t.Errorf("%s: %v", name, err)
		}
	}
	d := newDriver(Config{}, fakeRunner{})
	caps, _ := d.GetCapabilities(context.Background(), nil)
	if g := caps.GetResourceCapabilities().GetGpu(); !g.GetDefaultSelectionSupported() || !g.GetCountSelectionSupported() {
		t.Errorf("capabilities: %v", g)
	}
}

// The claim reaches both files, and a restart (rotate) keeps it: rotate
// edits the stored bootstrap and descriptor instead of rebuilding them.
func TestGPUClaimSurvivesRotate(t *testing.T) {
	a, err := decodeLaunchAuth(launchAuthJSON(t))
	if err != nil {
		t.Fatal(err)
	}
	id := identity{UID: 1000, GID: 1000, SupplementaryGIDs: []uint32{}, Source: "image"}
	for _, gpu := range []bool{false, true} {
		f, err := buildBoundary("sb-1", a, "cid", "sha256:img", id, nil, gpu)
		if err != nil {
			t.Fatal(err)
		}
		d := newDriver(Config{StateDir: t.TempDir()}, okRunner{})
		if err := d.writeState("sb-1", map[string][]byte{"boundary-bootstrap.json": f.Bootstrap, "runtime-descriptor.json": f.Descriptor, "identity": []byte("1000:1000")}); err != nil {
			t.Fatal(err)
		}
		c := &containerInfo{ID: "cid", Labels: map[string]string{labelID: "sb-1"}}
		if err := d.rotate(context.Background(), c, launchAuthJSON(t)); err != nil {
			t.Fatal(err)
		}
		st, _ := d.readState("sb-1", "boundary-bootstrap.json", "runtime-descriptor.json")
		for _, file := range []string{"boundary-bootstrap.json", "runtime-descriptor.json"} {
			var m struct {
				Claims map[string]string `json:"resource_claims"`
			}
			if err := json.Unmarshal(st[file], &m); err != nil {
				t.Fatal(err)
			}
			if got := m.Claims[claimGPU] == "true"; got != gpu {
				t.Errorf("%s gpu=%v: claims %v", file, gpu, m.Claims)
			}
		}
	}
}

// Every sandbox gets a /dev/shm big enough for a browser (WSLc's default is
// 64 MB); a GPU sandbox also gets `--gpus all`.
func TestMemoryDeviceFlags(t *testing.T) {
	if got, want := strings.Join(memoryDeviceFlags(false), " "), "--shm-size 1g"; got != want {
		t.Errorf("no GPU: %q, want %q", got, want)
	}
	if got, want := strings.Join(memoryDeviceFlags(true), " "), "--shm-size 1g --gpus all"; got != want {
		t.Errorf("GPU: %q, want %q", got, want)
	}
}
