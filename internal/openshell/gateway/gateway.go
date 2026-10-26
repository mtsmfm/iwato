// Package gateway runs Iwato's OpenShell gateway as a container: the
// official Linux release in an image built from this directory, with
// mutual TLS toward Iwato and the supervisors. On WSLc it relays its
// compute-driver socket to the WSLc driver that Iwato serves on Windows'
// loopback; on a Docker-compatible engine (Options.DockerSocket) it runs
// upstream's Docker driver on the engine's socket (docs/adr/0002). The
// runner is the engine's CLI: wslc, or docker through dockercli.
package gateway

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"iwato/internal/openshell/wslc"
)

//go:embed Dockerfile entrypoint.sh gateway.toml gateway-docker.toml h2-l7.patch
var buildContext embed.FS

// contextFiles are the image's build context.
var contextFiles = []string{"Dockerfile", "entrypoint.sh", "gateway.toml", "gateway-docker.toml", "h2-l7.patch"}

// PatchedSupervisor: the image's supervisor is built from source with
// h2-l7.patch (the Dockerfile's OPENSHELL_PATCH), and the gateway image,
// which carries it at /openshell-supervisor, is the sandboxes' supervisor
// image (ImageRef) in place of the release's.
const PatchedSupervisor = true

const (
	// containerPort is where the gateway listens inside its container.
	containerPort = 17670
	labelImage    = "iwato.gateway.image"
	// labelEngine records what a Docker engine's gateway was made with
	// (engineLabel): a change makes it again. WSLc's has none.
	labelEngine  = "iwato.gateway.engine"
	readyTimeout = 2 * time.Minute
)

// Options name the gateway's resources and ports.
type Options struct {
	// Prefix names the WSLc resources: the network itself, <Prefix>-gateway
	// (container and image) and <Prefix>-state (volume).
	Prefix string
	// Dir keeps the local certificates: driver-tls/ for the driver's
	// listener and the relay, gateway-tls/ for the gateway's clients.
	Dir string
	// Port publishes the gateway on 127.0.0.1; DriverPort is where Iwato
	// serves the driver on Windows' loopback.
	Port, DriverPort int
	// Report, when set, is told each step (for the page: a code, its
	// params and the Japanese text); Log, when set, receives the image
	// build's output.
	Report func(code string, params map[string]any, message string)
	Log    string
	// Image, when set, is the published gateway image to pull instead of
	// building one from this directory (release exes).
	Image string
	// DockerSocket, when set, runs upstream's Docker driver on this socket
	// (its path on the engine's side; DriverPort is then unused).
	// SandboxLabel is the driver's sandbox_label.
	DockerSocket, SandboxLabel string
}

func (o Options) report(code string, params map[string]any, message string) {
	if o.Report != nil {
		o.Report(code, params, message)
	}
}

// Gateway is a running gateway: where Iwato and the supervisors reach it,
// and the files each side needs.
type Gateway struct {
	// Address is the gateway for Iwato (127.0.0.1:Port) and Endpoint for
	// the supervisors on Network.
	Address, Endpoint, Network string
	// The client bundle: gateway CA, certificate and key.
	CA, Cert, Key string
}

func (o Options) engineLabel() string {
	if o.DockerSocket == "" {
		return ""
	}
	return "docker " + o.DockerSocket + " " + o.SandboxLabel
}

func (o Options) container() string { return o.Prefix + "-gateway" }
func (o Options) volume() string    { return o.Prefix + "-state" }

// DriverTLS returns the driver listener's certificate, key and the CA of
// the relay's client certificate, creating them on first use.
func DriverTLS(dir string) (cert, key, clientCA string, err error) {
	d := filepath.Join(dir, "driver-tls")
	if _, err := os.Stat(filepath.Join(d, "relay.key")); errors.Is(err, fs.ErrNotExist) {
		if err := wslc.WriteCerts(d); err != nil {
			return "", "", "", err
		}
	}
	return filepath.Join(d, "server.pem"), filepath.Join(d, "server.key"), filepath.Join(d, "ca.pem"), nil
}

