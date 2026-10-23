package client

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"

	"iwato/internal/termd"
)

// ErrNoDaemon is the error when the sandbox has no usable daemon (an image
// without iwato-entrypoint, or a sandbox made before the WSLc driver placed
// iwato-term): what the exec printed is not the daemon's.
var ErrNoDaemon = errors.New("the sandbox has no terminal daemon (iwato-entrypoint)")

// Held is a terminal that the daemon (iwato-entrypoint) keeps in the
// sandbox, reached through a TTY-less exec of its `attach`. Closing it
// leaves the terminal running; Kill ends it.
type Held struct {
	p       *Process
	r       *bufio.Reader
	Hello   termd.Hello
	wmu     sync.Mutex
	pending []byte
	closed  atomic.Bool
	cwdMu   sync.Mutex  // one cwd question at a time
	cwd     chan string // its answer
}

// termCommand runs bin with args, or exits quietly when the sandbox has no
// bin (the exec would fail as one that cannot start). bin may be several
// paths separated by ':', the first one there running (the daemon's path
// in the image, then where the WSLc driver placed it in older sandboxes).
func termCommand(bin string, args ...string) []string {
	return append([]string{"/bin/sh", "-c", `IFS=:; for b in $0; do if test -x "$b"; then exec "$b" "$@"; fi; done; exit 111`, bin}, args...)
}

// AttachHeld attaches the terminal name of the daemon with key, which
// starts it with cmd (and the exec's environment, env added) when it does
// not run and cmd is given. A size of 0×0 keeps the terminal's. ctx bounds
// the wait for the daemon's hello, not the attachment.
func AttachHeld(ctx context.Context, conn grpc.ClientConnInterface, workspace, sandbox, bin, key, name string, cols, rows int, cmd []string, env map[string]string) (*Held, error) {
	args := []string{"attach", "-spawn", "-key", key, "-cols", strconv.Itoa(cols), "-rows", strconv.Itoa(rows), name}
	if len(cmd) > 0 {
		args = append(append(args, "--"), cmd...)
	}
	p, err := Start(context.Background(), conn, workspace, sandbox, termCommand(bin, args...), Options{Env: env})
	if err != nil {
		return nil, err
	}
	h := &Held{p: p, r: bufio.NewReaderSize(p, 64<<10), cwd: make(chan string, 1)}
	kind, payload, err := first(ctx, p, h.r)
	if err == nil {
		switch kind {
		case termd.KindHello:
			if json.Unmarshal(payload, &h.Hello) == nil {
				return h, nil
			}
			err = ErrNoDaemon
		case termd.KindError:
			err = daemonError(payload)
		default:
			err = ErrNoDaemon
		}
	}
	p.Close()
	return nil, err
}

// first reads the exec's first message within ctx. What is not this
// protocol (a missing binary's shell error, nothing at all) is ErrNoDaemon;
// a failed exec is its error.
func first(ctx context.Context, p *Process, r *bufio.Reader) (byte, []byte, error) {
	type msg struct {
		kind    byte
		payload []byte
		err     error
	}
	got := make(chan msg, 1)
	go func() {
		k, b, err := termd.ReadMessage(r)
		got <- msg{k, b, err}
	}()
	select {
	case m := <-got:
		if m.err == nil {
			return m.kind, m.payload, nil
		}
		// A failed exec (the gateway, the connection) is not the daemon's
		// absence: it says nothing about the sandbox.
		select {
		case <-p.done:
			if p.err != nil {
				return 0, nil, p.err
			}
		case <-time.After(2 * time.Second):
		}
		return 0, nil, ErrNoDaemon
	case <-ctx.Done():
		p.Close()
		return 0, nil, fmt.Errorf("no answer from the terminal daemon: %w", ctx.Err())
	}
}

// daemonError is an error message's error: a *termd.Error, or ErrNoDaemon
// when the CLI could not reach (or start) the daemon.
func daemonError(payload []byte) error {
	var e termd.Error
	if json.Unmarshal(payload, &e) != nil {
		return ErrNoDaemon
	}
	if e.Code == termd.ErrUnavailable {
		return fmt.Errorf("%w: %s", ErrNoDaemon, e.Message)
	}
	return &e
}

