package wslc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"google.golang.org/grpc"

	computev1 "iwato/internal/openshell/computev1"
)

// Serve runs the driver on ln until ctx ends, driving WSLc through the
// wslc executable.
func Serve(ctx context.Context, cfg Config, wslcExe string, ln net.Listener) error {
	d := newDriver(cfg, execRunner{exe: wslcExe})
	if err := d.cfg.check(); err != nil {
		return err
	}
	s := grpc.NewServer()
	computev1.RegisterComputeDriverServer(s, d)
	go func() { <-ctx.Done(); s.GracefulStop() }()
	go func() { d.reconcile(ctx); d.pollLoop(ctx) }()
	log.Printf("openshell-driver-wslc %s serving on %s", driverVersion, ln.Addr())
	return s.Serve(ln)
}

// Listen opens unix:///PATH, tcp://HOST:PORT or tls://HOST:PORT; tls takes
// the listener's certificate and key and the CA of accepted clients.
func Listen(addr, certFile, keyFile, clientCAFile string) (net.Listener, error) {
	switch {
	case strings.HasPrefix(addr, "tls://"):
		return tlsListen(strings.TrimPrefix(addr, "tls://"), certFile, keyFile, clientCAFile)
	case strings.HasPrefix(addr, "unix://"):
		path := strings.TrimPrefix(addr, "unix://")
		_ = os.Remove(path)
		return net.Listen("unix", path)
	case strings.HasPrefix(addr, "tcp://"):
		return net.Listen("tcp", strings.TrimPrefix(addr, "tcp://"))
	}
	return nil, fmt.Errorf("listen address must be unix:///PATH, tcp://HOST:PORT or tls://HOST:PORT: %q", addr)
}

// WriteCerts creates the listener's certificates and the relay's client
// certificate in dir (ca.pem, server.pem/key, relay.pem/key).
func WriteCerts(dir string) error { return writeDriverCerts(dir) }

// DefaultStateDir is where each sandbox's boundary files are kept.
func DefaultStateDir() string {
	if dir, err := os.UserCacheDir(); err == nil {
		return filepath.Join(dir, "iwato", "openshell-wslc")
	}
	return filepath.Join(os.TempDir(), "iwato-openshell-wslc")
}

// CreatedImages are the IDs of the supervisor and sandbox runtime images
// the sandbox id was created with (the driver keeps both until it is
// recreated), "" for one not recorded.
func CreatedImages(stateDir, namespace, id string) (supervisor, sandbox string) {
	dir := (&driver{cfg: Config{StateDir: stateDir, Namespace: namespace}}).stateDir(id)
	read := func(name string) string {
		b, _ := os.ReadFile(filepath.Join(dir, name))
		return strings.TrimSpace(string(b))
	}
	return read("supervisor-image"), read("sandbox-image")
}

// ImageID is the ID of the local image ref, "" when there is none.
func ImageID(ctx context.Context, r Runner, ref string) (string, error) {
	img, err := (wslc{r}).image(ctx, ref)
	if errors.Is(err, errNotFound) {
		return "", nil
	}
	return img.ID, err
}

// Runner runs one wslc command, optionally feeding stdin.
type Runner = runner

// NewRunner runs the wslc executable, logging slow calls and keeping
// --env values out of errors.
func NewRunner(exe string) Runner { return execRunner{exe: exe} }

// Streamer runs a long wslc command (an image build) writing its output
// as it comes.
type Streamer interface {
	Stream(ctx context.Context, w io.Writer, args ...string) error
}

func (r execRunner) Stream(ctx context.Context, w io.Writer, args ...string) error {
	cmd := exec.CommandContext(ctx, r.exe, args...)
	cmd.Stdout, cmd.Stderr = w, w
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("wslc %s: %w", strings.Join(redact(args), " "), err)
	}
	return nil
}

// ErrNotFound is what Inspect reports for a missing object.
var ErrNotFound = errNotFound

// Inspect decodes `wslc <kind> inspect` of one object into v.
func Inspect(ctx context.Context, r Runner, kind, name string, v any) error {
	return wslc{r}.inspect(ctx, kind, name, v)
}