// Ensure builds the image when the embedded files changed, (re)creates the
// container when its image changed, starts it and waits until it serves
// mutual TLS. The driver must already listen on DriverPort.
func Ensure(ctx context.Context, run wslc.Runner, o Options) (Gateway, error) {
	g := Gateway{
		Address:  net.JoinHostPort("127.0.0.1", strconv.Itoa(o.Port)),
		Endpoint: fmt.Sprintf("https://%s:%d", o.container(), containerPort),
		Network:  o.Prefix,
		CA:       filepath.Join(o.Dir, "gateway-tls", "ca.crt"),
		Cert:     filepath.Join(o.Dir, "gateway-tls", "client.crt"),
		Key:      filepath.Join(o.Dir, "gateway-tls", "client.key"),
	}
	var v struct{}
	if o.DockerSocket != "" {
		// The supervisors use the engine's host network and reach the
		// gateway at 127.0.0.1 on the port published there, which
		// gateway-docker.toml names.
		g.Endpoint, g.Network = fmt.Sprintf("https://127.0.0.1:%d", containerPort), ""
		if o.Port != containerPort {
			return g, fmt.Errorf("the gateway's port must be %d on a Docker engine", containerPort)
		}
	} else {
		if _, _, _, err := DriverTLS(o.Dir); err != nil {
			return g, err
		}
		// Creating one that exists fails too: a real failure shows when the
		// container is created.
		if err := wslc.Inspect(ctx, run, "network", o.Prefix, &v); err != nil {
			_, _ = run.Run(ctx, nil, "network", "create", o.Prefix)
		}
	}
	if err := wslc.Inspect(ctx, run, "volume", o.volume(), &v); err != nil {
		_, _ = run.Run(ctx, nil, "volume", "create", o.volume())
	}
	o.report("gateway_checking", nil, "ゲートウェイのイメージを確認しています…")
	image, err := ensureImage(ctx, run, o)
	if err != nil {
		return g, err
	}
	o.report("gateway_starting", nil, "ゲートウェイを起動しています…")
	if err := ensureContainer(ctx, run, o, image); err != nil {
		return g, err
	}
	if err := copyClientBundle(ctx, run, o, g); err != nil {
		return g, err
	}
	o.report("gateway_waiting", nil, "ゲートウェイの応答を待っています…")
	return g, waitReady(ctx, run, o, g)
}

// imageTag names the image after the OpenShell release and the embedded
// build context, so a change to either builds a new one.
func imageTag(o Options) (string, error) {
	h := sha256.New()
	for _, name := range contextFiles {
		b, err := buildContext.ReadFile(name)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(h, "%s %d\n", name, len(b))
		h.Write(b)
	}
	return fmt.Sprintf("%s-gateway:%s-%s", o.Prefix, wslc.OpenShellVersion, hex.EncodeToString(h.Sum(nil))[:12]), nil
}

// ImageRef is the gateway image Ensure runs: the published one, or the
// one built from this directory.
func ImageRef(o Options) (string, error) {
	if o.Image != "" {
		return o.Image, nil
	}
	return imageTag(o)
}

