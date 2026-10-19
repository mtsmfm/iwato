// Package client reaches an OpenShell gateway over mutual TLS, for what
// OpenShell's Go SDK does not cover.
package client

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	dm "github.com/NVIDIA/OpenShell/sdk/go/proto/datamodelv1"
	pb "github.com/NVIDIA/OpenShell/sdk/go/proto/openshellv1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/resolver"
)

// MaxRecvMsgSize bounds a gateway response. gRPC's default of 4 MiB is too
// small for responses that grow with use (every draft chunk a sandbox ever
// had, 2000 log lines); the gateway (tonic) sends any size.
const MaxRecvMsgSize = 64 << 20

// sdkScheme is a resolver for the SDK's own connection, which takes no dial
// options: it resolves host:port to itself, as passthrough does, and adds a
// service config that raises the receive limit to MaxRecvMsgSize.
const sdkScheme = "iwato-gateway"

// SDKAddress is address for v1.Config.Address, so that the SDK's
// connection accepts responses up to MaxRecvMsgSize.
func SDKAddress(address string) string { return sdkScheme + ":///" + address }

func init() { resolver.Register(sdkResolver{}) }

type sdkResolver struct{}

func (sdkResolver) Scheme() string { return sdkScheme }

func (sdkResolver) Build(t resolver.Target, cc resolver.ClientConn, _ resolver.BuildOptions) (resolver.Resolver, error) {
	sc := cc.ParseServiceConfig(fmt.Sprintf(`{"methodConfig":[{"name":[{}],"maxResponseMessageBytes":%d}]}`, MaxRecvMsgSize))
	if sc.Err != nil {
		return nil, sc.Err
	}
	if err := cc.UpdateState(resolver.State{Addresses: []resolver.Address{{Addr: t.Endpoint()}}, ServiceConfig: sc}); err != nil {
		return nil, err
	}
	return sdkResolver{}, nil
}

func (sdkResolver) ResolveNow(resolver.ResolveNowOptions) {}

func (sdkResolver) Close() {}

// Dial connects to the gateway at address (host:port) with the client
// bundle: gateway CA, client certificate and key.
func Dial(address, caFile, certFile, keyFile string) (*grpc.ClientConn, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, err
	}
	caPEM, err := os.ReadFile(caFile)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("no certificates in the gateway CA file")
	}
	creds := credentials.NewTLS(&tls.Config{Certificates: []tls.Certificate{cert}, RootCAs: pool, MinVersion: tls.VersionTLS12})
	return grpc.NewClient(address, grpc.WithTransportCredentials(creds), grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(MaxRecvMsgSize)))
}

// Process is a command running in a sandbox without a terminal: its stdin
// is streamed as written and stdout comes back as it is produced.
type Process struct {
	stream grpc.BidiStreamingClient[pb.ExecSandboxInput, pb.ExecSandboxEvent]
	cancel context.CancelFunc
	out    *io.PipeReader
	sendMu sync.Mutex
	done   chan struct{}
	exit   int
	err    error
}

// Options are the command's working directory and environment.
type Options struct {
	Workdir string
	Env     map[string]string
}

// Start runs command in the sandbox. Stderr is merged into the output.
func Start(ctx context.Context, conn grpc.ClientConnInterface, workspace, sandbox string, command []string, o Options) (*Process, error) {
	ctx, cancel := context.WithCancel(ctx)
	stream, err := pb.NewOpenShellClient(conn).ExecSandboxInteractive(ctx)
	if err != nil {
		cancel()
		return nil, err
	}
	err = stream.Send(&pb.ExecSandboxInput{Payload: &pb.ExecSandboxInput_Start{Start: &pb.ExecSandboxRequest{
		WorkspaceScope: &dm.WorkspaceSelector{Selection: &dm.WorkspaceSelector_Workspace{Workspace: workspace}},
		Sandbox:        sandbox,
		Command:        command,
		Workdir:        o.Workdir,
		Environment:    o.Env,
		NoLoginShell:   true,
	}}})
	if err != nil {
		cancel()
		return nil, err
	}
	r, w := io.Pipe()
	p := &Process{stream: stream, cancel: cancel, out: r, done: make(chan struct{}), exit: -1}
	go p.receive(w)
	return p, nil
}

func (p *Process) receive(w *io.PipeWriter) {
	defer close(p.done)
	for {
		ev, err := p.stream.Recv()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				p.err = err
			}
			w.CloseWithError(err)
			return
		}
		switch e := ev.Payload.(type) {
		case *pb.ExecSandboxEvent_Stdout:
			if _, err := w.Write(e.Stdout.GetData()); err != nil {
				return
			}
		case *pb.ExecSandboxEvent_Stderr:
			if _, err := w.Write(e.Stderr.GetData()); err != nil {
				return
			}
		case *pb.ExecSandboxEvent_Exit:
			p.exit = int(e.Exit.GetExitCode())
		}
	}
}

func (p *Process) Read(b []byte) (int, error) { return p.out.Read(b) }

func (p *Process) Write(b []byte) (int, error) {
	p.sendMu.Lock()
	defer p.sendMu.Unlock()
	if err := p.stream.Send(&pb.ExecSandboxInput{Payload: &pb.ExecSandboxInput_Stdin{Stdin: append([]byte(nil), b...)}}); err != nil {
		return 0, err
	}
	return len(b), nil
}

// CloseWrite ends the command's stdin.
func (p *Process) CloseWrite() error {
	p.sendMu.Lock()
	defer p.sendMu.Unlock()
	return p.stream.CloseSend()
}

// Close abandons the command's streams.
func (p *Process) Close() error {
	p.cancel()
	p.out.Close()
	return nil
}

// Wait returns the command's exit code once its output has ended.
func (p *Process) Wait() (int, error) {
	<-p.done
	if p.exit < 0 && p.err == nil {
		return p.exit, fmt.Errorf("the command ended without an exit status")
	}
	return p.exit, p.err
}
