package wslc

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os/exec"
	"strings"
	"time"
)

// runner runs one wslc command, optionally feeding stdin; tests replace it.
type runner interface {
	Run(ctx context.Context, stdin []byte, args ...string) ([]byte, error)
}

type execRunner struct{ exe string }

// slowCall is how long a wslc call may take before it is logged.
const slowCall = time.Second

func (r execRunner) Run(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	began := time.Now()
	defer func() {
		if t := time.Since(began); t >= slowCall {
			log.Printf("slow: wslc %s took %s", strings.Join(redact(args), " "), t.Round(100*time.Millisecond))
		}
	}()
	cmd := exec.CommandContext(ctx, r.exe, args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if len(args) > 1 && args[0] == "container" && args[1] == "logs" {
		// A container's stderr comes back on wslc's stderr.
		cmd.Stderr = nil
		out, err := cmd.CombinedOutput()
		if err != nil {
			return out, fmt.Errorf("wslc %s: %w", strings.Join(args, " "), err)
		}
		return out, nil
	}
	out, err := cmd.Output()
	if err != nil {
		return out, fmt.Errorf("wslc %s: %w: %s", strings.Join(redact(args), " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// redact keeps environment values (they may carry tokens) out of errors.
func redact(args []string) []string {
	out := append([]string(nil), args...)
	for i := 0; i+1 < len(out); i++ {
		if out[i] == "--env" {
			if k, _, ok := strings.Cut(out[i+1], "="); ok {
				out[i+1] = k + "=…"
			}
		}
	}
	return out
}

// errNotFound is what inspect reports for a missing object (`[]`, exit 1).
var errNotFound = errors.New("not found")

// wslc wraps the WSLc commands the driver uses. JSON shapes follow WSL's
// wslc_schema.h (3.0.1): WSLc reshapes Docker's inspect documents.
type wslc struct{ r runner }

type containerInfo struct {
	ID    string `json:"Id"`
	Name  string `json:"Name"`
	Image string `json:"Image"`
	State struct {
		Status     string `json:"Status"`
		Running    bool   `json:"Running"`
		ExitCode   int    `json:"ExitCode"`
		StartedAt  string `json:"StartedAt"`
		FinishedAt string `json:"FinishedAt"`
		OOMKilled  bool   `json:"OOMKilled"`
	} `json:"State"`
	HostConfig struct {
		NetworkMode string `json:"NetworkMode"`
	} `json:"HostConfig"`
	Config struct {
		User       string            `json:"User"`
		WorkingDir string            `json:"WorkingDir"`
		Labels     map[string]string `json:"Labels"`
	} `json:"Config"`
	Labels          map[string]string `json:"Labels"`
	NetworkSettings struct {
		Networks map[string]json.RawMessage `json:"Networks"`
	} `json:"NetworkSettings"`
}

func (c containerInfo) label(key string) string {
	if v, ok := c.Labels[key]; ok {
		return v
	}
	return c.Config.Labels[key]
}

type imageInfo struct {
	ID     string `json:"Id"`
	Config *struct {
		User       string              `json:"User"`
		WorkingDir string              `json:"WorkingDir"`
		Volumes    map[string]struct{} `json:"Volumes"`
	} `json:"Config"`
}

func (w wslc) inspect(ctx context.Context, kind, name string, v any) error {
	out, err := w.r.Run(ctx, nil, kind, "inspect", "--format", "json", name)
	trimmed := bytes.TrimSpace(out)
	// A container removed between list and inspect comes back as the
	// engine's "No such container" error instead of `[]`.
	if bytes.Equal(trimmed, []byte("[]")) || (err != nil && strings.Contains(strings.ToLower(err.Error()), "no such ")) {
		return errNotFound
	}
	if err != nil {
		return err
	}
	var list []json.RawMessage
	if err := json.Unmarshal(trimmed, &list); err != nil || len(list) != 1 {
		return fmt.Errorf("unexpected %s inspect output for %s", kind, name)
	}
	return json.Unmarshal(list[0], v)
}

func (w wslc) container(ctx context.Context, name string) (containerInfo, error) {
	var c containerInfo
	return c, w.inspect(ctx, "container", name, &c)
}

func (w wslc) image(ctx context.Context, ref string) (imageInfo, error) {
	var i imageInfo
	return i, w.inspect(ctx, "image", ref, &i)
}

// ensureImage returns the inspected image, pulling it when missing.
func (w wslc) ensureImage(ctx context.Context, ref string) (imageInfo, error) {
	img, err := w.image(ctx, ref)
	if errors.Is(err, errNotFound) {
		if _, err = w.r.Run(ctx, nil, "image", "pull", ref); err != nil {
			return img, err
		}
		img, err = w.image(ctx, ref)
	}
	if err == nil && img.ID == "" {
		err = fmt.Errorf("image %s has no ID", ref)
	}
	return img, err
}

// create runs `container create` with flags before the image and returns
// the new container's ID.
func (w wslc) create(ctx context.Context, flags []string, image string, args ...string) (string, error) {
	a := append(append([]string{"container", "create"}, flags...), image)
	out, err := w.r.Run(ctx, nil, append(a, args...)...)
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(string(out))
	if i := strings.LastIndexByte(id, '\n'); i >= 0 {
		id = strings.TrimSpace(id[i+1:])
	}
	if id == "" {
		return "", errors.New("container create printed no ID")
	}
	return id, nil
}

func (w wslc) start(ctx context.Context, id string) error {
	_, err := w.r.Run(ctx, nil, "container", "start", id)
	return err
}

func (w wslc) stop(ctx context.Context, id string, timeoutSecs int) error {
	_, err := w.r.Run(ctx, nil, "container", "stop", "--time", fmt.Sprint(timeoutSecs), id)
	return err
}

// remove force-removes a container; a missing one is not an error.
func (w wslc) remove(ctx context.Context, id string) error {
	_, err := w.r.Run(ctx, nil, "container", "remove", "--force", id)
	if err != nil {
		if _, ierr := w.container(ctx, id); errors.Is(ierr, errNotFound) {
			return nil
		}
	}
	return err
}

// volumeCreate creates a volume with labels; one that already exists must
// carry them all, so a volume made by someone else is never adopted (the
// Docker driver checks ownership the same way).
func (w wslc) volumeCreate(ctx context.Context, name string, labels map[string]string) error {
	var v struct {
		Labels map[string]string `json:"Labels"`
	}
	switch err := w.inspect(ctx, "volume", name, &v); {
	case err == nil:
		for k, want := range labels {
			if v.Labels[k] != want {
				return fmt.Errorf("volume %s exists without this sandbox's label %s=%s", name, k, want)
			}
		}
		return nil
	case !errors.Is(err, errNotFound):
		return err
	}
	a := []string{"volume", "create"}
	for _, k := range sortedKeys(labels) {
		a = append(a, "--label", k+"="+labels[k])
	}
	_, err := w.r.Run(ctx, nil, append(a, name)...)
	return err
}

func (w wslc) volumeRemove(ctx context.Context, name string) error {
	_, err := w.r.Run(ctx, nil, "volume", "remove", name)
	if err != nil {
		var v map[string]any
		if ierr := w.inspect(ctx, "volume", name, &v); errors.Is(ierr, errNotFound) {
			return nil
		}
	}
	return err
}

// copyIn extracts a tar archive into a container; WSLc keeps the uid, gid
// and modes of the tar headers (Docker's PUT /archive).
func (w wslc) copyIn(ctx context.Context, id, dir string, archive []byte) error {
	_, err := w.r.Run(ctx, archive, "container", "cp", "-", id+":"+dir)
	return err
}

// errNotRegular is copyOut's error for a path that is in the image but is
// not a regular file.
var errNotRegular = errors.New("not a regular file")

// copyOut copies one file out of a (possibly never started) container. It
// reads the tar stream of `container cp ID:PATH -` instead of writing to the
// host: only a regular entry is accepted (as the Docker driver does), and on
// Windows wslc cannot even write a symlink out to a host directory.
func (w wslc) copyOut(ctx context.Context, id, path string) ([]byte, error) {
	out, err := w.r.Run(ctx, nil, "container", "cp", id+":"+path, "-")
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(bytes.NewReader(out))
	h, err := tr.Next()
	if err != nil {
		return nil, fmt.Errorf("copy %s out of the image: %w", path, err)
	}
	if h.Typeflag != tar.TypeReg {
		return nil, fmt.Errorf("%s in the image: %w", path, errNotRegular)
	}
	return io.ReadAll(tr)
}

// list returns the IDs of all containers carrying every label.
func (w wslc) list(ctx context.Context, labels map[string]string) ([]string, error) {
	a := []string{"container", "list", "--all", "--quiet", "--no-trunc"}
	for _, k := range sortedKeys(labels) {
		a = append(a, "--filter", "label="+k+"="+labels[k])
	}
	out, err := w.r.Run(ctx, nil, a...)
	if err != nil {
		return nil, err
	}
	return strings.Fields(string(out)), nil
}

func (w wslc) exec(ctx context.Context, id string, args ...string) error {
	_, err := w.r.Run(ctx, nil, append([]string{"container", "exec", id}, args...)...)
	return err
}

func (w wslc) logsTail(ctx context.Context, id string, lines int) string {
	out, _ := w.r.Run(ctx, nil, "container", "logs", "--tail", fmt.Sprint(lines), id)
	if len(out) > 16<<10 {
		out = out[len(out)-16<<10:]
	}
	return strings.TrimSpace(string(out))
}
