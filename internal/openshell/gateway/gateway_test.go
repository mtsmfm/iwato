package gateway

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"iwato/internal/openshell/wslc"
)

// fakeRunner records commands and answers inspects from a table.
type fakeRunner struct {
	inspect map[string]string
	calls   []string
}

func (f *fakeRunner) Run(_ context.Context, _ []byte, args ...string) ([]byte, error) {
	cmd := strings.Join(args, " ")
	f.calls = append(f.calls, cmd)
	if len(args) > 1 && args[1] == "inspect" {
		if out, ok := f.inspect[args[0]+" "+args[len(args)-1]]; ok {
			return []byte(out), nil
		}
		return []byte("[]"), errors.New("exit status 1")
	}
	return nil, nil
}

func TestEnsureContainerCreatesWithTheRelayCertificates(t *testing.T) {
	dir := t.TempDir()
	if _, _, _, err := DriverTLS(dir); err != nil {
		t.Fatal(err)
	}
	o := Options{Prefix: "iwato-default", Dir: dir, Port: 17670, DriverPort: 17671}
	tag, err := imageTag(o)
	if err != nil || !strings.HasPrefix(tag, "iwato-default-gateway:0.1.4-") {
		t.Fatal(tag, err)
	}
	f := &fakeRunner{}
	if err := ensureContainer(context.Background(), f, o, tag); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"container inspect --format json iwato-default-gateway",
		"container create --name iwato-default-gateway --label iwato.gateway.image=" + tag +
			" --network iwato-default --publish 127.0.0.1:17670:17670 --env DRIVER_PORT=17671" +
			" --env GATEWAY_HOST=iwato-default-gateway --mount type=volume,source=iwato-default-state,target=/var/lib/openshell " + tag,
		"container cp - iwato-default-gateway:/etc/openshell",
		"container start iwato-default-gateway",
	}
	if strings.Join(f.calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls:\n%s", strings.Join(f.calls, "\n"))
	}
}

func TestEnsureContainerKeepsARunningCurrentGateway(t *testing.T) {
	o := Options{Prefix: "p", Dir: t.TempDir()}
	f := &fakeRunner{inspect: map[string]string{"container p-gateway": `[{"State":{"Running":true},"Config":{"Labels":{"iwato.gateway.image":"img:1"}}}]`}}
	if err := ensureContainer(context.Background(), f, o, "img:1"); err != nil || len(f.calls) != 1 {
		t.Fatal(f.calls, err)
	}
	// A new image replaces it.
	f.calls = nil
	if err := ensureContainer(context.Background(), f, o, "img:2"); err == nil || f.calls[1] != "container remove --force p-gateway" {
		t.Fatal(f.calls, err) // fails at the missing driver-tls files after create
	}
}

// On a Docker engine the gateway runs upstream's Docker driver on the
// engine's socket: no relay certificates, no network of its own, and a
// gateway made for another socket or label is made again.
func TestEnsureContainerOnDocker(t *testing.T) {
	o := Options{Prefix: "p", Dir: t.TempDir(), Port: 17670, DockerSocket: "/var/run/docker.sock", SandboxLabel: "personal"}
	f := &fakeRunner{inspect: map[string]string{"container p-gateway": `[{"State":{"Running":true},"Config":{"Labels":{"iwato.gateway.image":"img:1"}}}]`}}
	if err := ensureContainer(context.Background(), f, o, "img:1"); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"container inspect --format json p-gateway",
		"container remove --force p-gateway",
		"container create --name p-gateway --label iwato.gateway.image=img:1 --label iwato.gateway.engine=docker /var/run/docker.sock personal" +
			" --publish 127.0.0.1:17670:17670 --env COMPUTE_DRIVER=docker --env SANDBOX_LABEL=personal --env GATEWAY_HOST=p-gateway" +
			" --mount type=volume,source=p-state,target=/var/lib/openshell --mount type=bind,source=/var/run/docker.sock,target=/var/run/docker.sock img:1",
		"container start p-gateway",
	}
	if strings.Join(f.calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls:\n%s", strings.Join(f.calls, "\n"))
	}
}

func TestEnsureImagePullsAPublishedImage(t *testing.T) {
	o := Options{Prefix: "iwato-default", Dir: t.TempDir(), Image: "ghcr.io/mtsmfm/iwato-gateway:0.1.0"}
	f := &fakeRunner{}
	tag, err := ensureImage(context.Background(), f, o)
	if err != nil || tag != o.Image || len(f.calls) != 2 || f.calls[1] != "image pull "+o.Image {
		t.Fatal(tag, err, f.calls)
	}
	// Present: nothing to pull or build.
	f = &fakeRunner{inspect: map[string]string{"image " + o.Image: `[{}]`}}
	if tag, err = ensureImage(context.Background(), f, o); err != nil || tag != o.Image || len(f.calls) != 1 {
		t.Fatal(tag, err, f.calls)
	}
}

// The providers' key-encryption key lives in the state volume, which
// outlives the container a new gateway image replaces; the entrypoint
// makes its directory (the gateway runs as root).
func TestCredentialKeyIsOnTheStateVolume(t *testing.T) {
	conf, _ := buildContext.ReadFile("gateway.toml")
	if !strings.Contains(string(conf), "[openshell.gateway.credential_storage]\nkey_encryption_key_path = \"/var/lib/openshell/credentials/key-encryption-key.bin\"") {
		t.Fatal("gateway.toml does not put the key-encryption key on the state volume")
	}
	entry, _ := buildContext.ReadFile("entrypoint.sh")
	if !strings.Contains(string(entry), "install -d -m 700 /var/lib/openshell/credentials") {
		t.Fatal("the entrypoint does not make the key's directory")
	}
	dockerfile, _ := buildContext.ReadFile("Dockerfile")
	if _, gatewayStage, _ := strings.Cut(string(dockerfile), "AS gateway"); strings.Contains(gatewayStage, "\nUSER ") {
		t.Fatal("the gateway stage changed its user: check the key directory's owner")
	}
}

