package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"maps"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	v1 "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1"
	"github.com/NVIDIA/OpenShell/sdk/go/openshell/v1/types"
	pb "github.com/NVIDIA/OpenShell/sdk/go/proto/openshellv1"
	"google.golang.org/grpc"

	"iwato/internal/dockercli"
	"iwato/internal/hostterm"
	"iwato/internal/openshell/client"
	"iwato/internal/openshell/gateway"
	"iwato/internal/openshell/wslc"
	"iwato/internal/termbin"
	"iwato/internal/termd"
)

const (
	// openshellWorkspace is the OpenShell workspace (its namespace for
	// sandboxes) that holds Iwato's sandboxes.
	openshellWorkspace = "default"
	gatewayPort        = 17670
	driverPort         = 17671
)

// OpenShell runs sandboxes on an OpenShell gateway that Iwato runs: in
// WSLc, serving the gateway's WSLc compute driver itself, or on a
// Docker-compatible engine, with upstream's Docker driver in the gateway.
type OpenShell struct {
	e   *Engine
	dir string

	mu    sync.Mutex
	ready chan struct{}
	err   error
	// prep is the progress of Prepare for the page; step is when its
	// current step began. served is set once the driver serves.
	prep   ImageState
	step   time.Time
	served bool
	sdk    *v1.Client
	conn   *grpc.ClientConn
	// env is the shells' environment.
	env map[string]string
	// timezone is Config.Timezone, as the owner last set it.
	tzMu     sync.Mutex
	timezone string

	termMu sync.Mutex
	terms  map[string]map[string]*hostterm.Session // sandbox name → terminal name
	// termStarted is when each terminal started: those the daemon keeps,
	// as it says; the others, when Iwato started them.
	termStarted map[string]time.Time // "sandbox/terminal"
	// daemons is what Iwato found of each running sandbox's daemon
	// (iwato-entrypoint, internal/termd): until the sandbox stops.
	daemons map[string]daemonState
	// imageDaemon tells whether an image carries the daemon (the image
	// convention's label, imageHasDaemon); tests replace it. New sandboxes
	// of such an image run it as their main process. termPath replaces the
	// daemon's paths (termd.Path, termd.LegacyPath) in tests.
	imageDaemon func(ctx context.Context, image string) bool
	termPath    string
	// legacyPlaced: the WSLc driver places this exe's daemon in the
	// sandboxes made before the image carried it (this exe has one).
	legacyPlaced bool

	pol v1.PolicyInterface // tests replace the gateway's policy API
	// updateConfig replaces the gateway's config update in tests.
	updateConfig func(context.Context, *v1.ConfigUpdate) (*v1.ConfigUpdateResult, error)
	// logs replaces reading the sandbox's logs from the gateway in tests.
	logs func(context.Context, Sandbox) ([]v1.LogLine, error)
	// logCache keeps each sandbox's log lines for logCacheFor.
	logMu    sync.Mutex
	logCache map[string]cachedLogLines
	// drafts keeps each sandbox's decided proposals (openshell_draft.go).
	drafts draftCache
	// sandboxConfig replaces reading the sandbox's effective config
	// (GetSandboxConfig) in tests.
	sandboxConfig func(context.Context, Sandbox) (*v1.SandboxConfig, error)
	// providerList replaces listing the providers (the fake's).
	providerList func(context.Context) ([]ProviderInfo, map[string][]string, error)
	// profilesDone is set once Iwato's provider profiles are in the
	// gateway; profileAPI replaces the gateway's profile API in tests.
	profilesDone bool
	profileAPI   profileAPI
	// sandboxAPI replaces the gateway's sandbox API in tests; pollEvery is
	// how often a start looks at the sandbox (a second when zero).
	sandboxAPI v1.SandboxInterface
	pollEvery  time.Duration
	// fwd connects the ingress to the sandboxes' ports (openshell_ingress.go).
	fwd *client.Forwarder
	// stopDriver stops the compute driver being served (a reset, reset.go);
	// runner replaces the gateway's wslc or docker in tests (gatewayCLI).
	stopDriver func()
	runner     wslc.Runner
	// termEnded is told when a terminal session Iwato holds ends
	// (autostop.go).
	termEnded atomic.Pointer[func(sandbox string)]
	// runtime: sandboxes made with older runtime images (WSLc).
	runtime runtimeCheck
	// engineErr refuses at once while the engine is unusable (serve waits
	// for it before Prepare: checks.go); nil in tests.
	engineErr func() error
	// store keeps each sandbox's policy (policy_store.go); nil keeps it in
	// memory.
	store     *policyStore
	storeOnce sync.Once
}

func NewOpenShell(e *Engine, configDir string) *OpenShell {
	return &OpenShell{e: e, dir: filepath.Join(configDir, "openshell"), ready: make(chan struct{}), env: termEnv, timezone: e.config.Timezone, terms: map[string]map[string]*hostterm.Session{},
		legacyPlaced: !e.docker && termbin.Binary() != nil, store: &policyStore{dir: filepath.Join(configDir, "policies")}}
}

// Prepare serves the compute driver on Windows' loopback (WSLc; with a
// Docker-compatible engine the gateway runs upstream's driver), then
// brings up the gateway container and connects to it. Sandbox operations
// wait for it; a failure is reported to each of them and on the page,
// which can retry (RetryPreparation).
func (o *OpenShell) Prepare(ctx context.Context) {
	// The channel of this preparation: a reset makes a new one for the next.
	o.mu.Lock()
	ready := o.ready
	o.mu.Unlock()
	err := o.prepare(ctx)
	o.mu.Lock()
	o.err = err
	if err != nil {
		o.prep = prepFailed("openshell", "openshell_failed", "OpenShell could not be prepared", err)
		o.prep.Log = o.logPath()
	} else {
		o.prep = ImageState{Phase: "ready"}
	}
	close(ready)
	o.mu.Unlock()
	if err != nil {
		log.Printf("OpenShell is not available: %v", english(err))
	}
}

// report shows the step Prepare is at (code and params for the page).
func (o *OpenShell) report(code string, params map[string]any, message string) {
	o.mu.Lock()
	o.prep = prepState("building", "openshell", code, params, message)
	o.prep.Log = o.logPath()
	o.step = time.Now()
	o.mu.Unlock()
}

func (o *OpenShell) logPath() string { return filepath.Join(o.dir, "gateway-build.log") }

// Preparation is Prepare's progress for the page: the step and how long it
// has taken so far.
func (o *OpenShell) Preparation() ImageState {
	o.mu.Lock()
	defer o.mu.Unlock()
	p := o.prep
	switch p.Phase {
	case "":
		return prepState("checking", "openshell", "openshell_waiting", nil, "OpenShell の準備を待っています…")
	case "building":
		if d := time.Since(o.step); d >= 5*time.Second {
			p.Message += fmt.Sprintf(" (%d 秒)", int(d.Seconds()))
			p.Params = maps.Clone(p.Params)
			if p.Params == nil {
				p.Params = map[string]any{}
			}
			p.Params["seconds"] = int(d.Seconds())
		}
	}
	return p
}

