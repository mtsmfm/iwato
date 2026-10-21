// Package wslc is OpenShell's compute driver for WSLc, a port of the
// Docker driver (docs/adr/0007).
package wslc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	computev1 "iwato/internal/openshell/computev1"
	extensionv1 "iwato/internal/openshell/extensionv1"
	"iwato/internal/termd"
)

// OpenShellVersion is the OpenShell release this driver and Iwato's gateway
// image follow; the default runtime images are its official ones, pinned by
// the digest of the tag's multi-architecture index (tag@digest: the engine
// pulls and finds them by the digest, and an image pulled earlier by the
// tag alone is found by it too). scripts/check-pins.sh checks the digests
// against the registry; docs/dev/development.md#bumping-pinned-versions.
const (
	OpenShellVersion       = "0.1.5"
	supervisorImageDigest  = "sha256:fabcd01f656e9d23df4386f82e80b4f48b90f3452d7d5c58889007643a069161"
	sandboxImageDigest     = "sha256:e9adf9ecde328122414ad2ee8c6fc5d5b389e4cf168daf4357c46f5a77d55851"
	DefaultSupervisorImage = "ghcr.io/nvidia/openshell/supervisor:" + OpenShellVersion + "@" + supervisorImageDigest
	DefaultSandboxImage    = "ghcr.io/nvidia/openshell/sandbox:" + OpenShellVersion + "@" + sandboxImageDigest
)

const (
	driverName    = "wslc"
	driverVersion = "0.1.0"
	// contractCapability is the only compute capability the gateway
	// requires; the optional ones (launch-authentication, isolation-backend)
	// are not declared.
	contractCapability = "openshell.compute.contract"

	labelManagedBy = "openshell.ai/managed-by"
	labelID        = "openshell.ai/sandbox-id"
	labelName      = "openshell.ai/sandbox-name"
	labelWorkspace = "openshell.ai/sandbox-workspace"
	labelNamespace = "openshell.ai/sandbox-namespace"
	labelBackend   = "openshell.ai/isolation-backend"
	labelRole      = "openshell.ai/isolation-role"
	// labelGPU marks a workload created with --gpus all (WSLc inspect shows
	// no GPU information).
	labelGPU = "iwato.gpu"

	pollInterval = 2 * time.Second
	// healthProbeTimeout bounds one supervisor health probe. Readiness has
	// no deadline of its own: the gateway owns it (as with the Docker
	// driver) and cancels provisioning when it gives up.
	healthProbeTimeout = 10 * time.Second
	// supervisorExitGrace lets a StopSandbox that races a supervisor's exit
	// mark the exit intentional (the Docker driver waits 1 s too).
	supervisorExitGrace = time.Second
	// The sandbox answers SIGTERM by killing the workload at once
	// (BoundaryRuntime::deactivate), so a longer grace buys the workload
	// nothing. If its supervisor is gone without ending it, v0.1.5 (as
	// v0.1.2) can hang after SIGTERM until killed (docs/dev/openshell.md);
	// a short grace bounds that.
	stopTimeoutSec = 2
	// supervisorStopSec is the supervisor's grace, the Docker driver's.
	supervisorStopSec = 5
)

// Config is what the driver needs that the gateway does not send.
type Config struct {
	// Endpoint is the gateway address supervisors dial (OPENSHELL_ENDPOINT).
	Endpoint string
	// Network is the WSLc network the gateway and the supervisors share.
	Network         string
	SupervisorImage string
	SandboxImage    string
	DefaultImage    string
	// Namespace labels this driver's sandboxes (sandbox-namespace).
	Namespace string
	// StateDir keeps each sandbox's boundary files for restarts.
	StateDir string
	LogLevel string
	// PullPolicy is if-not-present (default), always or never.
	PullPolicy string
	// GuestTLSCA is the gateway CA staged for every supervisor when
	// Endpoint is https. Supervisors authenticate with their sandbox's
	// bearer token, so no client certificate is staged (as the Docker
	// driver since OpenShell v0.1.4, NVIDIA/OpenShell#3110).
	GuestTLSCA string
	// ShellInit runs the sandbox under /bin/sh as the container's first
	// process, which passes SIGTERM and SIGINT on, so that
	// openshell-sandbox is not PID 1 (the kernel drops the signals a PID 1
	// has no handler for). Off by default.
	ShellInit bool
	// LegacyTerm is Iwato's daemon (cmd/iwato-entrypoint), placed at
	// termd.LegacyPath on each start of a sandbox whose main process runs
	// from there (one made before the image carried the daemon), or nil for
	// none. Sandboxes of the image convention (docs/adr/0005) get nothing
	// from the driver: the image carries the daemon and the Policy Advisor
	// guide.
	LegacyTerm []byte
}

// shellInit is the first process with ShellInit: it starts the sandbox,
// passes stop signals to it and exits with its status.
const shellInit = `trap 'kill -TERM "$p" 2>/dev/null' TERM; trap 'kill -INT "$p" 2>/dev/null' INT; "$@" & p=$!; while :; do wait "$p"; s=$?; kill -0 "$p" 2>/dev/null || exit "$s"; done`

// check validates the configuration at startup, as the Docker driver does.
func (c Config) check() error {
	switch c.PullPolicy {
	case "", "if-not-present", "always", "never":
	default:
		return fmt.Errorf("image pull policy must be if-not-present, always or never, not %q", c.PullPolicy)
	}
	// The files are read when a supervisor starts: Iwato serves the driver
	// before the gateway, which creates its CA on first start.
	https := strings.HasPrefix(c.Endpoint, "https://")
	switch {
	case !https && !strings.HasPrefix(c.Endpoint, "http://"):
		return fmt.Errorf("gateway endpoint must be http:// or https://, not %q", c.Endpoint)
	case https && c.GuestTLSCA == "":
		return errors.New("an https gateway endpoint needs the guest TLS CA")
	case !https && c.GuestTLSCA != "":
		return errors.New("the guest TLS CA needs an https gateway endpoint")
	}
	return nil
}

type driver struct {
	computev1.UnimplementedComputeDriverServer
	cfg Config
	w   wslc

	mu      sync.Mutex
	pending map[string]*computev1.DriverSandbox // provisioning, keyed by id
	// failed are sandboxes whose provisioning failed: reported with their
	// error until DeleteSandbox, as the Docker driver does, not as deleted.
	failed   map[string]*computev1.DriverSandbox
	failures map[string]string // runtime failure messages
	stopping map[string]bool   // stopped on purpose
	subs     map[chan *computev1.WatchSandboxesEvent]struct{}
	previous map[string]*computev1.DriverSandbox
	// supervised are sandboxes whose supervisor this process started; only
	// those are failed closed when the supervisor exits. A restarted driver
	// leaves running supervisors alone.
	supervised map[string]bool
	starting   map[string]bool               // StartSandbox in progress: no stale exits
	cancels    map[string]context.CancelFunc // provisioning in progress
	deleting   map[string]bool               // deleted while provisioning
	binary     []byte                        // openshell-sandbox, extracted once
	accounts   map[string][2][]byte          // image ID -> /etc/passwd, /etc/group
}

func newDriver(cfg Config, run runner) *driver {
	if cfg.Namespace == "" {
		cfg.Namespace = "default"
	}
	if cfg.LogLevel == "" {
		cfg.LogLevel = "info"
	}
	return &driver{
		cfg: cfg, w: wslc{run},
		pending: map[string]*computev1.DriverSandbox{}, failed: map[string]*computev1.DriverSandbox{}, failures: map[string]string{}, stopping: map[string]bool{},
		subs: map[chan *computev1.WatchSandboxesEvent]struct{}{}, previous: map[string]*computev1.DriverSandbox{},
		supervised: map[string]bool{}, starting: map[string]bool{}, cancels: map[string]context.CancelFunc{}, deleting: map[string]bool{},
		accounts: map[string][2][]byte{},
	}
}

// GetCapabilities carries the extension negotiation: protocol 1.x, the
// contract capability, and no resource admission policy (the gateway table
// sets resource_admission.enabled = false).
func (d *driver) GetCapabilities(context.Context, *computev1.GetCapabilitiesRequest) (*computev1.GetCapabilitiesResponse, error) {
	return &computev1.GetCapabilitiesResponse{
		DriverName:              driverName,
		DriverVersion:           driverVersion,
		DefaultImage:            d.cfg.DefaultImage,
		GatewayManagesLifecycle: true,
		ResourceCapabilities: &computev1.ResourceCapabilities{
			Cpu:    &computev1.CpuResourceCapabilities{LimitSupported: true},
			Memory: &computev1.MemoryResourceCapabilities{LimitSupported: true},
			Gpu:    &computev1.GpuResourceCapabilities{DefaultSelectionSupported: true, CountSelectionSupported: true},
		},
		Extension: &extensionv1.PeerMetadata{
			ProtocolVersion:       &extensionv1.ProtocolVersion{Major: 1, Minor: 0},
			ImplementationName:    "iwato/openshell-driver-wslc",
			ImplementationVersion: driverVersion,
			SupportedCapabilities: []string{contractCapability},
			RequiredCapabilities:  []string{contractCapability},
		},
	}, nil
}