func ensureImage(ctx context.Context, run wslc.Runner, o Options) (string, error) {
	tag, err := ImageRef(o)
	if err != nil {
		return "", err
	}
	var v struct{}
	if err := wslc.Inspect(ctx, run, "image", tag, &v); !errors.Is(err, wslc.ErrNotFound) {
		return tag, err
	}
	if o.Image != "" {
		log.Printf("pulling %s", tag)
		o.report("gateway_pulling", nil, "ゲートウェイのイメージを取得しています。初回は数分かかります…")
		return tag, o.stream(ctx, run, "pull "+tag, "image", "pull", tag)
	}
	dir, err := os.MkdirTemp("", "iwato-gateway-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	for _, name := range contextFiles {
		b, _ := buildContext.ReadFile(name)
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
			return "", err
		}
	}
	log.Printf("building %s (downloads OpenShell %s the first time)", tag, wslc.OpenShellVersion)
	o.report("gateway_building", map[string]any{"version": wslc.OpenShellVersion}, fmt.Sprintf("ゲートウェイのイメージをビルドしています。初回は OpenShell %s をダウンロードするので数分かかります…", wslc.OpenShellVersion))
	if err := o.stream(ctx, run, "build "+tag, "image", "build", "--tag", tag, "--target", "gateway",
		"--build-arg", "OPENSHELL_TAG=v"+wslc.OpenShellVersion, "--file", filepath.Join(dir, "Dockerfile"), dir); err != nil {
		return "", fmt.Errorf("build the OpenShell gateway image: %w", err)
	}
	return tag, nil
}

// stream runs a command of the engine's CLI whose output goes to Iwato's log and o.Log.
func (o Options) stream(ctx context.Context, run wslc.Runner, title string, args ...string) error {
	var w io.Writer = log.Writer()
	if o.Log != "" {
		if f, ferr := os.OpenFile(o.Log, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); ferr == nil {
			defer f.Close()
			fmt.Fprintf(f, "\n=== %s %s ===\n", time.Now().Format(time.RFC3339), title)
			w = io.MultiWriter(f, w)
		}
	}
	if st, ok := run.(wslc.Streamer); ok {
		return st.Stream(ctx, w, args...)
	}
	out, err := run.Run(ctx, nil, args...)
	_, _ = w.Write(out)
	return err
}

func ensureContainer(ctx context.Context, run wslc.Runner, o Options, image string) error {
	name := o.container()
	var c struct {
		State  struct{ Running bool }
		Config struct{ Labels map[string]string }
	}
	err := wslc.Inspect(ctx, run, "container", name, &c)
	switch {
	case err == nil && SameImage(c.Config.Labels[labelImage], image) && c.Config.Labels[labelEngine] == o.engineLabel():
		if c.State.Running {
			return nil
		}
		_, err = run.Run(ctx, nil, "container", "start", name)
		return err
	case err == nil:
		if _, err := run.Run(ctx, nil, "container", "remove", "--force", name); err != nil {
			return err
		}
	case !errors.Is(err, wslc.ErrNotFound):
		return err
	}
	if o.DockerSocket != "" {
		// The engine's socket gives the gateway the engine (as root in the
		// engine's VM on macOS), as upstream's deploy/docker does.
		env := []string{"--env", "COMPUTE_DRIVER=docker", "--env", "SANDBOX_LABEL=" + o.SandboxLabel, "--env", "GATEWAY_HOST=" + name}
		if PatchedSupervisor {
			env = append(env, "--env", "SUPERVISOR_IMAGE="+image)
		}
		args := append([]string{"container", "create", "--name", name, "--label", labelImage + "=" + image, "--label", labelEngine + "=" + o.engineLabel(),
			"--publish", fmt.Sprintf("127.0.0.1:%d:%d", o.Port, containerPort)}, env...)
		_, err := run.Run(ctx, nil, append(args,
			"--mount", "type=volume,source="+o.volume()+",target=/var/lib/openshell",
			"--mount", "type=bind,source="+o.DockerSocket+",target=/var/run/docker.sock", image)...)
		if err != nil {
			return err
		}
		_, err = run.Run(ctx, nil, "container", "start", name)
		return err
	}
	if _, err := run.Run(ctx, nil, "container", "create", "--name", name, "--label", labelImage+"="+image,
		"--network", o.Prefix, "--publish", fmt.Sprintf("127.0.0.1:%d:%d", o.Port, containerPort),
		"--env", "DRIVER_PORT="+strconv.Itoa(o.DriverPort), "--env", "GATEWAY_HOST="+name,
		"--mount", "type=volume,source="+o.volume()+",target=/var/lib/openshell", image); err != nil {
		return err
	}
	// The relay's client certificate goes in before the gateway starts.
	archive, err := driverTLSArchive(filepath.Join(o.Dir, "driver-tls"))
	if err != nil {
		return err
	}
	if _, err := run.Run(ctx, archive, "container", "cp", "-", name+":/etc/openshell"); err != nil {
		return err
	}
	_, err = run.Run(ctx, nil, "container", "start", name)
	return err
}