// RetryPreparation runs a failed Prepare again.
func (o *OpenShell) RetryPreparation(ctx context.Context) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.prep.Phase != "failed" {
		return false
	}
	o.ready, o.err, o.prep = make(chan struct{}), nil, ImageState{}
	go o.Prepare(ctx)
	return true
}

// readyChan closes when Prepare ends.
func (o *OpenShell) readyChan() chan struct{} {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.ready
}

func (o *OpenShell) prepare(ctx context.Context) error {
	if o.e.docker {
		// Upstream's Docker driver runs in the gateway (docs/adr/0002).
		return o.connect(ctx, nil)
	}
	c := o.e.config
	prefix := c.prefix()
	o.report("driver_starting", nil, "WSLc のドライバを起動しています…")
	cert, key, ca, err := gateway.DriverTLS(o.dir)
	if err != nil {
		return err
	}
	g := gateway.Gateway{
		Endpoint: fmt.Sprintf("https://%s-gateway:17670", prefix), Network: prefix,
		CA: filepath.Join(o.dir, "gateway-tls", "ca.crt"), Cert: filepath.Join(o.dir, "gateway-tls", "client.crt"), Key: filepath.Join(o.dir, "gateway-tls", "client.key"),
	}
	// A driver that stops early (a bad configuration) ends the wait for
	// the gateway with its error.
	base := ctx
	ctx, stop := context.WithCancelCause(ctx)
	defer stop(nil)
	o.mu.Lock()
	served := o.served
	o.mu.Unlock()
	if !served {
		ln, err := wslc.Listen("tls://127.0.0.1:"+strconv.Itoa(driverPort), cert, key, ca)
		if err != nil {
			return errCode("driver_port_in_use", map[string]any{"port": driverPort, "detail": err.Error()},
				fmt.Sprintf("the WSLc driver cannot listen on port %d (is another process or a WSLc container's published port using it?): %v", driverPort, err))
		}
		// The gateway image carries a patched supervisor and serves as the
		// supervisor image (gateway.PatchedSupervisor).
		supImage := wslc.DefaultSupervisorImage
		if gateway.PatchedSupervisor {
			if supImage, err = gateway.ImageRef(o.gatewayOptions(base)); err != nil {
				ln.Close()
				return err
			}
		}
		refs := runtimeImages{supImage, wslc.DefaultSandboxImage}
		o.runtime.setRefs(refs)
		dctx, dstop := context.WithCancel(base)
		o.mu.Lock()
		o.stopDriver = func() { dstop(); ln.Close() }
		o.mu.Unlock()
		go func() {
			err := wslc.Serve(dctx, wslc.Config{
				Endpoint: g.Endpoint, Network: g.Network, Namespace: c.Namespace,
				SupervisorImage: refs.Supervisor, SandboxImage: refs.Sandbox,
				DefaultImage: sandboxImage, StateDir: filepath.Join(o.dir, "sandboxes"), LogLevel: "info",
				PullPolicy: "if-not-present", GuestTLSCA: g.CA, LegacyTerm: termbin.Binary(),
			}, o.e.exe, ln)
			if err != nil && dctx.Err() == nil {
				noticef("compute driver stopped: %v", english(err))
				o.mu.Lock()
				o.served = false
				o.mu.Unlock()
				ln.Close()
				stop(errCode("driver_stopped", map[string]any{"detail": err.Error()}, "the WSLc driver stopped: "+err.Error()))
			}
		}()
		o.mu.Lock()
		o.served = true
		o.mu.Unlock()
	}
	return o.connect(ctx, func() error {
		if cause := context.Cause(ctx); cause != nil && !errors.Is(cause, context.Canceled) {
			return cause
		}
		return nil
	})
}

// connect brings up the gateway and connects to it. cause, when set,
// replaces the gateway's error with why its wait ended (the WSLc driver
// stopped).
func (o *OpenShell) connect(ctx context.Context, cause func() error) error {
	g, err := gateway.Ensure(ctx, o.gatewayCLI(), o.gatewayOptions(ctx))
	if err != nil {
		if cause != nil {
			if c := cause(); c != nil {
				return c
			}
		}
		return err
	}
	o.report("openshell_connecting", nil, "OpenShell に接続しています…")
	conn, err := client.Dial(g.Address, g.CA, g.Cert, g.Key)
	if err != nil {
		return err
	}
	sdk, err := v1.NewClient(v1.Config{Address: client.SDKAddress(g.Address), TLS: &v1.TLSConfig{CAFile: g.CA, CertFile: g.Cert, KeyFile: g.Key}})
	if err != nil {
		conn.Close()
		return err
	}
	if err := enablePolicyAdvisor(ctx, sdk); err != nil {
		noticef("Policy Advisor not enabled: %v", english(err))
	}
	// Creating a provider imports them again if this fails.
	profilesErr := ensureProfiles(ctx, pb.NewOpenShellClient(conn))
	if profilesErr != nil {
		noticef("provider profiles not imported: %v", english(profilesErr))
	}
	o.mu.Lock()
	o.conn, o.sdk = conn, sdk
	o.profilesDone = profilesErr == nil
	o.mu.Unlock()
	return nil
}

func (o *OpenShell) gatewayOptions(ctx context.Context) gateway.Options {
	opts := gateway.Options{Prefix: o.e.config.prefix(), Dir: o.dir, Port: gatewayPort, DriverPort: driverPort, Report: o.report, Log: o.logPath()}
	if pullsGatewayImage() {
		opts.Image = imageRef("iwato-gateway")
	}
	if o.e.docker {
		opts.DockerSocket, opts.SandboxLabel = o.e.config.DockerSocket, o.e.config.Namespace
		if opts.DockerSocket == "" {
			opts.DockerSocket = dockercli.Socket(ctx, o.e.exe)
		}
	}
	return opts
}

// gatewayCLI runs the gateway's commands with the engine's CLI.
func (o *OpenShell) gatewayCLI() wslc.Runner {
	if o.runner != nil {
		return o.runner
	}
	if o.e.docker {
		return dockercli.Runner{Exe: o.e.exe}
	}
	return wslc.NewRunner(o.e.exe)
}