func (d *driver) AuthenticateSandbox(context.Context, *computev1.AuthenticateSandboxRequest) (*computev1.AuthenticateSandboxResponse, error) {
	return nil, status.Error(codes.Unimplemented, "wslc does not authenticate sandbox credentials")
}

func (d *driver) EnsureWorkspace(context.Context, *computev1.EnsureWorkspaceRequest) (*computev1.EnsureWorkspaceResponse, error) {
	return &computev1.EnsureWorkspaceResponse{}, nil
}

func (d *driver) DeleteWorkspace(context.Context, *computev1.DeleteWorkspaceRequest) (*computev1.DeleteWorkspaceResponse, error) {
	return &computev1.DeleteWorkspaceResponse{}, nil
}

func (d *driver) ValidateSandboxCreate(ctx context.Context, r *computev1.ValidateSandboxCreateRequest) (*computev1.ValidateSandboxCreateResponse, error) {
	if r.GetSandbox() == nil {
		return nil, status.Error(codes.InvalidArgument, "sandbox is required")
	}
	return &computev1.ValidateSandboxCreateResponse{}, d.validate(ctx, r.GetSandbox())
}

// validate is validate plus what needs WSLc: driver_config volumes must
// exist (as the Docker driver checks at validation).
func (d *driver) validate(ctx context.Context, sb *computev1.DriverSandbox) error {
	if err := validate(sb); err != nil {
		return err
	}
	cfg, _ := parseDriverConfig(sb.GetSpec().GetTemplate())
	for _, m := range cfg.Mounts {
		if m.Type == "volume" {
			if err := d.volumeExists(ctx, m.Source); err != nil {
				return status.Error(codes.FailedPrecondition, err.Error())
			}
		}
	}
	return nil
}

// validate rejects what this driver cannot honor.
func validate(sb *computev1.DriverSandbox) error {
	spec, tmpl := sb.GetSpec(), sb.GetSpec().GetTemplate()
	switch {
	case spec == nil || tmpl == nil:
		return status.Error(codes.InvalidArgument, "sandbox spec and template are required")
	case spec.GetIsolationBackend() != nil:
		return status.Error(codes.FailedPrecondition, "external isolation backends are not supported")
	case tmpl.GetAgentSocketPath() != "":
		return status.Error(codes.FailedPrecondition, "agent_socket_path is not supported")
	case tmpl.GetPlatformConfig() != nil && len(tmpl.GetPlatformConfig().GetFields()) > 0:
		return status.Error(codes.FailedPrecondition, "platform_config is not supported")
	case strings.TrimSpace(tmpl.GetResources().GetCpuRequest()) != "" || strings.TrimSpace(tmpl.GetResources().GetMemoryRequest()) != "":
		return status.Error(codes.FailedPrecondition, "cpu/memory requests are not supported; use limits")
	case spec.GetResourceRequirements().GetGpu() != nil && spec.GetResourceRequirements().GetGpu().Count != nil && spec.GetResourceRequirements().GetGpu().GetCount() > 1:
		// WSLc has only `--gpus all` (one device); like the Docker driver on
		// WSL, a count above 1 cannot be satisfied.
		return status.Errorf(codes.FailedPrecondition, "WSLc exposes one GPU (--gpus all); requested %d", spec.GetResourceRequirements().GetGpu().GetCount())
	}
	if lim := strings.TrimSpace(tmpl.GetResources().GetCpuLimit()); lim != "" {
		if _, err := cpuLimit(lim); err != nil {
			return status.Error(codes.FailedPrecondition, err.Error())
		}
	}
	if lim := strings.TrimSpace(tmpl.GetResources().GetMemoryLimit()); lim != "" {
		if _, err := memoryLimit(lim); err != nil {
			return status.Error(codes.FailedPrecondition, err.Error())
		}
	}
	_, err := parseDriverConfig(tmpl)
	return err
}

// --- naming ---------------------------------------------------------------

var nameSafe = regexp.MustCompile(`[^a-z0-9_.-]+`)

func nameSegment(s string) string {
	return strings.Trim(nameSafe.ReplaceAllString(strings.ToLower(s), "-"), "-")
}

// workloadName ports the Docker driver's container_name_for_sandbox:
// openshell-<ws>--<name>-<id>, at most 200 characters, truncating only the
// name (or, when the workspace and id leave no room, the whole).
func workloadName(sb *computev1.DriverSandbox) string {
	const max = 200
	ws, id, name := sanitizeName(sb.GetWorkspace()), sanitizeName(sb.GetId()), sanitizeName(sb.GetName())
	reserved := len("openshell-") + len(ws) + 2 + 1 + len(id)
	if name == "" || reserved >= max {
		base := "openshell-" + ws + "---" + id
		if len(base) > max {
			base = base[:max]
		}
		return strings.TrimRight(base, "-._")
	}
	if len(name) > max-reserved {
		name = strings.TrimRight(name[:max-reserved], "-._")
	}
	return "openshell-" + ws + "--" + name + "-" + id
}

