// Package dockercli runs the docker CLI with the commands Iwato gives
// wslc. The CLI picks the engine (DOCKER_HOST, DOCKER_CONTEXT, `docker
// context use`).
package dockercli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// Args turns a wslc command into the docker one: `KIND inspect --format
// json NAME...` loses its format (Docker prints the same JSON array, and
// `[]` with status 1 for a missing object, by default).
func Args(args []string) []string {
	if len(args) >= 4 && args[1] == "inspect" && args[2] == "--format" && args[3] == "json" {
		return append(append([]string{}, args[:2]...), args[4:]...)
	}
	return args
}

// Runner runs the docker executable Exe; it is a wslc.Runner (and
// wslc.Streamer) for the gateway's commands.
type Runner struct{ Exe string }

// slowCall is how long a call may take before it is logged.
const slowCall = time.Second

// Run runs one command, feeding stdin when it is not nil.
func (r Runner) Run(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	args = Args(args)
	began := time.Now()
	defer func() {
		if t := time.Since(began); t >= slowCall {
			log.Printf("slow: docker %s took %s", strings.Join(Redact(args), " "), t.Round(100*time.Millisecond))
		}
	}()
	cmd := exec.CommandContext(ctx, r.Exe, args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	if len(args) > 1 && args[0] == "container" && args[1] == "logs" {
		// A container's stderr comes back on the CLI's stderr.
		out, err := cmd.CombinedOutput()
		if err != nil {
			return out, fmt.Errorf("docker %s: %w", strings.Join(args, " "), err)
		}
		return out, nil
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return out, fmt.Errorf("docker %s: %w: %s", strings.Join(Redact(args), " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// Stream runs a long command (an image build or pull), writing its output
// to w as it comes.
func (r Runner) Stream(ctx context.Context, w io.Writer, args ...string) error {
	args = Args(args)
	cmd := exec.CommandContext(ctx, r.Exe, args...)
	cmd.Stdout, cmd.Stderr = w, w
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker %s: %w", strings.Join(Redact(args), " "), err)
	}
	return nil
}

// Redact keeps environment values (they may carry tokens) out of errors.
func Redact(args []string) []string {
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

// DefaultSocket is where the engine's own API socket is on the machine
// that runs the containers. On macOS that is the engine's Linux VM, where
// Colima, Docker Desktop and OrbStack all serve it at this path, whatever
// socket the CLI uses on the Mac.
const DefaultSocket = "/var/run/docker.sock"

// Socket returns the path of the engine's API socket as the engine sees it
// (the source of a bind mount into the gateway's container). That is
// DefaultSocket but for rootless Docker on Linux, whose daemon runs here as
// the user and serves the CLI's unix:// endpoint (DOCKER_HOST or the
// context's). Engines in a VM (Docker Desktop and Colima on Linux too)
// serve DefaultSocket inside it, whatever socket the CLI uses here.
func Socket(ctx context.Context, exe string) string {
	if runtime.GOOS != "linux" {
		return DefaultSocket
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, exe, "info", "--format", "{{json .SecurityOptions}}").Output()
	if err != nil || !strings.Contains(string(out), "rootless") {
		return DefaultSocket
	}
	host := os.Getenv("DOCKER_HOST")
	if host == "" {
		out, err := exec.CommandContext(ctx, exe, "context", "inspect", "--format", "{{.Endpoints.docker.Host}}").Output()
		if err == nil {
			host = strings.TrimSpace(string(out))
		}
	}
	return socketPath(host)
}

// socketPath is the path of a unix:// endpoint, DefaultSocket otherwise.
func socketPath(host string) string {
	if u, err := url.Parse(host); err == nil && u.Scheme == "unix" && strings.HasPrefix(u.Path, "/") {
		return u.Path
	}
	return DefaultSocket
}