// clients waits for Prepare.
func (o *OpenShell) clients(ctx context.Context) (*v1.Client, *grpc.ClientConn, error) {
	if o.engineErr != nil {
		if err := o.engineErr(); err != nil {
			return nil, nil, err
		}
	}
	select {
	case <-o.readyChan():
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.err != nil {
		return nil, nil, errCodeEn("openshell_unavailable", map[string]any{"detail": safeCLIError(o.err)}, "OpenShell を使えません: "+o.err.Error(), "OpenShell is not usable: "+english(o.err), o.err)
	}
	return o.sdk, o.conn, nil
}

// mainExited reports whether the sandbox is done because its main process
// ended (the daemon, or in a sandbox without it the main
// terminal's shell): Completed, or Error with MainProcessFailed (a nonzero
// exit, as a shell's after a failed command).
func mainExited(sb *v1.Sandbox) bool {
	switch sb.Status.Phase {
	case v1.SandboxCompleted:
		return true
	case v1.SandboxError:
		for _, c := range sb.Status.Conditions {
			if c.Reason == "MainProcessFailed" {
				return true
			}
		}
	}
	return false
}

func notFound(err error) bool {
	var s *v1.StatusError
	return errors.As(err, &s) && s.Code == v1.ErrorNotFound
}

// persistVolume names the volume that keeps one of the sandbox's
// persisted paths.
func persistVolume(c Config, w Sandbox, target string) string {
	h := sha256.Sum256([]byte(target))
	return c.resourceName(w, "persist-"+hex.EncodeToString(h[:4]))
}

// sandboxMounts puts a volume on each persisted path, through the
// driver's driver_config (the WSLc driver's take the Docker driver's form).
func (o *OpenShell) sandboxMounts(w Sandbox) []any {
	// []any and map[string]any: the SDK passes driver_config as a
	// protobuf Struct.
	mounts := []any{}
	for _, p := range w.volumePaths() {
		mounts = append(mounts, map[string]any{"type": "volume", "source": persistVolume(o.e.config, w, p), "target": p, "read_only": false})
	}
	return mounts
}

// driverConfig is the sandbox's driver_config: its volumes, for the WSLc
// driver or upstream's Docker driver. The Docker driver gets /dev/shm as a
// tmpfs of shmBytes too (Docker's own is 64 MB; the WSLc driver sets this
// size itself).
func (o *OpenShell) driverConfig(w Sandbox) map[string]any {
	if !o.e.docker {
		return map[string]any{"wslc": map[string]any{"mounts": o.sandboxMounts(w)}}
	}
	mounts := append(o.sandboxMounts(w), map[string]any{"type": "tmpfs", "target": "/dev/shm", "size_bytes": float64(shmBytes)})
	return map[string]any{"docker": map[string]any{"mounts": mounts}}
}

// shmBytes is the size of a Docker sandbox's /dev/shm, the WSLc driver's.
const shmBytes = 1 << 30

// sandboxPolicy lets the sandbox user write its workspace (the image's
// WORKDIR, or /sandbox), the home and the other persisted paths and the
// temporary directories and read the system. It allows no network but its
// network presets' (presets.go): the agent proposes what else it needs
// through the Policy Advisor and the owner approves it in Iwato.
func sandboxPolicy(w Sandbox) *v1.SandboxPolicy {
	p := &v1.SandboxPolicy{
		Version: 1,
		Filesystem: &v1.FilesystemPolicy{
			IncludeWorkdir: true,
			ReadOnly:       []string{"/bin", "/sbin", "/usr", "/lib", "/lib64", "/etc", "/opt", "/var", "/proc", "/dev/urandom", "/dev/random"},
			ReadWrite:      append([]string{"/tmp", "/var/tmp", "/dev/null", "/dev/tty", "/dev/ptmx", "/dev/pts", "/dev/shm"}, w.volumePaths()...),
		},
		Landlock:        &v1.LandlockPolicy{Compatibility: "best_effort"},
		NetworkPolicies: presetRules(w.Presets),
	}
	return p
}

// sandboxes is the gateway's sandbox API (tests replace it).
func (o *OpenShell) sandboxes(ctx context.Context) (v1.SandboxInterface, error) {
	if o.sandboxAPI != nil {
		return o.sandboxAPI, nil
	}
	sdk, _, err := o.clients(ctx)
	if err != nil {
		return nil, err
	}
	return sdk.Sandboxes(), nil
}

// readyCondition is the sandbox's Ready condition, if it has one.
func readyCondition(sb *v1.Sandbox) *v1.SandboxCondition {
	for i, c := range sb.Status.Conditions {
		if c.Type == "Ready" {
			return &sb.Status.Conditions[i]
		}
	}
	return nil
}

// provisioningTimedOut reports a sandbox that OpenShell gave up on after
// its 300-second repair window (Error, ProvisioningTimedOut). OpenShell
// starts it again (with its policy) once it has reclaimed the container.
func provisioningTimedOut(sb *v1.Sandbox) bool {
	c := readyCondition(sb)
	return sb.Status.Phase == v1.SandboxError && c != nil && c.Reason == "ProvisioningTimedOut"
}

// provisioningProblem is why a sandbox being provisioned or started will
// not become ready unless the owner changes something, or "" while it is
// progressing (image pull, container start, waiting for the supervisor).
//
// OpenShell keeps such a sandbox Provisioning (or Starting) for a 300-second
// repair window: the supervisor reports the effective configuration (policy
// and providers) rejected, the gateway records it as the configuration
// admission's error and as a ConfigurationInvalid condition, and only when
// the window expires does the sandbox turn Error (ProvisioningTimedOut).
// A policy change within the window lets it continue.
func provisioningProblem(sb *v1.Sandbox) string {
	if a := sb.Status.ConfigurationAdmission; a != nil && a.State == types.ConfigurationAdmissionRejected {
		msg := a.Error
		if msg == "" {
			msg = "Effective configuration could not be activated"
		}
		return "OpenShell がサンドボックスの設定を受け付けません: " + msg + "。「ポリシー」タブで規則を直すと準備が続きます (OpenShell は 5 分で失敗にします)"
	}
	for _, c := range sb.Status.Conditions {
		if (c.Type != "Ready" && c.Type != "ConfigurationReady") || !strings.EqualFold(c.Status, "false") {
			continue
		}
		switch c.Reason {
		case "ConfigurationInvalid":
			return "OpenShell がサンドボックスの設定を受け付けません: " + c.Message + "。「ポリシー」タブで規則を直すと準備が続きます (OpenShell は 5 分で失敗にします)"
		case "ProvisioningDeadlineElapsed", "ProvisioningTimedOut":
			return "OpenShell がサンドボックスの準備をあきらめました: " + c.Message
		}
	}
	return ""
}

// errorDetail is OpenShell's reason for a sandbox in Error.
func errorDetail(sb *v1.Sandbox) string {
	if c := readyCondition(sb); c != nil && c.Message != "" {
		return c.Message
	}
	detail := "サンドボックスでエラーが起きました"
	for _, c := range sb.Status.Conditions {
		if c.Message != "" {
			detail = c.Message
		}
	}
	return detail
}

// startProgress tells from one look at a sandbox being started whether it
// is ready, failed (an error waiting will not fix), or still progressing
// (both nil).
func startProgress(sb *v1.Sandbox) (ready bool, err error) {
	switch sb.Status.Phase {
	case v1.SandboxReady, v1.SandboxCompleted:
		return true, nil
	case v1.SandboxError:
		return false, errors.New("サンドボックスを起動できませんでした: " + errorDetail(sb))
	case v1.SandboxStopped:
		return false, errors.New("サンドボックスは起動する前に止まりました")
	case v1.SandboxDeleting:
		return false, errors.New("サンドボックスは削除されています")
	}
	if p := provisioningProblem(sb); p != "" {
		return false, errors.New(p)
	}
	return false, nil
}

// waitStarted waits until the sandbox is ready, or returns as soon as
// OpenShell reports a failure: unlike WaitReady it does not wait out the
// repair window of a sandbox whose configuration was rejected.
func (o *OpenShell) waitStarted(ctx context.Context, api v1.SandboxInterface, name string) error {
	every := o.pollEvery
	if every == 0 {
		every = time.Second
	}
	for {
		sb, err := api.Get(ctx, openshellWorkspace, name)
		if err != nil {
			if ctx.Err() != nil {
				return startCanceled(ctx)
			}
			return err
		}
		if ready, err := startProgress(sb); ready || err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return startCanceled(ctx)
		case <-time.After(every):
		}
	}
}

