package main

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// On a Docker engine a home volume gets the labels OpenShell's Docker
// driver admits, inspects use Docker's default JSON, and the sandbox mounts
// it (with a 1 GiB /dev/shm) through the docker driver_config.
func TestDockerEngineVolumeAndDriverConfig(t *testing.T) {
	notFound := exec.Command("false").Run() // status 1, as for a missing object
	var calls []string
	created := false
	e := &Engine{config: testConfig(), docker: true}
	e.run = func(_ context.Context, a ...string) ([]byte, error) {
		a = e.args(a)
		calls = append(calls, strings.Join(a, " "))
		switch {
		case a[0] == "volume" && a[1] == "create":
			created = true
			return nil, nil
		case a[0] == "volume" && a[1] == "inspect" && created:
			return []byte(`[{"Name":"v","Driver":"local","Options":null,"Labels":{"iwato.owner":"` + e.config.Instance +
				`","openshell.ai/sandbox-attachable":"true","openshell.ai/sandbox-attachable-workspace":"default"}}]`), nil
		}
		return []byte("[]"), notFound
	}
	if err := e.volume(context.Background(), "v", map[string]string{sandboxKey: "dev"}); err != nil {
		t.Fatal(err, calls)
	}
	want := "volume inspect v\nvolume create --label iwato.owner=" + e.config.Instance +
		" --label iwato.sandbox=dev --label openshell.ai/sandbox-attachable=true --label openshell.ai/sandbox-attachable-workspace=default v\nvolume inspect v"
	if got := strings.Join(calls, "\n"); got != want {
		t.Fatalf("calls:\n%s", got)
	}
	// A bind-backed local volume is refused.
	if err := e.checkVolume(map[string]any{"Driver": "local", "Options": map[string]any{"o": "bind"}}); err == nil {
		t.Fatal("accepted a bind-backed volume")
	}

	o := &OpenShell{e: e}
	mounts := o.driverConfig(newSandbox("dev"))["docker"].(map[string]any)["mounts"].([]any)
	home, shm := mounts[0].(map[string]any), mounts[len(mounts)-1].(map[string]any)
	if home["target"] != homeDir || home["read_only"] != false || shm["type"] != "tmpfs" || shm["target"] != "/dev/shm" || shm["size_bytes"] != float64(1<<30) {
		t.Fatal(mounts)
	}
}

// A Docker engine offers a GPU only with CDI and an NVIDIA CDI device, as
// OpenShell's Docker driver requires; WSLc always does.
func TestEngineGPUAvailable(t *testing.T) {
	for _, c := range []struct {
		info string
		want bool
	}{
		{`{"CDISpecDirs":["/etc/cdi","/var/run/cdi"],"DiscoveredDevices":[{"Source":"cdi","ID":"nvidia.com/gpu=0"}]}`, true},
		{`{"CDISpecDirs":["/etc/cdi"],"DiscoveredDevices":[{"Source":"cdi","ID":"nvidia.com/gpu=all"}]}`, true},
		{`{"CDISpecDirs":["/etc/cdi"],"DiscoveredDevices":[{"Source":"cdi","ID":"vendor.com/device=x"}]}`, false},
		{`{"CDISpecDirs":["/etc/cdi"]}`, false},
		{`{"CDISpecDirs":[],"DiscoveredDevices":[{"Source":"cdi","ID":"nvidia.com/gpu=0"}]}`, false},
		{`{"OperatingSystem":"Docker Desktop"}`, false},
		{`not json`, true}, // unknown: OpenShell decides at creation
	} {
		e := &Engine{config: testConfig(), docker: true}
		e.run = func(context.Context, ...string) ([]byte, error) { return []byte(c.info), nil }
		if got := e.gpuAvailable(context.Background()); got != c.want {
			t.Errorf("%s: %v", c.info, got)
		}
	}
	// A failure keeps the last answer.
	e := &Engine{config: testConfig(), docker: true}
	e.run = func(context.Context, ...string) ([]byte, error) { return []byte(`{"CDISpecDirs":["/etc/cdi"]}`), nil }
	e.gpuAvailable(context.Background())
	e.gpu.at = time.Time{}
	e.run = func(context.Context, ...string) ([]byte, error) { return nil, errors.New("timeout") }
	if e.gpuAvailable(context.Background()) {
		t.Error("a failed docker info dropped the last answer")
	}
	if !(&Engine{}).gpuAvailable(context.Background()) {
		t.Error("WSLc offers no GPU")
	}
}