// sanitizeName ports sanitize_docker_name: each character outside
// [A-Za-z0-9_.-] becomes one '-', letters are lowercased, and '-' is
// trimmed from both ends.
func sanitizeName(v string) string {
	var b strings.Builder
	for _, r := range v {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_', r == '.', r == '-':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r - 'A' + 'a')
		default:
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

func (d *driver) volumeNames(id string) (channel, supervisor string) {
	h := sha256.Sum256([]byte(d.cfg.Namespace + "\x00" + id))
	s := hex.EncodeToString(h[:])[:32]
	return "openshell-channel-" + s, "openshell-supervisor-" + s
}

func (d *driver) labels(sb *computev1.DriverSandbox, role string) map[string]string {
	return map[string]string{
		labelManagedBy: "openshell", labelID: sb.GetId(), labelName: sb.GetName(),
		labelWorkspace: sb.GetWorkspace(), labelNamespace: d.cfg.Namespace,
		labelBackend: isolationBackend, labelRole: role,
	}
}

func labelFlags(m map[string]string) []string {
	var a []string
	for _, k := range sortedKeys(m) {
		a = append(a, "--label", k+"="+m[k])
	}
	return a
}

func (d *driver) stateDir(id string) string {
	return filepath.Join(d.cfg.StateDir, nameSegment(d.cfg.Namespace), nameSegment(id))
}

// --- snapshots --------------------------------------------------------------

func condition(ready bool, reason, message string) *computev1.DriverCondition {
	s := "False"
	if ready {
		s = "True"
	}
	return &computev1.DriverCondition{Type: "Ready", Status: s, Reason: reason, Message: message, TransitionTime: timestamppb.Now()}
}

// snapshot maps a workload container to the gateway's view.
func (d *driver) snapshot(c containerInfo) *computev1.DriverSandbox {
	sb := &computev1.DriverSandbox{
		Id: c.label(labelID), Name: c.label(labelName), Namespace: c.label(labelNamespace), Workspace: c.label(labelWorkspace),
		Status: &computev1.DriverSandboxStatus{Name: strings.TrimPrefix(c.Name, "/"), InstanceId: c.ID},
	}
	var cond *computev1.DriverCondition
	switch c.State.Status {
	case "running":
		cond = condition(true, "BackendReady", "Container is running")
	case "created", "":
		cond = condition(false, "Starting", "Container created")
	case "restarting":
		cond = condition(false, "ContainerRestarting", "Container is restarting after a failure")
	case "removing":
		cond = condition(false, "Deleting", "Container is being removed")
		sb.Status.Deleting = true
	case "paused":
		cond = condition(false, "ContainerPaused", "Container is paused")
	case "dead":
		cond = condition(false, "ContainerDead", "Container is dead")
	default:
		cond = condition(false, "ContainerExited", fmt.Sprintf("Container exited (code %d)", c.State.ExitCode))
		// A signal exit is recoverable, unless the kernel's OOM killer sent
		// it (as the Docker driver classifies).
		if (c.State.ExitCode == 137 || c.State.ExitCode == 143) && !c.State.OOMKilled {
			cond = condition(false, "ContainerRuntimeRestart", fmt.Sprintf("Container terminated by signal (exit code %d)", c.State.ExitCode))
		}
	}
	d.mu.Lock()
	if msg, ok := d.failures[sb.Id]; ok && cond.Status == "True" {
		cond = condition(false, "ControlSupervisorExited", msg)
	}
	d.mu.Unlock()
	sb.Status.Conditions = []*computev1.DriverCondition{cond}
	return sb
}

func (d *driver) workloads(ctx context.Context) ([]containerInfo, error) {
	ids, err := d.w.list(ctx, map[string]string{labelManagedBy: "openshell", labelNamespace: d.cfg.Namespace, labelRole: "sandbox"})
	if err != nil {
		return nil, err
	}
	var out []containerInfo
	for _, id := range ids {
		c, err := d.w.container(ctx, id)
		if errors.Is(err, errNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if c.label(labelRole) == "sandbox" && c.label(labelNamespace) == d.cfg.Namespace {
			out = append(out, c)
		}
	}
	return out, nil
}

func (d *driver) snapshots(ctx context.Context) ([]*computev1.DriverSandbox, error) {
	cs, err := d.workloads(ctx)
	if err != nil {
		return nil, err
	}
	byID := map[string]*computev1.DriverSandbox{}
	for _, c := range cs {
		s := d.snapshot(c)
		byID[s.Id] = s
	}
	d.mu.Lock()
	for _, m := range []map[string]*computev1.DriverSandbox{d.failed, d.pending} {
		for id, p := range m {
			byID[id] = proto.Clone(p).(*computev1.DriverSandbox)
		}
	}
	d.mu.Unlock()
	out := make([]*computev1.DriverSandbox, 0, len(byID))
	for _, s := range byID {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Id < out[j].Id })
	return out, nil
}

func (d *driver) find(ctx context.Context, id, name string) (*containerInfo, error) {
	if id == "" && name == "" {
		return nil, status.Error(codes.InvalidArgument, "sandbox_id or name is required")
	}
	cs, err := d.workloads(ctx)
	if err != nil {
		return nil, status.Error(codes.Unavailable, err.Error())
	}
	for _, c := range cs {
		if (id != "" && c.label(labelID) == id) || (id == "" && c.label(labelName) == name) {
			return &c, nil
		}
	}
	return nil, nil
}

func (d *driver) GetSandbox(ctx context.Context, r *computev1.GetSandboxRequest) (*computev1.GetSandboxResponse, error) {
	d.mu.Lock()
	for _, m := range []map[string]*computev1.DriverSandbox{d.pending, d.failed} {
		if p, ok := m[r.GetSandboxId()]; ok {
			d.mu.Unlock()
			return &computev1.GetSandboxResponse{Sandbox: proto.Clone(p).(*computev1.DriverSandbox)}, nil
		}
	}
	d.mu.Unlock()
	c, err := d.find(ctx, r.GetSandboxId(), r.GetName())
	if err != nil {
		return nil, err
	}
	if c == nil {
		return nil, status.Error(codes.NotFound, "sandbox not found")
	}
	return &computev1.GetSandboxResponse{Sandbox: d.snapshot(*c)}, nil
}

func (d *driver) ListSandboxes(ctx context.Context, _ *computev1.ListSandboxesRequest) (*computev1.ListSandboxesResponse, error) {
	s, err := d.snapshots(ctx)
	if err != nil {
		return nil, status.Error(codes.Unavailable, err.Error())
	}
	return &computev1.ListSandboxesResponse{Sandboxes: s}, nil
}

// --- watch ----------------------------------------------------------------

func (d *driver) publish(ev *computev1.WatchSandboxesEvent) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if s := ev.GetSandbox().GetSandbox(); s != nil {
		d.previous[s.Id] = proto.Clone(s).(*computev1.DriverSandbox)
	}
	if id := ev.GetDeleted().GetSandboxId(); id != "" {
		delete(d.previous, id)
	}
	for ch := range d.subs {
		select {
		case ch <- ev:
		default: // a lagging watcher catches up from the next poll
		}
	}
}

func sandboxEvent(s *computev1.DriverSandbox) *computev1.WatchSandboxesEvent {
	return &computev1.WatchSandboxesEvent{Payload: &computev1.WatchSandboxesEvent_Sandbox{Sandbox: &computev1.WatchSandboxesSandboxEvent{Sandbox: s}}}
}

func (d *driver) platformEvent(id, typ, reason, msg string) {
	d.publish(&computev1.WatchSandboxesEvent{Payload: &computev1.WatchSandboxesEvent_PlatformEvent{PlatformEvent: &computev1.WatchSandboxesPlatformEvent{
		SandboxId: id, Event: &computev1.DriverPlatformEvent{EventTime: timestamppb.Now(), Source: "wslc", Type: typ, Reason: reason, Message: msg},
	}}})
}

func (d *driver) WatchSandboxes(_ *computev1.WatchSandboxesRequest, stream computev1.ComputeDriver_WatchSandboxesServer) error {
	ch := make(chan *computev1.WatchSandboxesEvent, 128)
	d.mu.Lock()
	d.subs[ch] = struct{}{}
	d.mu.Unlock()
	defer func() { d.mu.Lock(); delete(d.subs, ch); d.mu.Unlock() }()
	initial, err := d.snapshots(stream.Context())
	if err != nil {
		return status.Error(codes.Unavailable, err.Error())
	}
	for _, s := range initial {
		if err := stream.Send(sandboxEvent(s)); err != nil {
			return err
		}
	}
	for {
		select {
		case <-stream.Context().Done():
			return nil
		case ev := <-ch:
			if err := stream.Send(ev); err != nil {
				return err
			}
		}
	}
}

// pollLoop publishes changed and deleted snapshots and fails a sandbox
// closed when its supervisor exits unexpectedly (as the Docker driver does).
func (d *driver) pollLoop(ctx context.Context) {
	wait := pollInterval
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		cs, err := d.workloads(ctx)
		if err != nil {
			log.Printf("poll: %v", err)
			wait = min(wait*2, 30*time.Second)
			continue
		}
		wait = pollInterval
		seen := map[string]bool{}
		for _, c := range cs {
			d.superviseExit(ctx, c)
			s := d.snapshot(c)
			seen[s.Id] = true
			d.mu.Lock()
			_, provisioning := d.pending[s.Id]
			prev, starting := d.previous[s.Id], d.starting[s.Id]
			d.mu.Unlock()
			if !provisioning && !starting && (prev == nil || !sameSnapshot(prev, s)) {
				d.publish(sandboxEvent(s))
			}
		}
		d.mu.Lock()
		var gone []string
		for id := range d.previous {
			_, provisioning := d.pending[id]
			_, failed := d.failed[id]
			if !seen[id] && !provisioning && !failed {
				gone = append(gone, id)
			}
		}
		d.mu.Unlock()
		for _, id := range gone {
			d.publish(&computev1.WatchSandboxesEvent{Payload: &computev1.WatchSandboxesEvent_Deleted{Deleted: &computev1.WatchSandboxesDeletedEvent{SandboxId: id}}})
		}
	}
}

// sameSnapshot ignores condition transition times.
func sameSnapshot(a, b *computev1.DriverSandbox) bool {
	a, b = proto.Clone(a).(*computev1.DriverSandbox), proto.Clone(b).(*computev1.DriverSandbox)
	for _, s := range []*computev1.DriverSandbox{a, b} {
		for _, c := range s.GetStatus().GetConditions() {
			c.TransitionTime = nil
		}
	}
	return proto.Equal(a, b)
}

func (d *driver) superviseExit(ctx context.Context, c containerInfo) {
	id := c.label(labelID)
	d.mu.Lock()
	_, provisioning := d.pending[id]
	stopping, supervised, starting := d.stopping[id], d.supervised[id], d.starting[id]
	_, failed := d.failures[id]
	d.mu.Unlock()
	if !c.State.Running || provisioning || stopping || failed || !supervised || starting {
		return
	}
	sup, err := d.w.container(ctx, strings.TrimPrefix(c.Name, "/")+"-supervisor")
	if err == nil && sup.State.Running {
		return
	}
	if err != nil && !errors.Is(err, errNotFound) {
		return
	}
	// A StopSandbox racing the exit marks it intentional; give it a moment.
	select {
	case <-ctx.Done():
		return
	case <-time.After(supervisorExitGrace):
	}
	d.mu.Lock()
	stopping, supervised, starting = d.stopping[id], d.supervised[id], d.starting[id]
	d.mu.Unlock()
	if stopping || !supervised || starting {
		return
	}
	// A StartSandbox during the grace may have started a new supervisor.
	again, err2 := d.w.container(ctx, strings.TrimPrefix(c.Name, "/")+"-supervisor")
	if err2 == nil && again.State.Running {
		return
	}
	if err2 != nil && !errors.Is(err2, errNotFound) {
		return
	}
	sup, err = again, err2
	msg := "sandbox supervisor exited; stopping the isolated workload container"
	if err == nil {
		msg += ": " + d.w.logsTail(ctx, sup.ID, 80)
	}
	d.mu.Lock()
	d.failures[id] = msg
	d.mu.Unlock()
	d.platformEvent(id, "Warning", "ControlSupervisorExited", msg)
	_ = d.w.stop(ctx, c.ID, stopTimeoutSec)
}