// startCanceled explains why waiting for a start ended early.
func startCanceled(ctx context.Context) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return errors.New("サンドボックスが 5 分以内に準備できませんでした")
	}
	return errors.New("起動を待つのをやめました")
}

// Start creates the sandbox, or starts it when stopped, and
// waits until it is ready.
func (o *OpenShell) Start(ctx context.Context, w Sandbox) error {
	api, err := o.sandboxes(ctx)
	if err != nil {
		return err
	}
	// The sandbox's next run is looked at again (the WSLc driver places
	// the current daemon in an older sandbox on its start).
	o.forgetDaemon(w.Name)
	sb, err := api.Get(ctx, openshellWorkspace, w.Name)
	providers := w.Providers
	if err == nil && sb.Status.Phase == v1.SandboxError && !mainExited(sb) && !provisioningTimedOut(sb) {
		// A failed sandbox is made again, with its providers; the volumes
		// keep the data.
		providers = sb.Spec.Providers
		if err := o.Delete(ctx, w, false); err != nil {
			return err
		}
		_, err = api.Get(ctx, openshellWorkspace, w.Name)
	}
	switch {
	case notFound(err):
		for _, p := range w.volumePaths() {
			if err := o.e.volume(ctx, persistVolume(o.e.config, w, p), map[string]string{sandboxKey: w.Name, pathKey: p}); err != nil {
				return err
			}
		}
		image := w.Image
		if image == "" {
			image = sandboxImage
		}
		policy := sandboxPolicy(w)
		if len(providers) > 0 {
			// The providers' initial rules, bound to them (ADR 0022): the
			// gateway checks the bindings against the providers created
			// with the sandbox.
			list, _, err := o.ListProviders(ctx)
			if err != nil {
				return err
			}
			if policy.NetworkPolicies, err = withProviderInitialRules(policy.NetworkPolicies, list, providers); err != nil {
				return err
			}
		}
		spec := &v1.SandboxSpec{
			Template: &v1.SandboxTemplate{
				Image:        image,
				DriverConfig: o.driverConfig(w),
			},
			Environment: sandboxEnv(w),
			Policy:      policy,
			Providers:   providers,
			GPU:         w.GPU,
			TTY:         true,
		}
		if o.hasDaemon(ctx, image) {
			// The main process keeps the terminals (the image's daemon),
			// which then outlive Iwato; without a terminal of its own.
			spec.Command, spec.TTY = []string{o.daemonPath(termd.Path), "serve", "-key", w.Name}, false
		}
		_, err = api.Create(ctx, openshellWorkspace, w.Name, spec, v1.WithLabels(map[string]string{"iwato.sandbox": w.Name}))
		if err != nil {
			return err
		}
	case err != nil:
		return err
	case sb.Status.Phase == v1.SandboxStopped || mainExited(sb) || provisioningTimedOut(sb):
		// A sandbox whose shell ended starts a fresh one (OpenShell
		// keeps it, /sandbox included, as Completed or MainProcessFailed).
		// One whose provisioning timed out starts again with its policy,
		// which the owner may have repaired meanwhile.
		if _, err := api.Start(ctx, openshellWorkspace, w.Name); err != nil {
			var s *v1.StatusError
			if errors.As(err, &s) && s.Code == v1.ErrorConflict && strings.Contains(s.Message, "cleanup is still pending") {
				return errors.New("OpenShell が前回の準備の後始末をしています。少し待ってからもう一度起動してください")
			}
			return err
		}
	}
	wait, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if err := o.waitStarted(wait, api, w.Name); err != nil {
		return err
	}
	// Before the owner opens a terminal: the agents read their files when
	// they start.
	o.placeAgentFiles(ctx, w)
	o.writeTermEnv(ctx, w.Name)
	return nil
}

func (o *OpenShell) Status(ctx context.Context, w Sandbox) Status {
	s := Status{Name: w.Name, State: "not-created"}
	select {
	case <-o.readyChan():
	default:
		s.Detail = o.Preparation().Message
		return s
	}
	api, err := o.sandboxes(ctx)
	if err != nil {
		s.State, s.Detail = "error", err.Error()
		return s
	}
	sb, err := api.Get(ctx, openshellWorkspace, w.Name)
	switch {
	case notFound(err):
	case err != nil:
		s.State, s.Detail = "error", err.Error()
	default:
		switch sb.Status.Phase {
		case v1.SandboxReady:
			s.State = "running"
		case v1.SandboxStopped, v1.SandboxCompleted:
			s.State = "stopped"
		case v1.SandboxStopping:
			s.State = "stopping"
		case v1.SandboxError:
			if mainExited(sb) {
				// Its shell ended (exit or Ctrl-D): stopped, not broken.
				s.State = "stopped"
				break
			}
			s.State, s.Detail = "error", errorDetail(sb)
		case v1.SandboxProvisioning, v1.SandboxStarting:
			// Being made or started: why it is not ready yet, and the
			// problem that keeps it from becoming ready, while it lasts.
			if p := provisioningProblem(sb); p != "" {
				s.State, s.Detail = "error", p
				break
			}
			s.State, s.Detail = "starting", string(sb.Status.Phase)+"…"
			if c := readyCondition(sb); c != nil && c.Message != "" {
				s.Detail = c.Message
			}
		default:
			s.State, s.Detail = "stopped", string(sb.Status.Phase)+"…"
		}
		if s.State == "running" || s.State == "stopped" {
			s.RecreateToUpdate = o.recreateToUpdate(ctx, sb.ID)
		}
	}
	return s
}

