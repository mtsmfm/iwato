package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"

	"iwato/internal/dockercli"
)

// Engine runs the engine's CLI (wslc.exe on Windows, docker elsewhere) for
// what Iwato manages itself: its images and the home volumes.
type Engine struct {
	config Config
	exe    string
	global []string
	// docker: the engine is a Docker-compatible one (NewEngine picks it when
	// the configuration names no wslc); the zero value is WSLc.
	docker bool
	// run replaces the CLI in tests.
	run       func(ctx context.Context, a ...string) ([]byte, error)
	imageMu   sync.Mutex
	cleanupMu sync.Mutex
	images    map[string]imageEntry
	gpuMu     sync.Mutex
	gpu       *gpuEntry
	gpuBusy   bool
}

type gpuEntry struct {
	ok    bool
	known bool // false: `docker info` failed and nothing was known before
	at    time.Time
}

type imageEntry struct {
	id string
	at time.Time
}

func NewEngine(c Config) (*Engine, error) {
	if c.usesDocker() {
		e := &Engine{config: c, exe: lookPath(c.dockerCLI()), docker: true}
		e.run = e.cli
		return e, nil
	}
	e := &Engine{config: c, exe: lookPath(c.WSLC)}
	if c.Session != "" {
		e.global = []string{"--session", c.Session}
	}
	e.run = e.cli
	return e, nil
}

// lookPath is the CLI's full path, or its name when it is not installed
// yet: each run looks for it again, and the startup checks (checks.go)
// report it missing until it is there.
func lookPath(name string) string {
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	return name
}

// tool names the engine's CLI in messages.
func (e *Engine) tool() string {
	if e.docker {
		return "docker"
	}
	return "wslc"
}

// cappedBuffer keeps the first limit bytes written to it.
type cappedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if b.Len() < b.limit {
		if left := b.limit - b.Len(); len(p) > left {
			p = p[:left]
		}
		b.Buffer.Write(p)
	}
	return n, nil
}

func (e *Engine) args(a []string) []string {
	if e.docker {
		a = dockercli.Args(a)
	}
	return append(append([]string(nil), e.global...), a...)
}

func (e *Engine) cli(ctx context.Context, a ...string) ([]byte, error) {
	c := exec.CommandContext(ctx, e.exe, e.args(a)...)
	configureProcess(c)
	out := &cappedBuffer{limit: 2 << 20}
	stderr := &cappedBuffer{limit: 8192}
	c.Stdout, c.Stderr = out, stderr
	if err := c.Run(); err != nil {
		return out.Bytes(), fmt.Errorf("%s %s: %w: %s", e.tool(), strings.Join(a[:min(2, len(a))], " "), err, strings.TrimSpace(stderr.String()))
	}
	return out.Bytes(), nil
}

