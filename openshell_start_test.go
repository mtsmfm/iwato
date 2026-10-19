package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	v1 "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1"
	"github.com/NVIDIA/OpenShell/sdk/go/openshell/v1/types"
)

// fakeSandboxes is the gateway's sandbox API for one sandbox: each Get
// shows the next of states (the last one stays), as OpenShell would report
// them while it provisions the sandbox.
type fakeSandboxes struct {
	v1.SandboxInterface
	mu     sync.Mutex
	states []*v1.Sandbox
	gets   int
	calls  []string
	specs  []*v1.SandboxSpec // what Create got
}

func (f *fakeSandboxes) Get(ctx context.Context, _, name string) (*v1.Sandbox, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(f.states) == 0 {
		return nil, &v1.StatusError{Code: v1.ErrorNotFound, Message: "sandbox not found"}
	}
	sb := f.states[0]
	if len(f.states) > 1 {
		f.states = f.states[1:]
	}
	f.gets++
	if sb == nil {
		return nil, &v1.StatusError{Code: v1.ErrorNotFound, Message: "sandbox not found"}
	}
	return sb, nil
}

func (f *fakeSandboxes) record(call string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
}

func (f *fakeSandboxes) Create(_ context.Context, _, name string, spec *v1.SandboxSpec, _ map[string]string, _ ...v1.CreateOptions) (*v1.Sandbox, error) {
	f.record("create")
	f.mu.Lock()
	f.specs = append(f.specs, spec)
	f.mu.Unlock()
	return &v1.Sandbox{}, nil
}

func (f *fakeSandboxes) Start(_ context.Context, _, name string) (*v1.Sandbox, error) {
	f.record("start")
	return &v1.Sandbox{}, nil
}

func (f *fakeSandboxes) Delete(_ context.Context, _, name string, _ ...v1.DeleteOptions) (*v1.DeletionResult, error) {
	f.record("delete")
	return &v1.DeletionResult{}, nil
}

func (f *fakeSandboxes) Stop(_ context.Context, _, name string) (*v1.Sandbox, error) {
	f.record("stop")
	return &v1.Sandbox{}, nil
}

func sandboxIn(phase v1.SandboxPhase, conds ...v1.SandboxCondition) *v1.Sandbox {
	sb := &v1.Sandbox{}
	sb.Status.Phase = phase
	sb.Status.Conditions = conds
	return sb
}

// rejected is what OpenShell v0.1.2 shows while the supervisor rejects the
// sandbox's effective configuration: still Provisioning, the admission
// rejected with the gateway's diagnostic, Ready=False ConfigurationInvalid.
func rejected(msg string) *v1.Sandbox {
	sb := sandboxIn(v1.SandboxProvisioning,
		v1.SandboxCondition{Type: "ConfigurationReady", Status: "False", Reason: "ConfigurationInvalid", Message: msg},
		v1.SandboxCondition{Type: "Ready", Status: "False", Reason: "ConfigurationInvalid", Message: msg})
	sb.Status.ConfigurationAdmission = &types.SandboxConfigurationAdmission{State: types.ConfigurationAdmissionRejected, Error: msg}
	return sb
}

const l4Only = "credentialed endpoint 'api.anthropic.com:443' in rule 'allow_api_anthropic_com_443' uses L4-only"

func testOpenShell(api *fakeSandboxes) *OpenShell {
	ready := make(chan struct{})
	close(ready)
	// The home's volume (homeDir) is made at every start: it exists.
	e := &Engine{config: testConfig()}
	e.run = func(_ context.Context, a ...string) ([]byte, error) {
		name := a[len(a)-1]
		return []byte(`[{"Name":"` + name + `","Driver":"guest","Labels":{"iwato.owner":"` + e.config.Instance + `"}}]`), nil
	}
	return &OpenShell{e: e, ready: ready, sandboxAPI: api, pollEvery: time.Millisecond}
}

func TestStartFailsAsSoonAsOpenShellRejectsTheConfiguration(t *testing.T) {
	pending := sandboxIn(v1.SandboxProvisioning, v1.SandboxCondition{Type: "Ready", Status: "False", Reason: "ConfigurationPending", Message: "Waiting for effective configuration validation before workload activation"})
	api := &fakeSandboxes{states: []*v1.Sandbox{nil, pending, pending, rejected(l4Only)}}
	o := testOpenShell(api)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := o.Start(ctx, newSandbox("dev"))
	if err == nil || !strings.Contains(err.Error(), l4Only) || !strings.Contains(err.Error(), "ポリシー") {
		t.Fatalf("start: %v", err)
	}
	if ctx.Err() != nil {
		t.Fatal("waited for the timeout")
	}
	if strings.Join(api.calls, ",") != "create" {
		t.Fatal(api.calls)
	}
}

func TestStartWaitsWhileTheSandboxProgresses(t *testing.T) {
	// Transient reasons (OpenShell's is_terminal_failure_reason) keep it waiting.
	var states []*v1.Sandbox
	for _, reason := range []string{"DependenciesNotReady", "ContainerCreated", "Starting", "SupervisorNotConnected", "ConfigurationPending"} {
		states = append(states, sandboxIn(v1.SandboxProvisioning, v1.SandboxCondition{Type: "Ready", Status: "False", Reason: reason, Message: "pulling image"}))
	}
	stopped := sandboxIn(v1.SandboxStopped)
	api := &fakeSandboxes{states: append(append([]*v1.Sandbox{stopped}, states...), sandboxIn(v1.SandboxReady))}
	o := testOpenShell(api)
	if err := o.Start(context.Background(), newSandbox("dev")); err != nil {
		t.Fatal(err)
	}
	if strings.Join(api.calls, ",") != "start" || api.gets != len(states)+2 {
		t.Fatal(api.calls, api.gets)
	}
}