// logsRunner answers the waits' wslc calls: logs come from logs (nil: a
// read that takes until it is given up) and inspect says running.
type logsRunner struct {
	mu      sync.Mutex
	running bool
	logs    func() string
	reads   []string
}

func (f *logsRunner) Run(ctx context.Context, _ []byte, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch strings.Join(args[:2], " ") {
	case "container inspect":
		if f.running {
			return []byte(`[{"State":{"Running":true}}]`), nil
		}
		return []byte(`[{"State":{"Running":false}}]`), nil
	case "container logs":
		f.reads = append(f.reads, strings.Join(args, " "))
		if f.logs == nil {
			f.mu.Unlock()
			<-ctx.Done()
			f.mu.Lock()
			return nil, ctx.Err()
		}
		return []byte(f.logs()), nil
	}
	return nil, nil
}

// waitGateway serves mutual TLS on a loopback port after delay (never when
// negative) and returns the Gateway that reaches it.
func waitGateway(t *testing.T, delay time.Duration) Gateway {
	dir := t.TempDir()
	if err := wslc.WriteCerts(dir); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	if delay >= 0 {
		cert, err := tls.LoadX509KeyPair(filepath.Join(dir, "server.pem"), filepath.Join(dir, "server.key"))
		if err != nil {
			t.Fatal(err)
		}
		ca, _ := os.ReadFile(filepath.Join(dir, "ca.pem"))
		pool := x509.NewCertPool()
		pool.AppendCertsFromPEM(ca)
		cfg := &tls.Config{Certificates: []tls.Certificate{cert}, ClientCAs: pool, ClientAuth: tls.RequireAndVerifyClientCert}
		go func() {
			time.Sleep(delay)
			ln, err := tls.Listen("tcp", addr, cfg)
			if err != nil {
				return
			}
			t.Cleanup(func() { ln.Close() })
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				go func() { _ = c.(*tls.Conn).Handshake(); c.Close() }()
			}
		}()
	}
	return Gateway{Address: addr, CA: filepath.Join(dir, "ca.pem"), Cert: filepath.Join(dir, "relay.pem"), Key: filepath.Join(dir, "relay.key")}
}

// A reused gateway's log can take minutes to read: the wait reads only its
// tail, beside the attempts, so the gateway is still found as it comes up.
func TestWaitReadyIsNotHeldBackByTheLog(t *testing.T) {
	f := &logsRunner{running: true}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	began := time.Now()
	if err := waitReady(ctx, f, Options{Prefix: "p"}, waitGateway(t, 1500*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(began); d > 8*time.Second {
		t.Fatalf("ready after %s", d)
	}
	for _, r := range f.reads {
		if r != "container logs --tail 300 p-gateway" {
			t.Fatal(r)
		}
	}

	// A stopped container ends the wait with the output's tail.
	f = &logsRunner{logs: func() string { return "starting\nfailed: bad config\n" }}
	err := waitReady(context.Background(), f, Options{Prefix: "p"}, waitGateway(t, -1))
	if err == nil || !strings.Contains(err.Error(), "p-gateway stopped:\nstarting\nfailed: bad config") {
		t.Fatal(err)
	}
}

// The release the driver follows is the one the Dockerfile builds and pins
// (a SHA-256 per tarball and architecture), its base image is pinned by
// digest, and the Docker driver uses the driver's pinned runtime images.
func TestDockerfilePinsOpenShell(t *testing.T) {
	b, _ := buildContext.ReadFile("Dockerfile")
	text := string(b)
	tag := "v" + wslc.OpenShellVersion
	if !strings.Contains(text, "ARG OPENSHELL_TAG="+tag+"\n") || !strings.Contains(text, "      "+tag+") printf") {
		t.Fatalf("the Dockerfile does not build and pin %s", tag)
	}
	pins := text[strings.Index(text, tag+") printf"):strings.Index(text, "> pinned.sha256 ;;")]
	pinLine := regexp.MustCompile(`'[0-9a-f]{64}  (openshell[a-z-]*-(x86_64|aarch64)-unknown-linux-(gnu|musl)\.tar\.gz)'`)
	if got := len(pinLine.FindAllString(pins, -1)); got != 8 {
		t.Errorf("%d tarballs pinned for %s, want 4 per architecture", got, tag)
	}
	for _, from := range regexp.MustCompile(`(?m)^FROM (\S+)`).FindAllStringSubmatch(text, -1) {
		if from[1] != "base" && from[1] != "scratch" && !regexp.MustCompile(`^ubuntu:24\.04@sha256:[0-9a-f]{64}$`).MatchString(from[1]) {
			t.Errorf("FROM %s is not pinned by digest", from[1])
		}
	}
	d, _ := buildContext.ReadFile("gateway-docker.toml")
	for _, want := range []string{`supervisor_image      = "` + wslc.DefaultSupervisorImage + `"`, `sandbox_runtime_image = "` + wslc.DefaultSandboxImage + `"`} {
		if !strings.Contains(string(d), want) {
			t.Errorf("gateway-docker.toml lacks %s", want)
		}
	}
}
