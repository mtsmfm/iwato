package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	pb "github.com/NVIDIA/OpenShell/sdk/go/proto/openshellv1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

// fakeGateway relays ForwardTcp as v0.1.2's gateway does (v0.1.4 too when
// a token is sent): a token is required and counted (3 at a time,
// perSandbox in all when set), the headers go out once the slots are
// taken, a slot is given back lag after the stream ends, port 7 echoes,
// others refuse.
type fakeGateway struct {
	pb.UnimplementedOpenShellServer
	mu         sync.Mutex
	made       int
	revoked    []string
	inUse      map[string]int
	max        int
	lag        time.Duration
	perSandbox int
}

func (g *fakeGateway) CreateSshSession(_ context.Context, r *pb.CreateSshSessionRequest) (*pb.CreateSshSessionResponse, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if r.GetSandbox() == "stopped" {
		return nil, status.Error(codes.FailedPrecondition, "sandbox is not ready")
	}
	g.made++
	return &pb.CreateSshSessionResponse{Token: fmt.Sprintf("tok%d", g.made)}, nil
}

func (g *fakeGateway) RevokeSshSession(_ context.Context, r *pb.RevokeSshSessionRequest) (*pb.RevokeSshSessionResponse, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.revoked = append(g.revoked, r.GetToken())
	return &pb.RevokeSshSessionResponse{}, nil
}

func (g *fakeGateway) ForwardTcp(s grpc.BidiStreamingServer[pb.TcpForwardFrame, pb.TcpForwardFrame]) error {
	first, err := s.Recv()
	if err != nil {
		return err
	}
	init := first.GetInit()
	tok := init.GetAuthorizationToken()
	if tok == "" {
		return status.Error(codes.Unauthenticated, "authorization_token is required for ForwardTcp")
	}
	g.mu.Lock()
	if g.inUse[tok] >= 3 {
		g.mu.Unlock()
		return status.Error(codes.ResourceExhausted, "SSH session connection limit reached")
	}
	total := 0
	for _, n := range g.inUse {
		total += n
	}
	if g.perSandbox > 0 && total >= g.perSandbox {
		g.mu.Unlock()
		return status.Error(codes.ResourceExhausted, "sandbox SSH connection limit reached")
	}
	g.inUse[tok]++
	g.max = max(g.max, total+1)
	lag := g.lag
	g.mu.Unlock()
	defer time.AfterFunc(lag, func() { g.mu.Lock(); g.inUse[tok]--; g.mu.Unlock() })
	if err := s.SendHeader(metadata.MD{}); err != nil {
		return err
	}
	if init.GetTcp().GetPort() != 7 || init.GetTcp().GetHost() != "127.0.0.1" {
		return status.Error(codes.Unavailable, "port-forward connect to "+init.GetTcp().GetHost()+": Connection refused (os error 111)")
	}
	for {
		f, err := s.Recv()
		if err == io.EOF {
			return nil // half-closed: done after echoing
		}
		if err != nil {
			return err
		}
		if err := s.Send(&pb.TcpForwardFrame{Payload: &pb.TcpForwardFrame_Data{Data: f.GetData()}}); err != nil {
			return err
		}
	}
}

func newForwarder(t *testing.T) (*Forwarder, *fakeGateway) {
	t.Helper()
	g := &fakeGateway{inUse: map[string]int{}}
	ln := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	pb.RegisterOpenShellServer(srv, g)
	go srv.Serve(ln)
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient("passthrough:///bufnet", grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return ln.DialContext(ctx) }), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return &Forwarder{Conn: conn, Workspace: "default", Idle: 50 * time.Millisecond, Linger: time.Millisecond}, g
}

func TestForwarderSharesAndRevokesTokens(t *testing.T) {
	f, g := newForwarder(t)
	ctx := context.Background()
	var conns []net.Conn
	for range 7 {
		c, err := f.Dial(ctx, "dev", "127.0.0.1", 7)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.Write([]byte("ping")); err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 4)
		if _, err := io.ReadFull(c, buf); err != nil || string(buf) != "ping" {
			t.Fatalf("%q %v", buf, err)
		}
		conns = append(conns, c)
	}
	if g.made != 3 || f.Tokens("dev") != 3 {
		t.Fatalf("7 connections used %d tokens", g.made)
	}
	for _, c := range conns {
		c.Close()
	}
	time.Sleep(200 * time.Millisecond)
	g.mu.Lock()
	revoked := len(g.revoked)
	g.mu.Unlock()
	if revoked != 3 || f.Tokens("dev") != 0 {
		t.Fatalf("revoked %d, kept %d", revoked, f.Tokens("dev"))
	}
}

func TestForwarderErrors(t *testing.T) {
	f, _ := newForwarder(t)
	ctx := context.Background()
	c, err := f.Dial(ctx, "dev", "127.0.0.1", 8080)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Read(make([]byte, 1)); !errors.Is(err, ErrConnRefused) {
		t.Fatalf("refused: %v", err)
	}
	c.Close()
	if _, err := f.Dial(ctx, "stopped", "127.0.0.1", 7); !errors.Is(err, ErrNotReady) {
		t.Fatalf("stopped: %v", err)
	}
	// Half-close and read deadlines.
	c, _ = f.Dial(ctx, "dev", "127.0.0.1", 7)
	defer c.Close()
	c.SetReadDeadline(time.Now().Add(30 * time.Millisecond))
	if _, err := c.Read(make([]byte, 1)); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("deadline: %v", err)
	}
	c.SetReadDeadline(time.Time{})
	c.Write([]byte("bye"))
	c.(interface{ CloseWrite() error }).CloseWrite()
	rest, err := io.ReadAll(c)
	if err != nil || string(rest) != "bye" {
		t.Fatalf("%q %v", rest, err)
	}
}

// A token whose closed connection the gateway still counts is refused for
// its limit: the dial goes again with a new token. The sandbox's limit is
// ErrSandboxBusy, not tried again.
func TestForwarderRetriesFullToken(t *testing.T) {
	f, g := newForwarder(t)
	g.lag = 300 * time.Millisecond
	ctx := context.Background()
	echo := func(c net.Conn) {
		t.Helper()
		c.Write([]byte("ping"))
		buf := make([]byte, 4)
		if _, err := io.ReadFull(c, buf); err != nil || string(buf) != "ping" {
			t.Fatalf("%q %v", buf, err)
		}
	}
	var conns []net.Conn
	for range 3 {
		c, err := f.Dial(ctx, "dev", "127.0.0.1", 7)
		if err != nil {
			t.Fatal(err)
		}
		echo(c)
		conns = append(conns, c)
	}
	conns[0].Close()
	time.Sleep(20 * time.Millisecond) // past Linger, within the gateway's lag
	c, err := f.Dial(ctx, "dev", "127.0.0.1", 7)
	if err != nil {
		t.Fatal(err)
	}
	echo(c)
	conns[0] = c
	if g.made != 2 {
		t.Fatalf("made %d tokens", g.made)
	}
	g.mu.Lock()
	g.perSandbox = 3
	g.mu.Unlock()
	if _, err := f.Dial(ctx, "dev", "127.0.0.1", 7); !errors.Is(err, ErrSandboxBusy) {
		t.Fatalf("sandbox limit: %v", err)
	}
	for _, c := range conns {
		c.Close()
	}
}