func TestStartReportsOpenShellsErrorMessage(t *testing.T) {
	failed := sandboxIn(v1.SandboxError, v1.SandboxCondition{Type: "Ready", Status: "False", Reason: "ImagePullFailed", Message: "image not found"})
	api := &fakeSandboxes{states: []*v1.Sandbox{nil, sandboxIn(v1.SandboxProvisioning), failed}}
	err := testOpenShell(api).Start(context.Background(), newSandbox("dev"))
	if err == nil || !strings.Contains(err.Error(), "image not found") {
		t.Fatal(err)
	}
}

// A sandbox whose repair window expired is started again, keeping its
// policy (which the owner may have fixed), not deleted and made anew.
func TestStartRestartsATimedOutSandboxWithItsPolicy(t *testing.T) {
	timedOut := sandboxIn(v1.SandboxError, v1.SandboxCondition{Type: "Ready", Status: "False", Reason: "ProvisioningTimedOut", Message: "Provisioning repair window expired after 300 seconds: " + l4Only})
	api := &fakeSandboxes{states: []*v1.Sandbox{timedOut, sandboxIn(v1.SandboxStarting), sandboxIn(v1.SandboxReady)}}
	if err := testOpenShell(api).Start(context.Background(), newSandbox("dev")); err != nil {
		t.Fatal(err)
	}
	if strings.Join(api.calls, ",") != "start" {
		t.Fatal(api.calls)
	}
}

// A failed sandbox that Start makes again by itself keeps an older
// sandbox's volumes outside the home (mounted and writable): only the
// owner's recreate drops them.
func TestStartRemakesAFailedSandboxWithItsPersistedPaths(t *testing.T) {
	failed := sandboxIn(v1.SandboxError, v1.SandboxCondition{Type: "Ready", Status: "False", Reason: "ContainerStartFailed", Message: "gone"})
	api := &fakeSandboxes{states: []*v1.Sandbox{failed, nil, nil, sandboxIn(v1.SandboxReady)}}
	o := testOpenShell(api)
	w := newSandbox("dev")
	w.LegacyPersist = []string{"/sandbox/data"}
	if err := o.Start(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	if strings.Join(api.calls, ",") != "delete,create" || len(api.specs) != 1 {
		t.Fatal(api.calls)
	}
	spec := api.specs[0]
	var targets []string
	for _, m := range spec.Template.DriverConfig["wslc"].(map[string]any)["mounts"].([]any) {
		mm := m.(map[string]any)
		targets = append(targets, mm["target"].(string))
		if mm["source"] != persistVolume(o.e.config, w, mm["target"].(string)) {
			t.Errorf("mount %v: not the path's volume", mm)
		}
	}
	if strings.Join(targets, ",") != homeDir+",/sandbox/data" {
		t.Errorf("mounts: %v", targets)
	}
	rw := strings.Join(spec.Policy.Filesystem.ReadWrite, " ")
	if !strings.Contains(rw, "/sandbox/data") || !strings.Contains(rw, homeDir) {
		t.Errorf("writable: %v", spec.Policy.Filesystem.ReadWrite)
	}
}

func TestStartEndsWhenItsContextIsCanceled(t *testing.T) {
	api := &fakeSandboxes{states: []*v1.Sandbox{sandboxIn(v1.SandboxStopped), sandboxIn(v1.SandboxStarting)}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- testOpenShell(api).Start(ctx, newSandbox("dev")) }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "やめました") {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancel did not end the start")
	}
}

func TestStatusShowsWhyTheSandboxIsNotReady(t *testing.T) {
	for _, c := range []struct {
		sb            *v1.Sandbox
		state, detail string
	}{
		{rejected(l4Only), "error", l4Only},
		{sandboxIn(v1.SandboxStarting, v1.SandboxCondition{Type: "ConfigurationReady", Status: "False", Reason: "ConfigurationInvalid", Message: l4Only}), "error", l4Only},
		{sandboxIn(v1.SandboxProvisioning, v1.SandboxCondition{Type: "Ready", Status: "False", Reason: "DependenciesNotReady", Message: "Pulling image"}), "starting", "Pulling image"},
		{sandboxIn(v1.SandboxStarting), "starting", "Starting…"},
		{sandboxIn(v1.SandboxError, v1.SandboxCondition{Type: "ConfigurationReady", Status: "False", Message: "other"}, v1.SandboxCondition{Type: "Ready", Status: "False", Reason: "ProvisioningTimedOut", Message: "Provisioning repair window expired after 300 seconds"}), "error", "repair window expired"},
		{sandboxIn(v1.SandboxReady), "running", ""},
	} {
		s := testOpenShell(&fakeSandboxes{states: []*v1.Sandbox{c.sb}}).Status(context.Background(), newSandbox("dev"))
		if s.State != c.state || !strings.Contains(s.Detail, c.detail) {
			t.Errorf("%s: %+v", c.sb.Status.Phase, s)
		}
	}
}

func TestStopRefusesASandboxStillProvisioning(t *testing.T) {
	api := &fakeSandboxes{states: []*v1.Sandbox{sandboxIn(v1.SandboxProvisioning)}}
	if err := testOpenShell(api).Stop(context.Background(), newSandbox("dev")); !errors.Is(err, errNotStoppable) {
		t.Fatal(err)
	}
	if len(api.calls) != 0 {
		t.Fatal(api.calls)
	}
}