// errTerminalEnded and errTerminalLimit refuse an attach.
var errTerminalEnded = errCodeEn("terminal_ended", nil, "このターミナルは終了しています", "this terminal has ended", nil)

func errTerminalLimit() error {
	return errCodeEn("terminal_limit", map[string]any{"max": maxTerminals}, fmt.Sprintf("ターミナルは %d 個までです", maxTerminals), fmt.Sprintf("at most %d terminals", maxTerminals), nil)
}

// errNotStoppable: OpenShell stops only a ready sandbox.
var errNotStoppable = errCodeEn("not_stoppable", nil, "OpenShell は準備中のサンドボックスを止められません。準備が終わるのを待つか、削除してください", "OpenShell cannot stop a sandbox it is still preparing; wait for it, or delete it", nil)

func (o *OpenShell) Stop(ctx context.Context, w Sandbox) error {
	api, err := o.sandboxes(ctx)
	if err != nil {
		return err
	}
	if sb, err := api.Get(ctx, openshellWorkspace, w.Name); err == nil && (sb.Status.Phase == v1.SandboxProvisioning || sb.Status.Phase == v1.SandboxStarting) {
		return errNotStoppable
	}
	o.dropTerminals(w)
	if _, err := api.Stop(ctx, openshellWorkspace, w.Name); err != nil && !notFound(err) {
		return err
	}
	wait, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	_, err = api.WaitStopped(wait, openshellWorkspace, w.Name)
	if notFound(err) {
		return nil
	}
	return err
}