// --- create -----------------------------------------------------------------

func (d *driver) CreateSandbox(ctx context.Context, r *computev1.CreateSandboxRequest) (*computev1.CreateSandboxResponse, error) {
	sb := r.GetSandbox()
	if sb == nil {
		return nil, status.Error(codes.InvalidArgument, "sandbox is required")
	}
	if err := d.validate(ctx, sb); err != nil {
		return nil, err
	}
	// The gateway mints launch credentials for create only, not validation.
	if _, err := decodeLaunchAuth(sb.GetSpec().GetLaunchAuthentication()); err != nil {
		return nil, status.Error(codes.FailedPrecondition, err.Error())
	}
	existing, err := d.find(ctx, sb.GetId(), sb.GetName())
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	_, pending := d.pending[sb.GetId()]
	_, failed := d.failed[sb.GetId()]
	if pending || failed || existing != nil {
		d.mu.Unlock()
		return nil, status.Error(codes.AlreadyExists, "sandbox already exists")
	}
	snap := &computev1.DriverSandbox{Id: sb.Id, Name: sb.Name, Namespace: d.cfg.Namespace, Workspace: sb.Workspace,
		Status: &computev1.DriverSandboxStatus{Name: workloadName(sb), Conditions: []*computev1.DriverCondition{condition(false, "Starting", "WSLc container is starting")}}}
	d.pending[sb.Id] = snap
	delete(d.failures, sb.Id)
	delete(d.stopping, sb.Id)
	d.mu.Unlock()
	d.platformEvent(sb.Id, "Normal", "Scheduled", "Scheduled on WSLc")
	d.publish(sandboxEvent(proto.Clone(snap).(*computev1.DriverSandbox)))
	go d.provision(proto.Clone(sb).(*computev1.DriverSandbox))
	return &computev1.CreateSandboxResponse{}, nil
}

// provision runs the create flow; any failure marks the sandbox failed and
// removes what was created.
func (d *driver) provision(sb *computev1.DriverSandbox) {
	// No deadline of the driver's own: the gateway owns it and cancels
	// through StopSandbox or DeleteSandbox.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.mu.Lock()
	d.cancels[sb.Id] = cancel
	d.mu.Unlock()
	began := time.Now()
	reason, err := d.provisionSteps(ctx, sb)
	if err == nil {
		log.Printf("provision %s: ready in %s", sb.Id, time.Since(began).Round(100*time.Millisecond))
	}
	d.mu.Lock()
	delete(d.pending, sb.Id)
	delete(d.cancels, sb.Id)
	abandoned, deleted := d.stopping[sb.Id], d.deleting[sb.Id]
	delete(d.deleting, sb.Id)
	d.mu.Unlock()
	if abandoned {
		// A run-once sandbox (--no-keep) is often deleted before the
		// health check here finishes; only a stop is worth noting.
		if err != nil && !deleted {
			log.Printf("provision %s: abandoned: %v", sb.Id, err)
		}
		// Stopped while provisioning: a created workload is kept, stopped,
		// as the Docker driver does; without one there is nothing to keep.
		if c, ierr := d.w.container(context.Background(), workloadName(sb)); !deleted && ierr == nil {
			d.stopSupervisor(context.Background(), strings.TrimPrefix(c.Name, "/"))
			_ = d.w.stop(context.Background(), c.ID, stopTimeoutSec)
			if c2, err := d.w.container(context.Background(), c.ID); err == nil {
				d.publish(sandboxEvent(d.snapshot(c2)))
			}
			return
		}
		d.cleanup(context.Background(), sb)
		d.mu.Lock()
		delete(d.stopping, sb.Id)
		delete(d.supervised, sb.Id)
		d.mu.Unlock()
		d.publish(&computev1.WatchSandboxesEvent{Payload: &computev1.WatchSandboxesEvent_Deleted{Deleted: &computev1.WatchSandboxesDeletedEvent{SandboxId: sb.Id}}})
		return
	}
	if err != nil {
		log.Printf("provision %s: %s: %v", sb.Id, reason, err)
		d.cleanup(context.Background(), sb)
		d.platformEvent(sb.Id, "Warning", reason, "WSLc sandbox provisioning failed: "+err.Error())
		failed := &computev1.DriverSandbox{Id: sb.Id, Name: sb.Name, Namespace: d.cfg.Namespace, Workspace: sb.Workspace,
			Status: &computev1.DriverSandboxStatus{Name: workloadName(sb), Conditions: []*computev1.DriverCondition{condition(false, reason, err.Error())}}}
		d.mu.Lock()
		d.failed[sb.Id] = failed
		d.mu.Unlock()
		d.publish(sandboxEvent(proto.Clone(failed).(*computev1.DriverSandbox)))
		return
	}
	d.platformEvent(sb.Id, "Normal", "Started", "Sandbox supervisor is ready")
	if c, err := d.w.container(ctx, workloadName(sb)); err == nil {
		d.publish(sandboxEvent(d.snapshot(c)))
	}
}

