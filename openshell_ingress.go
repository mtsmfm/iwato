package main

import (
	"context"
	"io"
	"net"
	"strings"

	"iwato/internal/ingress"
	"iwato/internal/openshell/client"
)

// forwarder is the gateway's ForwardTcp for the ingress (made once the
// gateway is ready).
func (o *OpenShell) forwarder(ctx context.Context) (*client.Forwarder, error) {
	_, conn, err := o.clients(ctx)
	if err != nil {
		return nil, err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.fwd == nil {
		o.fwd = &client.Forwarder{Conn: conn, Workspace: openshellWorkspace}
	}
	return o.fwd, nil
}

// DialPort connects to host:port on the sandbox's loopback through the
// gateway's relay.
func (o *OpenShell) DialPort(ctx context.Context, sandbox, host string, port uint16) (net.Conn, error) {
	f, err := o.forwarder(ctx)
	if err != nil {
		return nil, err
	}
	return f.Dial(ctx, sandbox, host, port)
}

// ForgetPorts revokes the sandbox's session tokens of the ingress.
func (o *OpenShell) ForgetPorts(sandbox string) {
	o.mu.Lock()
	f := o.fwd
	o.mu.Unlock()
	if f != nil {
		f.Forget(sandbox)
	}
}

// ListeningPorts reads the sandbox's /proc/net/tcp{,6} with one exec (sh
// and cat, python3 for the sweep; ss is not everywhere): see
// ingress.ListenScript.
func (o *OpenShell) ListeningPorts(ctx context.Context, w Sandbox) ([]ingress.ListenPort, error) {
	_, conn, err := o.clients(ctx)
	if err != nil {
		return nil, err
	}
	p, err := client.Start(ctx, conn, openshellWorkspace, w.Name, []string{"sh", "-c", ingress.ListenScript}, client.Options{})
	if err != nil {
		return nil, err
	}
	defer p.Close()
	_ = p.CloseWrite()
	out, err := io.ReadAll(io.LimitReader(p, 4<<20))
	if err != nil && !strings.Contains(err.Error(), "EOF") && len(out) == 0 {
		return nil, err
	}
	_, _ = p.Wait()
	return ingress.ParseProcNetTCP(string(out)), nil
}