// Delete removes the sandbox; with purge also its volumes (the home's, and
// those of an older sandbox's paths outside it).
func (o *OpenShell) Delete(ctx context.Context, w Sandbox, purge bool) error {
	api, err := o.sandboxes(ctx)
	if err != nil {
		return err
	}
	o.dropTerminals(w)
	// A sandbox made again under the name starts a new draft.
	defer o.forgetDraft(w.Name)
	if _, err := api.Delete(ctx, openshellWorkspace, w.Name); err != nil && !notFound(err) {
		return err
	}
	// Deletion finishes asynchronously; the volumes stay mounted until then.
	deadline := time.Now().Add(2 * time.Minute)
	for {
		_, err := api.Get(ctx, openshellWorkspace, w.Name)
		if notFound(err) {
			break
		}
		if time.Now().After(deadline) {
			return errCodeEn("delete_unfinished", nil, "サンドボックスの削除が終わりません", "the sandbox's deletion does not finish", nil)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	if !purge {
		return nil
	}
	for _, p := range w.volumePaths() {
		if err := o.e.removeVolume(ctx, persistVolume(o.e.config, w, p)); err != nil {
			return err
		}
	}
	return nil
}

// Exists says whether OpenShell has the sandbox.
func (o *OpenShell) Exists(ctx context.Context, w Sandbox) (bool, error) {
	api, err := o.sandboxes(ctx)
	if err != nil {
		return false, err
	}
	_, err = api.Get(ctx, openshellWorkspace, w.Name)
	if notFound(err) {
		return false, nil
	}
	return err == nil, err
}

// ApplyPolicyOps makes one policy update (a recreate's carried rules).
func (o *OpenShell) ApplyPolicyOps(ctx context.Context, w Sandbox, ops []v1.PolicyMergeOperation) error {
	_, err := o.applyOps(ctx, w, ops)
	return err
}

// Volumes lists the persisted paths' volumes, kept ones included.
func (o *OpenShell) Volumes(ctx context.Context) ([]Volume, error) { return o.e.volumes(ctx) }

// GPUAvailable tells whether a new sandbox can ask for a GPU.
func (o *OpenShell) GPUAvailable(ctx context.Context) bool { return o.e.gpuAvailable(ctx) }

// RemoveVolume removes one of this configuration's volumes.
func (o *OpenShell) RemoveVolume(ctx context.Context, name string) error {
	return o.e.removeVolume(ctx, name)
}

// termEnv adds a truecolor terminal to the environment OpenShell gives the
// shells (USER is the image's, SHELL the login shell found in the image).
var termEnv = map[string]string{"COLORTERM": "truecolor"}

// sandboxEnv is the sandbox's environment: termEnv, HOME (otherwise the
// workspace) and IWATO_PERSIST, what persists: the home alone for a new
// sandbox (only what is under it does), and an older one's paths outside
// it until the owner recreates it. Agents and scripts read it (the
// sandbox's /AGENTS.md names it).
func sandboxEnv(w Sandbox) map[string]string {
	env := maps.Clone(termEnv)
	env["HOME"] = homeDir
	env["IWATO_PERSIST"] = strings.Join(w.volumePaths(), ":")
	return env
}

// exportHome makes the home of a process Iwato starts with an exec (an extra
// terminal, a helper) the home volume when the sandbox has one. An exec does
// not get the HOME the sandbox was created with, and a sandbox created before
// the home was a volume has no homeDir and keeps its own home, so the script
// decides by looking.
const exportHome = `h=` + homeDir + `
if [ -d "$h" ] && [ -w "$h" ]; then HOME=$h; export HOME; fi
`

// enterWork moves a terminal's shell to ~/work (workDir, made when missing,
// as the sandbox user) when its home is the home volume and the workspace
// is not the home itself: the image convention's WORKDIR (/sandbox) is
// outside the home, where nothing persists. A sandbox whose workspace is
// the home (made from the standard image before the convention) keeps it.
const enterWork = `if [ "$HOME" = ` + homeDir + ` ] && [ "$(pwd -P)" != "$HOME" ]; then
  mkdir -p "$HOME/` + workDir + `" 2>/dev/null; cd "$HOME/` + workDir + `" 2>/dev/null || :
fi
`

// shellCommand is a terminal's shell: the image's login shell, in ~/work
// (enterWork), else in the workspace like the main process OpenShell
// starts without a command.
var shellCommand = []string{"/bin/sh", "-c", exportHome + enterWork + `exec "${SHELL:-/bin/sh}" -l`}

// maxTerminals bounds a sandbox's terminals, the main one included.
const maxTerminals = 8

// Attach joins (or starts) one of the sandbox's terminals. Iwato keeps
// each terminal's screen (internal/hostterm). Its process is the daemon's,
// which outlives Iwato; in a sandbox without it, the main terminal is the
// sandbox's main process, which outlives Iwato too, and the others are
// shells that end when Iwato does. A size of 0×0 keeps a daemon's terminal's.
func (o *OpenShell) Attach(ctx context.Context, w Sandbox, t TermRef, cols, rows int) (Terminal, error) {
	if !validTermName(t.Name) {
		return nil, errors.New("invalid terminal name")
	}
	sdk, conn, err := o.clients(ctx)
	if err != nil {
		return nil, err
	}
	d, err := o.daemon(ctx, w)
	if err != nil {
		return nil, err
	}
	o.termMu.Lock()
	defer o.termMu.Unlock()
	sessions := o.terms[w.Name]
	if sessions == nil {
		sessions = map[string]*hostterm.Session{}
		o.terms[w.Name] = sessions
	}
	if s := sessions[t.Name]; s != nil {
		if c, err := s.Attach(); err == nil {
			return c, nil
		}
		delete(sessions, t.Name)
	}
	var backend hostterm.Backend
	started := time.Now()
	switch {
	case d.ok:
		var cmd []string
		if t.New {
			cmd = shellCommand
		}
		var h *client.Held
		h, err = client.AttachHeld(ctx, conn, openshellWorkspace, w.Name, d.bin, w.Name, t.Name, cols, rows, cmd, o.env)
		var te *termd.Error
		if t.Name == mainTerm && cmd == nil && errors.As(err, &te) && te.Code == termd.ErrAbsent {
			// Main starts again only when no terminal runs (the list then
			// shows it alone): a page still showing an ended main does
			// not bring it back next to the others.
			if l, lerr := client.ListHeld(ctx, conn, openshellWorkspace, w.Name, d.bin, w.Name); lerr == nil && len(runningTerms(l)) == 0 {
				h, err = client.AttachHeld(ctx, conn, openshellWorkspace, w.Name, d.bin, w.Name, t.Name, cols, rows, shellCommand, o.env)
			}
		}
		switch {
		case errors.As(err, &te) && te.Code == termd.ErrAbsent:
			return nil, errTerminalEnded
		case errors.As(err, &te) && te.Code == termd.ErrLimit:
			return nil, errTerminalLimit()
		case errors.Is(err, client.ErrNoDaemon):
			// It was there: the next attach looks again.
			delete(o.daemons, w.Name)
		case err == nil:
			backend, started = h, time.UnixMilli(h.Hello.Started)
			if cols == 0 || rows == 0 {
				cols, rows = h.Hello.Cols, h.Hello.Rows
			}
		}
	case cols == 0 || rows == 0:
		return nil, errors.New("no terminal size")
	case t.Name == mainTerm:
		// The tunnel lives as long as the session, not the request that
		// opened it (the page's attach gives up after a while).
		backend, err = client.AttachMain(context.WithoutCancel(ctx), sdk, openshellWorkspace, w.Name, cols, rows)
	case !t.New:
		return nil, errTerminalEnded
	case len(sessions) >= maxTerminals:
		return nil, errTerminalLimit()
	default:
		backend, err = client.StartShell(context.Background(), sdk, openshellWorkspace, w.Name, shellCommand, client.Options{Env: o.env}, cols, rows)
	}
	if err != nil {
		log.Printf("terminal %s/%s: %v", w.Name, t.Name, english(err))
		return nil, err
	}
	s := hostterm.NewRecorded(backend, cols, rows, o.openTrace(conn, w, t, cols, rows))
	if o.termStarted == nil {
		o.termStarted = map[string]time.Time{}
	}
	o.termStarted[w.Name+"/"+t.Name] = started
	go func() {
		<-s.Done()
		log.Printf("terminal %s/%s ended: %v", w.Name, t.Name, english(s.Err()))
		if f := o.termEnded.Load(); f != nil {
			(*f)(w.Name)
		}
	}()
	sessions[t.Name] = s
	return s.Attach()
}

// ResumeTerminals attaches, once OpenShell is ready, the terminals that
// the running sandboxes' daemons kept while Iwato was not running, so that
// Iwato watches their screens again (notifier.go) before a page opens them.
func (o *OpenShell) ResumeTerminals(ctx context.Context, list func() []Sandbox) {
	select {
	case <-o.readyChan():
	case <-ctx.Done():
		return
	}
	if _, _, err := o.clients(ctx); err != nil {
		return
	}
	for _, w := range list() {
		c, cancel := context.WithTimeout(ctx, time.Minute)
		if o.Status(c, w).State == "running" {
			o.resume(c, w)
		}
		cancel()
	}
}

func (o *OpenShell) resume(ctx context.Context, w Sandbox) {
	d, err := o.daemon(ctx, w)
	if err != nil || !d.ok {
		return
	}
	_, conn, err := o.clients(ctx)
	if err != nil {
		return
	}
	l, err := client.ListHeld(ctx, conn, openshellWorkspace, w.Name, d.bin, w.Name)
	if err != nil {
		log.Printf("sandbox %s: terminals not resumed: %v", w.Name, english(err))
		return
	}
	for _, t := range l.Terminals {
		if !validTermName(t.Name) {
			continue
		}
		term, err := o.Attach(ctx, w, TermRef{Name: t.Name}, 0, 0)
		if err != nil {
			log.Printf("terminal %s/%s: not resumed: %v", w.Name, t.Name, english(err))
			continue
		}
		// The session stays; this attachment is not a page's.
		term.Close()
	}
}

// OnTerminalEnd registers f, told the sandbox's name whenever one of the
// terminal sessions Iwato holds ends (its process exited, it was killed,
// or its attachment broke).
func (o *OpenShell) OnTerminalEnd(f func(sandbox string)) { o.termEnded.Store(&f) }

// TerminalsIdle tells whether the running sandbox's daemon runs no
// terminal. Every daemon version lists its terminals, so this works with
// the daemons of older images too. Without a daemon it cannot tell (its
// main terminal is the main process, whose end stops the sandbox anyway).
// When terminals run, Iwato holds a session for each (as on its start), so
// that their ends are seen even when an attachment broke.
func (o *OpenShell) TerminalsIdle(ctx context.Context, w Sandbox) (bool, error) {
	d, err := o.daemon(ctx, w)
	if err != nil || !d.ok {
		return false, err
	}
	_, conn, err := o.clients(ctx)
	if err != nil {
		return false, err
	}
	l, err := client.ListHeld(ctx, conn, openshellWorkspace, w.Name, d.bin, w.Name)
	if err != nil {
		return false, err
	}
	if len(l.Terminals) == 0 {
		return true, nil
	}
	o.resume(ctx, w)
	return false, nil
}

// daemonState is what Iwato found of a sandbox's daemon: ok when it
// answered, with its version and capabilities, and bin, the binary that
// serves the CLI's commands (attach, list, kill, sftp). restartUpdates: a
// sandbox made before the image carried the daemon, into which the WSLc
// driver places the current binary on each start (an image's daemon is
// renewed only by recreating the sandbox).
type daemonState struct {
	ok             bool
	version        int
	caps           []string
	bin            string
	restartUpdates bool
}

// has tells whether the daemon serves capability c.
func (d daemonState) has(c string) bool { return d.ok && termd.Has(d.version, d.caps, c) }

// daemonPath is one of the daemon's paths (tests replace them all).
func (o *OpenShell) daemonPath(p string) string {
	if o.termPath != "" {
		return o.termPath
	}
	return p
}

// daemonPaths are where the daemon may be, the image's path first, for an
// exec (client.termCommand).
func (o *OpenShell) daemonPaths() string {
	return o.daemonPath(termd.Path) + ":" + o.daemonPath(termd.LegacyPath)
}

// hasDaemon tells whether new sandboxes of image run the daemon as their
// main process.
func (o *OpenShell) hasDaemon(ctx context.Context, image string) bool {
	if o.imageDaemon != nil {
		return o.imageDaemon(ctx, image)
	}
	return o.e.imageHasDaemon(ctx, image)
}

// daemon looks once per run of the sandbox for its daemon (and starts it
// in a sandbox whose main process it is not). Without one the terminals
// are the main process and execs, and files go through OpenShell's SFTP.
func (o *OpenShell) daemon(ctx context.Context, w Sandbox) (daemonState, error) {
	o.termMu.Lock()
	d, known := o.daemons[w.Name]
	o.termMu.Unlock()
	if known {
		return d, nil
	}
	_, conn, err := o.clients(ctx)
	if err != nil {
		return d, err
	}
	l, err := client.ListHeld(ctx, conn, openshellWorkspace, w.Name, o.daemonPaths(), w.Name)
	if err == nil && l.Version < termd.MinVersion {
		err = fmt.Errorf("%w: version %d is older than %d", client.ErrNoDaemon, l.Version, termd.MinVersion)
	}
	switch {
	case errors.Is(err, client.ErrNoDaemon) && o.mainDaemon(ctx, w) != "":
		// Its main process is the daemon (no terminal of its own): the
		// main process and execs are no way to its terminals. Not
		// remembered: the page attaches again.
		return d, errCodeEn("daemon_unreachable", nil, "サンドボックスのターミナルのデーモンに接続できません。少し待ってから接続し直してください", "cannot reach the sandbox's terminal daemon; connect again in a moment", nil)
	case errors.Is(err, client.ErrNoDaemon):
		log.Printf("sandbox %s: no daemon, its terminals end with Iwato: %v", w.Name, english(err))
	case err != nil:
		return d, err
	default:
		d = daemonState{ok: true, version: l.Version, caps: l.Capabilities, bin: o.daemonBin(l)}
		// The driver places its binary at the legacy path on each start of
		// a sandbox whose main process runs from there.
		d.restartUpdates = o.legacyPlaced && d.bin == o.daemonPath(termd.LegacyPath) && o.mainDaemon(ctx, w) == d.bin
	}
	o.termMu.Lock()
	if o.daemons == nil {
		o.daemons = map[string]daemonState{}
	}
	o.daemons[w.Name] = d
	o.termMu.Unlock()
	return d, nil
}

// daemonBin is the binary of the daemon that sent l: its path, or for a
// daemon of the first version (which does not say it) the legacy path,
// the only one it ran from.
func (o *OpenShell) daemonBin(l *termd.List) string {
	switch {
	case o.termPath != "":
		return o.termPath
	case termd.IsDaemonPath(l.Path):
		return l.Path
	case l.Path == "":
		return termd.LegacyPath
	}
	return o.daemonPaths()
}

// mainDaemon is the daemon's path when the sandbox was made with the
// daemon as its main process ("" when it was not; the image's path when
// that cannot be read: falling back would not help such a sandbox).
func (o *OpenShell) mainDaemon(ctx context.Context, w Sandbox) string {
	api, err := o.sandboxes(ctx)
	if err != nil {
		return o.daemonPath(termd.Path)
	}
	sb, err := api.Get(ctx, openshellWorkspace, w.Name)
	if err != nil {
		return o.daemonPath(termd.Path)
	}
	c := sb.Spec.Command
	if len(c) >= 2 && (path.Base(c[0]) == "iwato-entrypoint" || path.Base(c[0]) == "iwato-term") && c[1] == "serve" {
		return c[0]
	}
	return ""
}

func (o *OpenShell) forgetDaemon(name string) {
	o.termMu.Lock()
	delete(o.daemons, name)
	o.termMu.Unlock()
}

// traceMax bounds one terminal recording.
const traceMax = 64 << 20

// traceScript writes a recording ($1, a file name) into the sandbox user's
// ~/.local/state/iwato/term-trace (the home is a volume, so it stays), and
// removes the ones older than 14 days first.
const traceScript = exportHome + `umask 077
d=${HOME:-/tmp}/.local/state/iwato/term-trace
mkdir -p "$d" && chmod 700 "$d" || exit 1
find "$d" -name '*.jsonl' -mtime +14 -delete 2>/dev/null
exec cat >"$d/$1"`

// openTrace starts the recording of a terminal when the configuration asks
// for it (TermTrace). The recording is written inside the sandbox, by a
// process that cat's it into a file there, so that the sandbox's own tools
// can read and replay it (cmd/term-replay); nothing is kept on the host.
// It never holds the terminal up (hostterm.AsyncWriter): when the process
// cannot start, the recording is lost and the terminal is not.
func (o *OpenShell) openTrace(conn *grpc.ClientConn, w Sandbox, t TermRef, cols, rows int) *hostterm.Recorder {
	if !o.e.config.TermTrace || conn == nil {
		return nil
	}
	name := fmt.Sprintf("%s-%s-%s.jsonl", w.Name, t.Name, time.Now().UTC().Format("20060102T150405Z"))
	open := func() (io.WriteCloser, error) {
		// Not the request's context: the recording lives as long as the terminal.
		p, err := client.Start(context.Background(), conn, openshellWorkspace, w.Name, []string{"/bin/sh", "-c", traceScript, "iwato-term-trace", name}, client.Options{Env: termEnv})
		if err != nil {
			return nil, err
		}
		go func() { _, _ = io.Copy(io.Discard, p) }()
		log.Printf("terminal %s/%s: recording its output in the sandbox, ~/.local/state/iwato/term-trace/%s", w.Name, t.Name, name)
		return procWriter{p}, nil
	}
	failed := func(err error) { log.Printf("terminal %s/%s: recording stopped: %v", w.Name, t.Name, english(err)) }
	return hostterm.NewRecorder(hostterm.NewAsyncWriter(open, failed, 256), hostterm.TraceHeader{Sandbox: w.Name, Term: t.Name, Cols: cols, Rows: rows}, traceMax)
}

// procWriter writes to a process's stdin; closing ends its input (so that
// cat finishes the file) and then the process.
type procWriter struct{ p *client.Process }

func (w procWriter) Write(b []byte) (int, error) { return w.p.Write(b) }
func (w procWriter) Close() error {
	err := w.p.CloseWrite()
	go func() { _, _ = w.p.Wait(); _ = w.p.Close() }()
	return err
}

// Terminals lists the sandbox's terminals, main first: the daemon's (main
// alone when none runs: attaching starts it), or else the ones Iwato holds
// (main always). outdated are all when the daemon is older than this
// Iwato's.
func (o *OpenShell) Terminals(ctx context.Context, w Sandbox) (names, outdated []string, multi bool, e error) {
	d, err := o.daemon(ctx, w)
	if err != nil {
		return nil, nil, false, err
	}
	if d.ok {
		_, conn, err := o.clients(ctx)
		if err != nil {
			return nil, nil, false, err
		}
		l, err := client.ListHeld(ctx, conn, openshellWorkspace, w.Name, d.bin, w.Name)
		if err != nil {
			return nil, nil, false, err
		}
		names = runningTerms(l)
		o.termMu.Lock()
		if o.termStarted == nil {
			o.termStarted = map[string]time.Time{}
		}
		maps.DeleteFunc(o.termStarted, func(k string, _ time.Time) bool { return strings.HasPrefix(k, w.Name+"/") })
		for _, t := range l.Terminals {
			o.termStarted[w.Name+"/"+t.Name] = time.UnixMilli(t.Started)
		}
		o.termMu.Unlock()
		if len(names) == 0 {
			names = []string{mainTerm}
		}
		// Only a daemon that a restart renews: an image's keeps working as
		// it is, and what it lacks shows where it is used.
		if d.restartUpdates && l.Version < termd.Version {
			outdated = slices.Clone(names)
		}
		return names, outdated, true, nil
	}
	o.termMu.Lock()
	defer o.termMu.Unlock()
	names = []string{mainTerm}
	for name, s := range o.terms[w.Name] {
		select {
		case <-s.Done():
			delete(o.terms[w.Name], name)
			continue
		default:
		}
		if name != mainTerm {
			names = append(names, name)
		}
	}
	sortTerms(names[1:])
	return names, nil, true, nil
}

// sortTerms sorts terminal names, numbers by their value.
func sortTerms(names []string) {
	slices.SortFunc(names, func(a, b string) int {
		na, ea := strconv.Atoi(a)
		nb, eb := strconv.Atoi(b)
		if ea == nil && eb == nil {
			return na - nb
		}
		return strings.Compare(a, b)
	})
}

// SandboxRestartTerms are the terminals among names that only a restart of
// the sandbox renews: all while its daemon is older than this Iwato's and
// a restart renews it, and main in a sandbox without one (its main process).
func (o *OpenShell) SandboxRestartTerms(w Sandbox, names []string) []string {
	o.termMu.Lock()
	d, known := o.daemons[w.Name]
	o.termMu.Unlock()
	switch {
	case known && d.ok && d.restartUpdates && d.version < termd.Version:
		return slices.Clone(names)
	case known && d.ok:
		return []string{}
	}
	return []string{mainTerm}
}

// termSettle is how long a new attachment's screen takes to settle: the
// replayed output and the repaint it asks for.
const termSettle = 5 * time.Second

// TerminalScreens returns the screens of the sandbox's running terminals
// (not those attached less than termSettle ago).
func (o *OpenShell) TerminalScreens(w Sandbox) map[string]termScreen {
	o.termMu.Lock()
	sessions := maps.Clone(o.terms[w.Name])
	o.termMu.Unlock()
	out := map[string]termScreen{}
	for name, s := range sessions {
		select {
		case <-s.Done():
			continue
		default:
		}
		if time.Since(s.Started()) < termSettle {
			continue
		}
		t := termScreen{attached: s.Started()}
		t.cols, t.rows, t.lines = s.Screen()
		t.activity = s.Activity()
		out[name] = t
	}
	return out
}

// runningTerms lists the daemon's terminals, main first.
func runningTerms(l *termd.List) []string {
	var names []string
	main := false
	for _, t := range l.Terminals {
		switch {
		case t.Name == mainTerm:
			main = true
		case validTermName(t.Name):
			names = append(names, t.Name)
		}
	}
	sortTerms(names)
	if main {
		names = append([]string{mainTerm}, names...)
	}
	return names
}

// KillTerminal ends one of the terminals. Without a daemon the main
// terminal is the sandbox's main process: stop the sandbox instead.
func (o *OpenShell) KillTerminal(ctx context.Context, w Sandbox, name string) error {
	d, err := o.daemon(ctx, w)
	if err != nil {
		return err
	}
	if name == mainTerm && !d.ok {
		return errors.New("メインのターミナルは終了できません (サンドボックスを停止してください)")
	}
	o.termMu.Lock()
	s := o.terms[w.Name][name]
	delete(o.terms[w.Name], name)
	o.termMu.Unlock()
	if !d.ok {
		if s != nil {
			s.Kill()
		}
		return nil
	}
	if s != nil {
		// Through its attachment, and through a kill of its own when that
		// did not end it.
		s.Kill()
		select {
		case <-s.Done():
		case <-time.After(10 * time.Second):
		}
		s.Close()
		if errors.Is(s.Err(), io.EOF) {
			return nil
		}
	}
	_, conn, err := o.clients(ctx)
	if err != nil {
		return err
	}
	return client.KillHeld(ctx, conn, openshellWorkspace, w.Name, d.bin, w.Name, name)
}

// TermDir is the working directory of one of the sandbox's terminals as
// far as known (hostterm.Session.Dir), "" otherwise.
func (o *OpenShell) TermDir(ctx context.Context, w Sandbox, name string) string {
	o.termMu.Lock()
	s := o.terms[w.Name][name]
	o.termMu.Unlock()
	if s == nil {
		return ""
	}
	return s.Dir(ctx)
}

// dropTerminals forgets a stopped or deleted sandbox's terminals.
func (o *OpenShell) dropTerminals(w Sandbox) {
	o.termMu.Lock()
	sessions := o.terms[w.Name]
	delete(o.terms, w.Name)
	delete(o.daemons, w.Name)
	o.termMu.Unlock()
	for _, s := range sessions {
		s.Close()
	}
}

// TerminalStarted tells when one of the sandbox's terminals started: as
// its daemon said (Terminals and Attach ask), or when Iwato started one
// of the shells it holds (not main without a daemon: the sandbox's main
// process).
func (o *OpenShell) TerminalStarted(w Sandbox, name string) (time.Time, bool) {
	o.termMu.Lock()
	defer o.termMu.Unlock()
	if !o.daemons[w.Name].ok && (name == mainTerm || o.terms[w.Name][name] == nil) {
		return time.Time{}, false
	}
	at, ok := o.termStarted[w.Name+"/"+name]
	return at, ok
}

// GatewayImage is the image the gateway container runs (update.go).
func (o *OpenShell) GatewayImage(ctx context.Context) string {
	return gateway.ContainerImage(ctx, o.gatewayCLI(), o.e.config.prefix())
}