// inspect returns the object, or nil when it does not exist.
func (e *Engine) inspect(ctx context.Context, kind, name string) (map[string]any, error) {
	out, err := e.run(ctx, kind, "inspect", "--format", "json", name)
	var list []map[string]any
	if je := json.Unmarshal(out, &list); je != nil {
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("invalid %s inspect JSON for %s", e.tool(), name)
	}
	// WSLc (and Docker) return [] with status 1 only for not found. Never treat
	// arbitrary failures as absence.
	var ee *exec.ExitError
	if len(list) == 0 && errors.As(err, &ee) && ee.ExitCode() == 1 {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(list) != 1 {
		return nil, fmt.Errorf("unexpected inspect result for %s", name)
	}
	return list[0], nil
}

func obj(v any) map[string]any { m, _ := v.(map[string]any); return m }
func val(v any) string         { s, _ := v.(string); return s }

// checkOwner refuses a resource that this configuration did not create.
func (e *Engine) checkOwner(d map[string]any, name string) error {
	l := obj(d["Labels"])
	if l == nil {
		l = obj(obj(d["Config"])["Labels"])
	}
	if owner := val(l[ownerKey]); owner != e.config.Instance {
		if owner == "" {
			return fmt.Errorf("refusing resource %s: it has no Iwato ownership label (created by something else)", name)
		}
		return fmt.Errorf("refusing resource %s: it belongs to instance %s, not this config's instance %s; use a different \"namespace\" or remove that resource with %s", name, owner, e.config.Instance, e.tool())
	}
	return nil
}

// volume creates one of our volumes with the labels, or checks the one
// that exists (WSLc cannot change the labels of an existing volume). On a
// Docker engine it also carries the labels that let OpenShell's Docker
// driver attach it to a sandbox (resource admission, attachLabels).
func (e *Engine) volume(ctx context.Context, name string, labels map[string]string) error {
	d, err := e.inspect(ctx, "volume", name)
	if err != nil {
		return err
	}
	if e.docker {
		labels = maps.Clone(labels)
		maps.Copy(labels, attachLabels)
	}
	if d == nil {
		a := []string{"volume", "create", "--label", ownerKey + "=" + e.config.Instance}
		for _, k := range slices.Sorted(maps.Keys(labels)) {
			a = append(a, "--label", k+"="+labels[k])
		}
		if _, err = e.run(ctx, append(a, name)...); err != nil {
			return err
		}
		if d, err = e.inspect(ctx, "volume", name); err != nil {
			return err
		}
		if d == nil {
			return fmt.Errorf("created %s not found", name)
		}
	}
	if err := e.checkOwner(d, name); err != nil {
		return err
	}
	if err := e.checkVolume(d); err != nil {
		return fmt.Errorf("volume %s: %w", name, err)
	}
	if e.docker {
		// One without them (made by hand, say) is refused by the driver.
		l := obj(d["Labels"])
		for k, v := range attachLabels {
			if val(l[k]) != v {
				return errCode("volume_unattachable", map[string]any{"volume": name, "label": k + "=" + v},
					fmt.Sprintf("ボリューム %s に OpenShell が付けるためのラベル %s=%s がありません。「ボリューム」で削除する (データは消えます) か、データを新しいボリュームに移してください", name, k, v))
			}
		}
	}
	return nil
}

// attachLabels are the labels OpenShell's Docker driver requires on a
// volume a sandbox mounts (its default resource admission: approval, and
// the sandbox's OpenShell workspace).
var attachLabels = map[string]string{
	"openshell.ai/sandbox-attachable":           "true",
	"openshell.ai/sandbox-attachable-workspace": openshellWorkspace,
}

// removeVolume removes one of our volumes; a missing one is fine.
func (e *Engine) removeVolume(ctx context.Context, name string) error {
	d, err := e.inspect(ctx, "volume", name)
	if err != nil || d == nil {
		return err
	}
	if err := e.checkOwner(d, name); err != nil {
		return err
	}
	_, err = e.run(ctx, "volume", "remove", name)
	return err
}

// volumes lists this configuration's volumes. The labels, when present,
// name the sandbox and path.
func (e *Engine) volumes(ctx context.Context) ([]Volume, error) {
	out, err := e.run(ctx, "volume", "list", "--quiet", "--filter", "label="+ownerKey+"="+e.config.Instance)
	if err != nil {
		return nil, err
	}
	names := strings.Fields(string(out))
	if len(names) == 0 {
		return []Volume{}, nil
	}
	// One inspect for all. It fails with status 1 when one was removed
	// meanwhile, and still reports the others.
	out, err = e.run(ctx, append([]string{"volume", "inspect", "--format", "json"}, names...)...)
	var list []map[string]any
	if je := json.Unmarshal(out, &list); je != nil {
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("invalid %s volume inspect JSON", e.tool())
	}
	var ee *exec.ExitError
	if err != nil && !(errors.As(err, &ee) && ee.ExitCode() == 1) {
		return nil, err
	}
	vs := []Volume{}
	for _, d := range list {
		name := val(d["Name"])
		if e.checkOwner(d, name) != nil {
			continue
		}
		l := obj(d["Labels"])
		vs = append(vs, Volume{Name: name, Sandbox: val(l[sandboxKey]), Path: val(l[pathKey]), CreatedAt: val(d["CreatedAt"])})
	}
	return vs, nil
}

// checkVolume accepts only engine-managed persistent storage. WSLc: a
// "guest" volume with options may be a tmpfs, and "vhd" options must not
// change the filesystem. Docker: "local" with options is a bind or tmpfs.
func (e *Engine) checkVolume(d map[string]any) error {
	options := obj(d["Options"])
	if e.docker {
		if val(d["Driver"]) != "local" || len(options) != 0 {
			return errors.New("volume must use Docker's local driver without options (not bind/tmpfs/external)")
		}
		return nil
	}
	switch val(d["Driver"]) {
	case "guest":
		if len(options) != 0 {
			return errors.New("guest volume must have no driver options (tmpfs is not persistent)")
		}
	case "vhd":
		if _, ok := options["type"]; ok {
			return errors.New("vhd volume must not override the filesystem type")
		}
	default:
		return errors.New("volume driver must be WSLc guest or vhd (not bind/external)")
	}
	return nil
}

// imageID resolves a tag, cached briefly because status polls ask often.
func (e *Engine) imageID(ctx context.Context, tag string) string {
	e.imageMu.Lock()
	if c, ok := e.images[tag]; ok && time.Since(c.at) < time.Minute {
		e.imageMu.Unlock()
		return c.id
	}
	e.imageMu.Unlock()
	d, err := e.inspect(ctx, "image", tag)
	if err != nil || d == nil {
		return ""
	}
	id := val(d["Id"])
	e.imageMu.Lock()
	if e.images == nil {
		e.images = map[string]imageEntry{}
	}
	e.images[tag] = imageEntry{id, time.Now()}
	e.imageMu.Unlock()
	return id
}

func (e *Engine) forgetImage(tag string) {
	e.imageMu.Lock()
	delete(e.images, tag)
	e.imageMu.Unlock()
}

// gpuInfo is the part of `docker info` OpenShell's Docker driver reads for
// GPUs: CDI enabled (CDISpecDirs) and the NVIDIA devices CDI found
// (DiscoveredDevices, Docker 28.2 and later).
type gpuInfo struct {
	CDISpecDirs       []string
	DiscoveredDevices []struct{ Source, ID string }
}

// gpuAvailable tells whether a sandbox can get a GPU. WSLc always passes
// one through (or fails at creation on a machine without one). On a
// Docker engine it follows OpenShell's Docker driver, which refuses a GPU
// request without CDI or without an NVIDIA CDI device: no GPU on macOS,
// and on Linux only with the NVIDIA Container Toolkit's CDI spec. The
// answer is kept for a minute (the list asks on every refresh); one caller
// at a time runs `docker info`, with its own short timeout, and the others
// get the last answer. When it fails the last answer stays (unknown counts
// as available: OpenShell then refuses at creation if there is none).
func (e *Engine) gpuAvailable(context.Context) bool {
	if !e.docker {
		return true
	}
	e.gpuMu.Lock()
	last := e.gpu
	fresh := last != nil && time.Since(last.at) < time.Minute
	if fresh || e.gpuBusy {
		e.gpuMu.Unlock()
		return last == nil || !last.known || last.ok
	}
	e.gpuBusy = true
	e.gpuMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	raw, err := e.run(ctx, "info", "--format", "{{json .}}")
	cancel()
	var info gpuInfo
	next := gpuEntry{at: time.Now()}
	if err == nil && json.Unmarshal(raw, &info) == nil {
		next.known = true
		next.ok = len(info.CDISpecDirs) > 0 && slices.ContainsFunc(info.DiscoveredDevices, func(d struct{ Source, ID string }) bool {
			return d.Source == "cdi" && strings.HasPrefix(d.ID, "nvidia.com/gpu=")
		})
	} else if last != nil {
		next.ok, next.known = last.ok, last.known
	}
	e.gpuMu.Lock()
	e.gpu, e.gpuBusy = &next, false
	e.gpuMu.Unlock()
	return !next.known || next.ok
}

func timeout(parent context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, d)
}

// safeCLIError is an error for the page: CLI errors that mention a URL are
// replaced, since a login URL must never reach it.
func safeCLIError(err error) string {
	if err == nil {
		return ""
	}
	return safeText(err.Error())
}

// safeText is safeCLIError for a text.
func safeText(s string) string {
	if strings.Contains(s, "https://") {
		return "operation failed; inspect trusted owner status"
	}
	return s
}