// SameImage reports whether two image references name the same image: the
// same reference, or the same digest under another tag (a release's
// version tag), which needs no new container.
func SameImage(a, b string) bool {
	if a == b {
		return true
	}
	_, da, okA := strings.Cut(a, "@sha256:")
	_, db, okB := strings.Cut(b, "@sha256:")
	return okA && okB && da != "" && da == db
}

// ContainerImage is the image the gateway container of prefix was made
// with ("" when there is none or the engine cannot tell).
func ContainerImage(ctx context.Context, run wslc.Runner, prefix string) string {
	var c struct {
		Config struct{ Labels map[string]string }
	}
	if err := wslc.Inspect(ctx, run, "container", Options{Prefix: prefix}.container(), &c); err != nil {
		return ""
	}
	return c.Config.Labels[labelImage]
}

func driverTLSArchive(dir string) ([]byte, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{Name: "driver-tls/", Typeflag: tar.TypeDir, Mode: 0o700}); err != nil {
		return nil, err
	}
	for _, name := range []string{"ca.pem", "relay.pem", "relay.key"} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		if err := tw.WriteHeader(&tar.Header{Name: "driver-tls/" + name, Mode: 0o600, Size: int64(len(b)), Typeflag: tar.TypeReg}); err != nil {
			return nil, err
		}
		if _, err := tw.Write(b); err != nil {
			return nil, err
		}
	}
	return buf.Bytes(), tw.Close()
}

// copyClientBundle copies the gateway CA and the client certificate out of
// the container once the entrypoint has created them.
func copyClientBundle(ctx context.Context, run wslc.Runner, o Options, g Gateway) error {
	if err := os.MkdirAll(filepath.Dir(g.CA), 0o700); err != nil {
		return err
	}
	files := map[string]string{"ca.crt": g.CA, "client/tls.crt": g.Cert, "client/tls.key": g.Key}
	deadline := time.Now().Add(readyTimeout)
	for src, dst := range files {
		for {
			_, err := run.Run(ctx, nil, "container", "cp", o.container()+":/var/lib/openshell/tls/"+src, dst)
			if err == nil {
				break
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("copy the gateway's %s: %w", src, err)
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Second):
			}
		}
	}
	return nil
}