func (d *driver) provisionSteps(ctx context.Context, sb *computev1.DriverSandbox) (string, error) {
	spec, tmpl := sb.GetSpec(), sb.GetSpec().GetTemplate()
	auth, err := decodeLaunchAuth(spec.GetLaunchAuthentication())
	if err != nil {
		return "LaunchAuthenticationInvalid", err
	}
	ref := tmpl.GetImage()
	if ref == "" {
		ref = d.cfg.DefaultImage
	}
	img, err := d.ensureImage(ctx, ref)
	if err != nil {
		return "ImagePullFailed", err
	}
	imageUser, workdir := "", ""
	var imageVolumes []string
	if img.Config != nil {
		imageUser, workdir = img.Config.User, img.Config.WorkingDir
		for v := range img.Config.Volumes {
			imageVolumes = append(imageVolumes, v)
		}
	}
	workspace, err := resolveWorkspace(workdir)
	if err != nil {
		return "WorkspaceInvalid", err
	}
	dcfg, err := parseDriverConfig(tmpl)
	if err != nil {
		return "DriverConfigInvalid", err
	}
	userMounts, err := d.mountFlags(ctx, dcfg, workspace, imageVolumes)
	if err != nil {
		return "MountInvalid", err
	}
	id, err := d.identity(ctx, sb, img.ID, imageUser)
	if err != nil {
		return "IdentityResolutionFailed", err
	}
	binary, err := d.sandboxBinary(ctx)
	if err != nil {
		return "SandboxRuntimeUnavailable", err
	}
	supImg, err := d.ensureRuntimeImage(ctx, d.cfg.SupervisorImage)
	if err != nil {
		return "ImagePullFailed", err
	}
	channel, supVol := d.volumeNames(sb.Id)
	volLabels := map[string]string{labelManagedBy: "openshell", labelID: sb.Id, labelNamespace: d.cfg.Namespace, labelBackend: isolationBackend}
	for _, v := range []string{channel, supVol} {
		if err := d.w.volumeCreate(ctx, v, volLabels); err != nil {
			return "BoundaryChannelCreateFailed", err
		}
	}

	name := workloadName(sb)
	// The template's labels, with OpenShell's own on top (as upstream).
	workloadLabels := map[string]string{}
	for k, v := range tmpl.GetLabels() {
		workloadLabels[k] = v
	}
	for k, v := range d.labels(sb, "sandbox") {
		workloadLabels[k] = v
	}
	gpu := spec.GetResourceRequirements().GetGpu() != nil
	if gpu {
		workloadLabels[labelGPU] = "true"
	}
	flags := append([]string{"--name", name}, labelFlags(workloadLabels)...)
	flags = append(flags,
		"--network", "none",
		"--user", "0:0", // launch-capability-free drops to the identity
		"--workdir", "/",
		"--entrypoint", sandboxBinaryPath,
		"--env", "OPENSHELL_LOG_LEVEL="+firstNonEmpty(spec.GetLogLevel(), d.cfg.LogLevel),
		"--env", "OPENSHELL_TELEMETRY_ENABLED=false",
		"--mount", "type=volume,source="+channel+",target="+channelMount,
		"--tmpfs", fmt.Sprintf("%s:rw,noexec,nosuid,nodev,size=1m,uid=%d,gid=%d,mode=0755", caMaterialTmpfs, id.UID, id.GID),
		"--dns", "127.0.0.53", "--dns-search", ".",
		"--pull", "never")
	flags = append(flags, userMounts...)
	flags = append(flags, memoryDeviceFlags(gpu)...)
	if lim := strings.TrimSpace(tmpl.GetResources().GetCpuLimit()); lim != "" {
		v, err := cpuLimit(lim)
		if err != nil {
			return "ContainerCreateFailed", err
		}
		flags = append(flags, "--cpus", v)
	}
	if lim := strings.TrimSpace(tmpl.GetResources().GetMemoryLimit()); lim != "" {
		v, err := memoryLimit(lim)
		if err != nil {
			return "ContainerCreateFailed", err
		}
		flags = append(flags, "--memory", v)
	}
	args := []string{"launch-capability-free", fmt.Sprint(id.UID), fmt.Sprint(id.GID), bootstrapPath}
	if workspace == defaultWorkspace {
		args = append(args, defaultWorkspace)
	}
	if d.cfg.ShellInit {
		for i, f := range flags {
			if f == sandboxBinaryPath && i > 0 && flags[i-1] == "--entrypoint" {
				flags[i] = "/bin/sh"
			}
		}
		args = append([]string{"-c", shellInit, "openshell-init", sandboxBinaryPath}, args...)
	}
	cid, err := d.w.create(ctx, flags, img.ID, args...)
	if err != nil {
		return "ContainerCreateFailed", err
	}
	c, err := d.w.container(ctx, cid)
	if err != nil {
		return "OuterFenceInspectFailed", err
	}
	if err := checkOuterFence(c); err != nil {
		return "OuterFenceRejected", err
	}
	d.platformEvent(sb.Id, "Normal", "Created", "WSLc container created")

	files, err := buildBoundary(sb.Id, auth, cid, img.ID, id, childEnv(spec), gpu)
	if err != nil {
		return "BoundaryConfigWriteFailed", err
	}
	state := map[string][]byte{
		"boundary-bootstrap.json": files.Bootstrap, "boundary-server.crt": []byte(files.TLS.CertPEM),
		"boundary-server.key": []byte(files.TLS.KeyPEM), "runtime-descriptor.json": files.Descriptor,
		"supervisor-auth.json": files.SupervisorAuth, "workspace-root": []byte(workspace),
		"main-process.json": mainProcessSpec(spec), "supervisor-image": []byte(supImg.ID),
		"identity":       []byte(fmt.Sprintf("%d:%d", id.UID, id.GID)),
		"log-level":      []byte(firstNonEmpty(spec.GetLogLevel(), d.cfg.LogLevel)),
		"volume-targets": []byte(strings.Join(writableVolumeTargets(dcfg), "\n")),
	}
	if err := d.writeState(sb.Id, state); err != nil {
		return "BoundaryStateCreateFailed", err
	}
	if err := d.stageWorkload(ctx, cid, id, binary, files, workspace == defaultWorkspace); err != nil {
		return "BoundaryConfigWriteFailed", err
	}
	if err := d.w.start(ctx, cid); err != nil {
		return "ContainerStartFailed", err
	}
	d.ownVolumes(ctx, sb.Id, cid)
	if err := d.startSupervisor(ctx, sb, name); err != nil {
		return "ControlSupervisorStartFailed", err
	}
	return "", nil
}

// checkOuterFence checks what the bootstrap claims (default-deny egress): the
// workload has no network at all (validate_docker_outer_fence).
func checkOuterFence(c containerInfo) error {
	if c.HostConfig.NetworkMode != "none" {
		return fmt.Errorf("workload outer fence requires network mode none, got %q", c.HostConfig.NetworkMode)
	}
	for n := range c.NetworkSettings.Networks {
		if n != "none" {
			return fmt.Errorf("workload is attached to network %q", n)
		}
	}
	return nil
}

// ensureImage applies the pull policy to a workload image.
func (d *driver) ensureImage(ctx context.Context, ref string) (imageInfo, error) {
	return d.pullImage(ctx, ref, d.cfg.PullPolicy)
}

// ensureRuntimeImage gets the supervisor or sandbox runtime image, as the
// Docker driver does: pulled when missing whatever the policy, and a
// latest or dev tag refreshed, falling back to the local image when that
// pull fails.
func (d *driver) ensureRuntimeImage(ctx context.Context, ref string) (imageInfo, error) {
	if tag := imageTag(ref); tag == "latest" || tag == "dev" {
		if _, err := d.w.r.Run(ctx, nil, "image", "pull", ref); err != nil {
			if img, ierr := d.w.image(ctx, ref); ierr == nil {
				log.Printf("refresh %s failed, using the local image: %v", ref, err)
				return img, nil
			}
			return imageInfo{}, err
		}
		return d.w.image(ctx, ref)
	}
	return d.w.ensureImage(ctx, ref)
}

// imageTag ports supervisor_image_tag: the tag of the last path segment,
// "latest" when there is none, and "" for a digest or an empty tag.
func imageTag(ref string) string {
	if strings.Contains(ref, "@") {
		return ""
	}
	name := ref[strings.LastIndex(ref, "/")+1:]
	if i := strings.LastIndex(name, ":"); i >= 0 {
		return name[i+1:]
	}
	return "latest"
}