// Read returns the terminal's output: io.EOF once it ended, ErrRelayLost
// when the exec did first (the daemon or the connection to the sandbox).
func (h *Held) Read(b []byte) (int, error) {
	for len(h.pending) == 0 {
		kind, payload, err := termd.ReadMessage(h.r)
		if err != nil {
			if h.closed.Load() {
				return 0, io.EOF
			}
			return 0, ErrRelayLost
		}
		switch kind {
		case termd.KindOutput:
			h.pending = payload
		case termd.KindExit:
			return 0, io.EOF
		case termd.KindError:
			return 0, daemonError(payload)
		case termd.KindCwd:
			select {
			case h.cwd <- string(payload):
			default:
			}
		}
	}
	n := copy(b, h.pending)
	h.pending = h.pending[n:]
	return n, nil
}

func (h *Held) send(kind byte, p []byte) error {
	h.wmu.Lock()
	defer h.wmu.Unlock()
	return termd.WriteMessage(h.p, kind, p)
}

func (h *Held) Write(b []byte) (int, error) {
	for off := 0; off < len(b); off += 64 << 10 {
		if err := h.send(termd.KindInput, b[off:min(len(b), off+64<<10)]); err != nil {
			return off, err
		}
	}
	return len(b), nil
}

func (h *Held) Resize(cols, rows int) error {
	return h.send(termd.KindResize, termd.ResizePayload(cols, rows))
}

// Cwd asks the daemon for the terminal's working directory ("" when it
// does not know or cannot tell). The answer comes through Read.
func (h *Held) Cwd(ctx context.Context) string {
	if !termd.Has(h.Hello.Version, h.Hello.Capabilities, termd.CapCwd) {
		return ""
	}
	h.cwdMu.Lock()
	defer h.cwdMu.Unlock()
	select {
	case <-h.cwd: // a late answer to an earlier question
	default:
	}
	if h.send(termd.KindCwd, nil) != nil {
		return ""
	}
	select {
	case dir := <-h.cwd:
		return dir
	case <-ctx.Done():
		return ""
	}
}

// Kill ends the terminal; its exit ends the output.
func (h *Held) Kill() error { return h.send(termd.KindEnd, nil) }

// Close detaches; the terminal keeps running.
func (h *Held) Close() error {
	h.closed.Store(true)
	_ = h.p.CloseWrite()
	return h.p.Close()
}

// request runs a list or kill of the daemon with key and returns its answer.
func request(ctx context.Context, conn grpc.ClientConnInterface, workspace, sandbox string, args []string) (byte, []byte, error) {
	p, err := Start(ctx, conn, workspace, sandbox, args, Options{})
	if err != nil {
		return 0, nil, err
	}
	defer p.Close()
	kind, payload, err := first(ctx, p, bufio.NewReader(p))
	if err == nil && kind == termd.KindError {
		err = daemonError(payload)
	}
	return kind, payload, err
}

// ListHeld lists the terminals of the daemon with key, starting it when it
// does not run (in a sandbox whose main process it is not).
func ListHeld(ctx context.Context, conn grpc.ClientConnInterface, workspace, sandbox, bin, key string) (*termd.List, error) {
	kind, payload, err := request(ctx, conn, workspace, sandbox, termCommand(bin, "list", "-spawn", "-key", key))
	if err != nil {
		return nil, err
	}
	var l termd.List
	if kind != termd.KindListed || json.Unmarshal(payload, &l) != nil {
		return nil, ErrNoDaemon
	}
	return &l, nil
}

// KillHeld ends the terminal name of the daemon with key and returns once
// it ended (or it was not running).
func KillHeld(ctx context.Context, conn grpc.ClientConnInterface, workspace, sandbox, bin, key, name string) error {
	kind, _, err := request(ctx, conn, workspace, sandbox, termCommand(bin, "kill", "-key", key, name))
	var e *termd.Error
	if errors.As(err, &e) && e.Code == termd.ErrAbsent {
		return nil
	}
	if err == nil && kind != termd.KindKilled {
		err = ErrNoDaemon
	}
	return err
}