// waitReady waits until the gateway completes a mutual TLS handshake. It
// logs the container's output written while it waits (earlier output
// would bury the login URL on the console), and gives up early, with the
// output's tail, once the container has stopped.
//
// The output is watched beside the attempts, from its tail only: a reused
// gateway's log grows for as long as the container lives (one full read
// took over two minutes, the whole wait), and a slow read must not hold
// back the next attempt.
func waitReady(ctx context.Context, run wslc.Runner, o Options, g Gateway) error {
	cert, err := tls.LoadX509KeyPair(g.Cert, g.Key)
	if err != nil {
		return err
	}
	caPEM, err := os.ReadFile(g.CA)
	if err != nil {
		return err
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(caPEM)
	cfg := &tls.Config{Certificates: []tls.Certificate{cert}, RootCAs: pool, ServerName: "127.0.0.1", NextProtos: []string{"h2"}}
	deadline := time.Now().Add(readyTimeout)
	var w *logWatch
	for {
		d := tls.Dialer{Config: cfg, NetDialer: &net.Dialer{Timeout: 2 * time.Second}}
		conn, err := d.DialContext(ctx, "tcp", g.Address)
		if err == nil {
			if w != nil {
				w.finish()
			}
			return conn.Close()
		}
		if w == nil {
			w = watchLogs(ctx, run, o.container())
			defer w.stop()
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the OpenShell gateway did not come up on %s: %w\n%s", g.Address, err, w.tail())
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-w.stopped:
			return fmt.Errorf("the OpenShell gateway container %s stopped:\n%s", o.container(), w.tail())
		case <-time.After(time.Second):
		}
	}
}

const (
	// logTail is how many of the container's last lines each read takes,
	// and logBytes caps their size.
	logTail  = 300
	logBytes = 64 << 10
	// logRead bounds one read (logLast the one after the gateway is up);
	// logEvery spaces the reads (and the checks that the container still
	// runs).
	logRead  = 10 * time.Second
	logLast  = 2 * time.Second
	logEvery = 3 * time.Second
)

// logWatch copies the gateway's new output to Iwato's log and reports, on
// stopped, that the container no longer runs.
type logWatch struct {
	run       wslc.Runner
	container string
	cancel    context.CancelFunc
	done      chan struct{}
	stopped   chan struct{}

	mu sync.Mutex
	// seen is the last read's tail; nil until a read has set the baseline.
	seen []string
}

func watchLogs(ctx context.Context, run wslc.Runner, container string) *logWatch {
	ctx, cancel := context.WithCancel(ctx)
	w := &logWatch{run: run, container: container, cancel: cancel, done: make(chan struct{}), stopped: make(chan struct{})}
	go func() {
		defer close(w.done)
		for {
			w.read(ctx, logRead)
			var c struct{ State struct{ Running bool } }
			if err := wslc.Inspect(ctx, run, "container", container, &c); err == nil && !c.State.Running {
				w.read(ctx, logRead) // what it wrote before it stopped
				close(w.stopped)
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(logEvery):
			}
		}
	}()
	return w
}

// read takes the output's tail and logs the lines after what the previous
// read saw. The first read is the baseline: earlier output is not logged.
func (w *logWatch) read(ctx context.Context, timeout time.Duration) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	out, err := w.run.Run(ctx, nil, "container", "logs", "--tail", strconv.Itoa(logTail), w.container)
	if err != nil && len(out) == 0 {
		return
	}
	lines := tailLines(out, logBytes)
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.seen != nil {
		for _, l := range lines[overlap(w.seen, lines):] {
			if l != "" {
				log.Printf("gateway: %s", l)
			}
		}
	}
	w.seen = lines
}

// tailLines splits the last limit bytes of out into lines, dropping a line
// the cut went through.
func tailLines(out []byte, limit int) []string {
	if len(out) > limit {
		out = out[len(out)-limit:]
		if i := bytes.IndexByte(out, '\n'); i >= 0 {
			out = out[i+1:]
		}
	}
	s := strings.TrimRight(string(out), "\n")
	if s == "" {
		return []string{}
	}
	return strings.Split(s, "\n")
}

// overlap returns how many of next's first lines are prev's last ones:
// the longest such run, so a tail that did not move logs nothing. None
// means everything in next is new (more was written than one tail holds,
// or the container was replaced).
func overlap(prev, next []string) int {
	for k := min(len(prev), len(next)); k > 0; k-- {
		if slices.Equal(prev[len(prev)-k:], next[:k]) {
			return k
		}
	}
	return 0
}

// tail returns the last 30 lines read.
func (w *logWatch) tail() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return strings.Join(w.seen[max(0, len(w.seen)-30):], "\n")
}

// stop ends the watch and waits for its reads to return.
func (w *logWatch) stop() {
	w.cancel()
	<-w.done
}

// finish stops the watch after logging what the gateway wrote last.
func (w *logWatch) finish() {
	w.stop()
	w.read(context.Background(), logLast)
}