func (d *driver) pullImage(ctx context.Context, ref, policy string) (imageInfo, error) {
	switch policy {
	case "always":
		if _, err := d.w.r.Run(ctx, nil, "image", "pull", ref); err != nil {
			return imageInfo{}, err
		}
		return d.w.image(ctx, ref)
	case "never":
		img, err := d.w.image(ctx, ref)
		if errors.Is(err, errNotFound) {
			return img, fmt.Errorf("image %s is not present and the pull policy is never", ref)
		}
		return img, err
	}
	return d.w.ensureImage(ctx, ref)
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// childEnv is the template environment overlaid by the spec's, without the
// keys OpenShell reserves for itself.
func childEnv(spec *computev1.DriverSandboxSpec) map[string]string {
	env := map[string]string{}
	for k, v := range spec.GetTemplate().GetEnvironment() {
		env[k] = v
	}
	for k, v := range spec.GetEnvironment() {
		env[k] = v
	}
	for _, k := range []string{"OPENSHELL_ENDPOINT", "OPENSHELL_GATEWAY_TLS_SERVER_NAME", "OPENSHELL_MAIN_PROCESS_SPEC",
		"OPENSHELL_NETWORK_RUNTIME_CAPABILITIES", "OPENSHELL_OCI_IMAGE_USER", "OPENSHELL_SANDBOX", "OPENSHELL_SANDBOX_GID",
		"OPENSHELL_SANDBOX_ID", "OPENSHELL_SANDBOX_TOKEN", "OPENSHELL_SANDBOX_TOKEN_FILE", "OPENSHELL_SANDBOX_UID",
		"OPENSHELL_SSH_SOCKET_PATH", "OPENSHELL_TLS_CA", "OPENSHELL_TLS_CERT", "OPENSHELL_TLS_KEY", "OPENSHELL_USER_ENVIRONMENT"} {
		delete(env, k)
	}
	return env
}

func (d *driver) identity(ctx context.Context, sb *computev1.DriverSandbox, imageID, imageUser string) (identity, error) {
	req := sb.GetSpec().GetWorkloadIdentity()
	var passwd, group []byte
	if req.GetUser() != "" || req.GetGroup() != "" || imageUser != "" {
		d.mu.Lock()
		cached, ok := d.accounts[imageID]
		d.mu.Unlock()
		if ok {
			return resolveIdentity(req.GetUser(), req.GetGroup(), imageUser, imageID, cached[0], cached[1])
		}
		name := workloadName(sb) + "-identity"
		_ = d.w.remove(ctx, name)
		flags := append([]string{"--name", name}, labelFlags(d.labels(sb, "identity"))...)
		cid, err := d.w.create(ctx, append(flags, "--network", "none", "--pull", "never"), imageID)
		if err != nil {
			return identity{}, err
		}
		defer d.w.remove(context.Background(), cid)
		if passwd, err = d.w.copyOut(ctx, cid, "/etc/passwd"); err != nil {
			return identity{}, fmt.Errorf("read /etc/passwd: %w", err)
		}
		if group, err = d.w.copyOut(ctx, cid, "/etc/group"); err != nil {
			return identity{}, fmt.Errorf("read /etc/group: %w", err)
		}
		// An image ID names fixed content, so its accounts never change.
		d.mu.Lock()
		d.accounts[imageID] = [2][]byte{passwd, group}
		d.mu.Unlock()
	}
	return resolveIdentity(req.GetUser(), req.GetGroup(), imageUser, imageID, passwd, group)
}

// sandboxBinary extracts /openshell-sandbox from the sandbox runtime image
// once per driver process.
func (d *driver) sandboxBinary(ctx context.Context) ([]byte, error) {
	d.mu.Lock()
	b := d.binary
	d.mu.Unlock()
	if b != nil {
		return b, nil
	}
	img, err := d.ensureRuntimeImage(ctx, d.cfg.SandboxImage)
	if err != nil {
		return nil, err
	}
	name := fmt.Sprintf("openshell-wslc-extract-%d", time.Now().UnixNano())
	cid, err := d.w.create(ctx, []string{"--name", name, "--label", labelManagedBy + "=openshell", "--label", labelRole + "=staging",
		"--label", labelNamespace + "=" + d.cfg.Namespace, "--network", "none", "--pull", "never", "--entrypoint", "/openshell-sandbox"}, img.ID)
	if err != nil {
		return nil, err
	}
	defer d.w.remove(context.Background(), cid)
	b, err = d.w.copyOut(ctx, cid, "/openshell-sandbox")
	if err != nil {
		return nil, err
	}
	if len(b) < 4 || string(b[:4]) != "\x7fELF" {
		return nil, errors.New("sandbox runtime image did not yield an ELF /openshell-sandbox")
	}
	d.mu.Lock()
	d.binary = b
	d.mu.Unlock()
	return b, nil
}

// stageWorkload copies the runtime binary (root, 0555) and the boundary
// files (the identity's, 0600) into the created workload; the channel
// entries land in the channel volume.
func (d *driver) stageWorkload(ctx context.Context, cid string, id identity, binary []byte, f boundaryFiles, defaultWS bool) error {
	if id.UID > math.MaxInt32 || id.GID > math.MaxInt32 {
		return fmt.Errorf("uid %d or gid %d out of range", id.UID, id.GID)
	}
	u, g := int(id.UID), int(id.GID)
	entries := []tarEntry{
		{name: ".openshell", dir: true, mode: 0o755},
		{name: ".openshell/runtime", dir: true, mode: 0o555},
		{name: ".openshell/runtime/openshell-sandbox", mode: 0o555, data: binary},
		{name: ".openshell/channel", dir: true, mode: 0o755},
		{name: ".openshell/channel/sandbox", dir: true, mode: 0o711, uid: u, gid: g},
		{name: ".openshell/channel/sandbox/bootstrap.json", mode: 0o600, uid: u, gid: g, data: f.Bootstrap},
		{name: ".openshell/channel/sandbox/server.crt", mode: 0o600, uid: u, gid: g, data: []byte(f.TLS.CertPEM)},
		{name: ".openshell/channel/sandbox/server.key", mode: 0o600, uid: u, gid: g, data: []byte(f.TLS.KeyPEM)},
	}
	if defaultWS {
		entries = append(entries, tarEntry{name: "sandbox", dir: true, mode: 0o700, uid: u, gid: g})
	}
	archive, err := buildTar(entries)
	if err != nil {
		return err
	}
	return d.w.copyIn(ctx, cid, "/", archive)
}

// legacyTermEntries place Iwato's daemon at termd.LegacyPath, root's and
// read-only, for a sandbox whose main process (mainProcess, its
// main-process.json) runs it from there. The driver places it on each
// start until the sandbox is recreated. Only into a workload that does not
// run: OpenShell pins the hash of each executable in a sandbox's
// connections, so a daemon replaced while it runs would cut its terminals'
// network.
func (d *driver) legacyTermEntries(mainProcess []byte) []tarEntry {
	var spec struct {
		Command []string `json:"command"`
	}
	if len(d.cfg.LegacyTerm) == 0 || json.Unmarshal(mainProcess, &spec) != nil || len(spec.Command) == 0 || spec.Command[0] != termd.LegacyPath {
		return nil
	}
	dir := strings.TrimPrefix(path.Dir(termd.LegacyPath), "/")
	return []tarEntry{
		{name: dir, dir: true, mode: 0o755},
		{name: strings.TrimPrefix(termd.LegacyPath, "/"), mode: 0o555, data: d.cfg.LegacyTerm},
	}
}

// startSupervisor stages the supervisor volume through a throwaway
// container (the supervisor mounts it read-only), then creates and starts
// the supervisor on the network it shares with the gateway and waits for
// its health check.
func (d *driver) startSupervisor(ctx context.Context, sb *computev1.DriverSandbox, workload string) error {
	st, err := d.readState(sb.Id, "runtime-descriptor.json", "supervisor-auth.json", "workspace-root", "main-process.json", "supervisor-image")
	if err != nil {
		return err
	}
	_, supVol := d.volumeNames(sb.Id)
	channel, _ := d.volumeNames(sb.Id)
	supImage := string(st["supervisor-image"])
	stage := workload + "-supervisor-stage"
	_ = d.w.remove(ctx, stage)
	sflags := append([]string{"--name", stage}, labelFlags(d.labels(sb, "staging"))...)
	sflags = append(sflags, "--user", fmt.Sprintf("%d:%d", supervisorUID, supervisorUID), "--network", "none",
		"--mount", "type=volume,source="+supVol+",target="+supervisorMount, "--entrypoint", supervisorBinary, "--pull", "never")
	scid, err := d.w.create(ctx, sflags, supImage)
	if err != nil {
		return err
	}
	entries := []tarEntry{
		{name: "runtime-descriptor.json", mode: 0o600, uid: supervisorUID, gid: supervisorUID, data: st["runtime-descriptor.json"]},
		{name: "auth.json", mode: 0o600, uid: supervisorUID, gid: supervisorUID, data: st["supervisor-auth.json"]},
	}
	tlsEnv, tlsEntries, err := d.guestTLS()
	if err != nil {
		return err
	}
	archive, err := buildTar(append(entries, tlsEntries...))
	if err == nil {
		err = d.w.copyIn(ctx, scid, supervisorMount, archive)
	}
	_ = d.w.remove(context.Background(), scid)
	if err != nil {
		return err
	}

	sup := workload + "-supervisor"
	d.stopSupervisor(ctx, workload)
	flags := append([]string{"--name", sup}, labelFlags(d.labels(sb, "supervisor"))...)
	run := fmt.Sprintf("rw,noexec,nosuid,size=64m,uid=%d,gid=%d,mode=0700", supervisorUID, supervisorUID)
	flags = append(flags,
		"--network", d.cfg.Network,
		"--user", fmt.Sprintf("%d:%d", supervisorUID, supervisorUID),
		"--entrypoint", supervisorBinary,
		"--mount", "type=volume,source="+channel+",target="+channelMount+",readonly",
		"--mount", "type=volume,source="+supVol+",target="+supervisorMount+",readonly",
		"--tmpfs", "/run:"+run, "--tmpfs", "/var/log:"+run,
		"--tmpfs", fmt.Sprintf("/tmp:rw,noexec,nosuid,size=64m,uid=%d,gid=%d,mode=1777", supervisorUID, supervisorUID),
		"--pull", "never")
	for _, kv := range [][2]string{
		{"OPENSHELL_ADMITTED_ISOLATION_BACKEND", isolationBackend},
		{"OPENSHELL_MAIN_PROCESS_SPEC", "base64url:" + base64url(st["main-process.json"])},
		{"OPENSHELL_ENDPOINT", d.cfg.Endpoint},
		{"OPENSHELL_SANDBOX_ID", sb.Id},
		{"OPENSHELL_SANDBOX", sb.Name},
		{"OPENSHELL_SSH_SOCKET_PATH", "/run/openshell/ssh.sock"},
		{"OPENSHELL_PROXY_TLS_DIR", "/run/openshell/proxy-tls"},
		{"OPENSHELL_NETWORK_RUNTIME_CAPABILITIES", networkCapabilities},
		{"OPENSHELL_LOG_LEVEL", d.supervisorLogLevel(sb.Id)},
		{"OPENSHELL_TELEMETRY_ENABLED", "false"},
	} {
		flags = append(flags, "--env", kv[0]+"="+kv[1])
	}
	flags = append(flags, tlsEnv...)
	cid, err := d.w.create(ctx, flags, supImage,
		"--backend-descriptor-file", supervisorMount+"/runtime-descriptor.json",
		"--auth-bundle-file", supervisorMount+"/auth.json",
		"--workdir", string(st["workspace-root"]),
		"--health-socket-path="+supervisorHealth)
	if err != nil {
		return err
	}
	if err := d.w.start(ctx, cid); err != nil {
		return err
	}
	if err := d.waitReady(ctx, workload, cid); err != nil {
		return err
	}
	d.mu.Lock()
	d.supervised[sb.Id] = true
	d.mu.Unlock()
	return nil
}

// stopSupervisor ends a workload's supervisor as the Docker driver does:
// SIGTERM with a grace, so that it ends the workload through the sandbox,
// then removal. Removed outright, it can leave a Wait pending in the
// sandbox, which then hangs after SIGTERM (docs/dev/openshell.md).
func (d *driver) stopSupervisor(ctx context.Context, workload string) {
	sup := workload + "-supervisor"
	if c, err := d.w.container(ctx, sup); err == nil && c.State.Running {
		_ = d.w.stop(ctx, sup, supervisorStopSec)
	}
	_ = d.w.remove(ctx, sup)
}

// supervisorLogLevel is the sandbox's log level (spec, else the driver's),
// kept in its state for restarts.
func (d *driver) supervisorLogLevel(id string) string {
	if st, err := d.readState(id, "log-level"); err == nil && len(st["log-level"]) > 0 {
		return string(st["log-level"])
	}
	return d.cfg.LogLevel
}

// guestTLS stages the gateway CA (a configured file, never generated here)
// and points the supervisor at it.
func (d *driver) guestTLS() ([]string, []tarEntry, error) {
	if d.cfg.GuestTLSCA == "" {
		return nil, nil, nil
	}
	b, err := os.ReadFile(d.cfg.GuestTLSCA)
	if err != nil {
		return nil, nil, err
	}
	entries := []tarEntry{
		{name: "tls", dir: true, mode: 0o700, uid: supervisorUID, gid: supervisorUID},
		{name: "tls/ca.pem", mode: 0o600, uid: supervisorUID, gid: supervisorUID, data: b},
	}
	env := []string{"--env", "OPENSHELL_TLS_CA=" + supervisorMount + "/tls/ca.pem"}
	// A sandbox created before OpenShell v0.1.4 keeps its v0.1.2 supervisor
	// image, which requires a client certificate: its volume still holds
	// the one staged then. Supervisors since v0.1.4 ignore these variables.
	for _, f := range []string{"cert", "key"} {
		env = append(env, "--env", "OPENSHELL_TLS_"+strings.ToUpper(f)+"="+supervisorMount+"/tls/"+f+".pem")
	}
	return env, entries, nil
}

// reconcile removes leftovers of an interrupted driver: staging, identity
// and extractor containers, and volumes no workload uses. Supervisors keep
// running (they talk to the gateway directly) and are watched again.
func (d *driver) reconcile(ctx context.Context) {
	for _, role := range []string{"staging", "identity"} {
		ids, err := d.w.list(ctx, map[string]string{labelManagedBy: "openshell", labelRole: role, labelNamespace: d.cfg.Namespace})
		if err != nil {
			log.Printf("reconcile: %v", err)
			return
		}
		for _, id := range ids {
			_ = d.w.remove(ctx, id)
		}
	}
	live := map[string]bool{}
	cs, err := d.workloads(ctx)
	if err != nil {
		log.Printf("reconcile: %v", err)
		return
	}
	for _, c := range cs {
		id := c.label(labelID)
		live[id] = true
		// A supervisor started by an earlier driver process is watched too,
		// so its exit still fails the sandbox closed.
		if c.State.Running {
			if sup, err := d.w.container(ctx, strings.TrimPrefix(c.Name, "/")+"-supervisor"); err == nil && sup.State.Running {
				d.mu.Lock()
				d.supervised[id] = true
				d.mu.Unlock()
			}
		}
	}
	out, err := d.w.r.Run(ctx, nil, "volume", "list", "--quiet", "--filter", "label="+labelManagedBy+"=openshell", "--filter", "label="+labelNamespace+"="+d.cfg.Namespace)
	if err != nil {
		log.Printf("reconcile: %v", err)
		return
	}
	for _, name := range strings.Fields(string(out)) {
		var v struct {
			Labels map[string]string `json:"Labels"`
		}
		if d.w.inspect(ctx, "volume", name, &v) == nil && v.Labels[labelID] != "" && !live[v.Labels[labelID]] {
			_ = d.w.volumeRemove(ctx, name)
			_ = os.RemoveAll(d.stateDir(v.Labels[labelID]))
		}
	}
}

// statusTail keeps the end of a log tail that goes into a gRPC status
// message: those travel in HTTP/2 headers, and a long one (up to 3x by
// percent-encoding) can exceed the gateway's header budget and hide the
// error behind PROTOCOL_ERROR (the Docker driver's format_log_tail).
func statusTail(tail string) string {
	const max = 1024
	if len(tail) <= max {
		return tail
	}
	start := len(tail) - max
	for start < len(tail) && !utf8.RuneStart(tail[start]) {
		start++
	}
	return "[truncated] " + tail[start:]
}

// waitReady polls the supervisor's health subcommand (WSLc's health check
// would need a shell in the supervisor image).
func (d *driver) waitReady(ctx context.Context, workload, sup string) error {
	for {
		probe, cancel := context.WithTimeout(ctx, healthProbeTimeout)
		err := d.w.exec(probe, sup, supervisorBinary, "health", "--socket", supervisorHealth)
		cancel()
		if err == nil {
			return nil
		}
		// Not the workload's log: since OpenShell v0.1.4 it carries the
		// agent's output, which must not reach the gateway's status and
		// events (as the Docker driver, NVIDIA/OpenShell#4005).
		w, err := d.w.container(ctx, workload)
		if err != nil || !w.State.Running {
			return errors.New("sandbox exited before the supervisor became ready")
		}
		s, err := d.w.container(ctx, sup)
		if err != nil || !s.State.Running {
			return fmt.Errorf("supervisor stopped before it became ready: %s", statusTail(d.w.logsTail(ctx, sup, 80)))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func (d *driver) writeState(id string, files map[string][]byte) error {
	dir := d.stateDir(id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	for name, b := range files {
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o600); err != nil {
			return err
		}
	}
	return nil
}

func (d *driver) readState(id string, names ...string) (map[string][]byte, error) {
	out := map[string][]byte{}
	for _, n := range names {
		b, err := os.ReadFile(filepath.Join(d.stateDir(id), n))
		if err != nil {
			return nil, fmt.Errorf("sandbox state %s: %w", n, err)
		}
		out[n] = b
	}
	return out, nil
}

// --- stop / start / delete ----------------------------------------------------

func (d *driver) StopSandbox(ctx context.Context, r *computev1.StopSandboxRequest) (*computev1.StopSandboxResponse, error) {
	if d.abandonPending(r.GetSandboxId(), false) {
		return &computev1.StopSandboxResponse{}, nil
	}
	c, err := d.find(ctx, r.GetSandboxId(), r.GetName())
	if err != nil {
		return nil, err
	}
	if c == nil {
		return nil, status.Error(codes.NotFound, "sandbox not found")
	}
	id := c.label(labelID)
	d.mu.Lock()
	d.stopping[id] = true
	delete(d.supervised, id)
	d.mu.Unlock()
	// The supervisor first, gracefully, while the sandbox's runtime runs.
	d.stopSupervisor(ctx, strings.TrimPrefix(c.Name, "/"))
	if err := d.w.stop(ctx, c.ID, stopTimeoutSec); err != nil {
		return nil, status.Error(codes.Unavailable, err.Error())
	}
	if c2, err := d.w.container(ctx, c.ID); err == nil {
		d.publish(sandboxEvent(d.snapshot(c2)))
	}
	return &computev1.StopSandboxResponse{}, nil
}

func (d *driver) StartSandbox(ctx context.Context, r *computev1.StartSandboxRequest) (*computev1.StartSandboxResponse, error) {
	if r.GetIsolationBackend() != nil {
		return nil, status.Error(codes.FailedPrecondition, "external isolation backends are not supported")
	}
	c, err := d.find(ctx, r.GetSandboxId(), r.GetName())
	if err != nil {
		return nil, err
	}
	if c == nil {
		return nil, status.Error(codes.NotFound, "sandbox not found")
	}
	id := c.label(labelID)
	sb := &computev1.DriverSandbox{Id: id, Name: c.label(labelName), Workspace: c.label(labelWorkspace)}
	name := strings.TrimPrefix(c.Name, "/")
	d.mu.Lock()
	delete(d.stopping, id)
	delete(d.failures, id)
	d.starting[id] = true
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		delete(d.starting, id)
		d.mu.Unlock()
	}()
	// As the Docker driver: the outer fence is checked on every start.
	if err := checkOuterFence(*c); err != nil {
		return nil, status.Error(codes.FailedPrecondition, err.Error())
	}
	auth := r.GetLaunchAuthentication()
	// A running workload cannot take a new supervisor: its runtime stays
	// bound to the supervisor instance it attached first ("already bound to
	// another supervisor process"). A start with launch authentication on
	// one (the gateway starting a fresh instance of a Completed or
	// MainProcessFailed sandbox) restarts it as a new session instead, like
	// a stopped one.
	if c.State.Running && len(auth) > 0 {
		d.stopSupervisor(ctx, name)
		if err := d.w.stop(ctx, c.ID, stopTimeoutSec); err != nil {
			return nil, status.Error(codes.Unavailable, err.Error())
		}
		again, err := d.w.container(ctx, c.ID)
		if err != nil {
			return nil, status.Error(codes.Unavailable, err.Error())
		}
		c = &again
	}
	if c.State.Running {
		// A retry of the start that is running carries its generation; a
		// different one means another start won (adopt_or_verify).
		if err := d.adoptGeneration(id, r.GetGenerationId()); err != nil {
			return nil, err
		}
		if len(auth) > 0 {
			a, err := decodeLaunchAuth(auth)
			if err != nil {
				return nil, status.Error(codes.FailedPrecondition, err.Error())
			}
			// A running workload keeps its session; only the supervisor's
			// credentials change.
			if err := d.writeState(id, map[string][]byte{"supervisor-auth.json": a.Supervisor}); err != nil {
				return nil, status.Error(codes.Internal, err.Error())
			}
		} else if sup, err := d.w.container(ctx, name+"-supervisor"); err == nil && sup.State.Running {
			// Its exit fails the sandbox closed from now on, also when
			// another driver process started it.
			d.mu.Lock()
			d.supervised[id] = true
			d.mu.Unlock()
			return &computev1.StartSandboxResponse{}, nil
		}
		if err := d.startSupervisor(ctx, sb, name); err != nil {
			return nil, d.failClosed(ctx, id, c.ID, "ControlSupervisorStartFailed", err)
		}
		return &computev1.StartSandboxResponse{}, nil
	}
	// A stopped workload starts a new session: rotate the boundary TLS and
	// the bootstrap's session fields, restage them, then start.
	if len(auth) == 0 {
		return nil, status.Error(codes.FailedPrecondition, "starting a stopped sandbox needs launch authentication")
	}
	if g := r.GetGenerationId(); g != "" {
		if err := d.writeState(id, map[string][]byte{"start-generation": []byte(g)}); err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}
	}
	if err := d.rotate(ctx, c, auth); err != nil {
		return nil, status.Error(codes.FailedPrecondition, err.Error())
	}
	if err := d.w.start(ctx, c.ID); err != nil {
		return nil, status.Error(codes.Unavailable, err.Error())
	}
	d.ownVolumes(ctx, id, c.ID)
	if err := d.startSupervisor(ctx, sb, name); err != nil {
		return nil, d.failClosed(ctx, id, c.ID, "ControlSupervisorStartFailed", err)
	}
	return &computev1.StartSandboxResponse{}, nil
}

// failClosed stops a workload left without a supervisor, records why and
// returns the error for the caller (the Docker driver's runtime failure).
func (d *driver) failClosed(ctx context.Context, id, workload, reason string, err error) error {
	msg := "sandbox supervisor did not start; stopping the isolated workload container: " + err.Error()
	d.mu.Lock()
	d.failures[id] = msg
	d.mu.Unlock()
	d.platformEvent(id, "Warning", reason, msg)
	// Not the request's context: a cancelled StartSandbox must still stop
	// the workload, or it runs on without a supervisor.
	stop, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	defer cancel()
	if c, err := d.w.container(stop, workload); err == nil {
		d.stopSupervisor(stop, strings.TrimPrefix(c.Name, "/"))
	}
	_ = d.w.stop(stop, workload, stopTimeoutSec)
	return status.Error(codes.Unavailable, msg)
}

// adoptGeneration records the running start's generation when none is
// known, else verifies it.
func (d *driver) adoptGeneration(id, generation string) error {
	if generation == "" {
		return nil
	}
	if st, err := d.readState(id, "start-generation"); err == nil {
		if active := string(st["start-generation"]); active != generation {
			return status.Errorf(codes.FailedPrecondition, "sandbox is running start generation %s, not %s", active, generation)
		}
		return nil
	}
	if err := d.writeState(id, map[string][]byte{"start-generation": []byte(generation)}); err != nil {
		return status.Error(codes.Internal, err.Error())
	}
	return nil
}

// abandonPending cancels a sandbox still provisioning; provision removes
// what it made and reports the deletion.
func (d *driver) abandonPending(id string, deleting bool) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.pending[id]; !ok {
		return false
	}
	d.stopping[id] = true
	d.deleting[id] = d.deleting[id] || deleting
	if cancel := d.cancels[id]; cancel != nil {
		cancel()
	}
	return true
}

func (d *driver) DeleteSandbox(ctx context.Context, r *computev1.DeleteSandboxRequest) (*computev1.DeleteSandboxResponse, error) {
	if d.abandonPending(r.GetSandboxId(), true) {
		return &computev1.DeleteSandboxResponse{Deleted: true}, nil
	}
	c, err := d.find(ctx, r.GetSandboxId(), r.GetName())
	if err != nil {
		return nil, err
	}
	var sb *computev1.DriverSandbox
	d.mu.Lock()
	failed := d.failed[r.GetSandboxId()]
	d.mu.Unlock()
	switch {
	case c != nil:
		sb = &computev1.DriverSandbox{Id: c.label(labelID), Name: c.label(labelName), Workspace: c.label(labelWorkspace)}
	case failed != nil:
		sb = failed
	case r.GetSandboxId() != "":
		// The workload is gone but a supervisor, volumes or state may
		// remain; the Docker driver cleans those up by id too.
		removed := d.cleanup(ctx, &computev1.DriverSandbox{Id: r.GetSandboxId()})
		return &computev1.DeleteSandboxResponse{Deleted: removed}, nil
	default:
		return &computev1.DeleteSandboxResponse{Deleted: false}, nil
	}
	d.mu.Lock()
	d.stopping[sb.Id] = true
	d.mu.Unlock()
	d.cleanup(ctx, sb)
	d.mu.Lock()
	delete(d.stopping, sb.Id)
	delete(d.failures, sb.Id)
	delete(d.supervised, sb.Id)
	delete(d.failed, sb.Id)
	d.mu.Unlock()
	d.publish(&computev1.WatchSandboxesEvent{Payload: &computev1.WatchSandboxesEvent_Deleted{Deleted: &computev1.WatchSandboxesDeletedEvent{SandboxId: sb.Id}}})
	return &computev1.DeleteSandboxResponse{Deleted: true}, nil
}

// cleanup removes every container, volume and state file of a sandbox and
// reports whether there was any. A sandbox known only by id (its workload
// gone) is found by its labels and volume names.
func (d *driver) cleanup(ctx context.Context, sb *computev1.DriverSandbox) bool {
	found := false
	if ids, err := d.w.list(ctx, map[string]string{labelManagedBy: "openshell", labelID: sb.Id, labelNamespace: d.cfg.Namespace}); err == nil {
		for _, id := range ids {
			found = true
			_ = d.w.remove(ctx, id)
		}
	}
	if sb.Name != "" {
		name := workloadName(sb)
		for _, n := range []string{name + "-supervisor", name + "-supervisor-stage", name + "-identity", name} {
			_ = d.w.remove(ctx, n)
		}
	}
	channel, supVol := d.volumeNames(sb.Id)
	for _, v := range []string{channel, supVol} {
		var info map[string]any
		if d.w.inspect(ctx, "volume", v, &info) == nil {
			found = true
			_ = d.w.volumeRemove(ctx, v)
		}
	}
	if _, err := os.Stat(d.stateDir(sb.Id)); err == nil {
		found = true
		_ = os.RemoveAll(d.stateDir(sb.Id))
	}
	return found
}

// shmSize is /dev/shm of every sandbox. WSLc's default is Docker's 64 MB,
// which crashes Chromium and Firefox (the image carries a Selkies desktop).
// A limit of the tmpfs, not a reservation: pages are used only when written,
// and they count against --memory like any tmpfs.
const shmSize = "1g"

// memoryDeviceFlags are the container-create flags every sandbox gets
// (--shm-size) plus, for one that asked for a GPU, `--gpus all` (all WSLc
// takes).
func memoryDeviceFlags(gpu bool) []string {
	flags := []string{"--shm-size", shmSize}
	if gpu {
		flags = append(flags, "--gpus", "all")
	}
	return flags
}
