//go:build linux

package termd

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRingKeepsTheLastBytes(t *testing.T) {
	r := NewRing(8)
	r.Write([]byte("abc"))
	if b, off := r.Bytes(); string(b) != "abc" || off != 0 {
		t.Fatalf("%q %d", b, off)
	}
	r.Write([]byte("defgh"))
	r.Write([]byte("ij"))
	if b, off := r.Bytes(); string(b) != "cdefghij" || off != 2 {
		t.Fatalf("wrapped: %q %d", b, off)
	}
	r.Write([]byte("0123456789xy"))
	if b, off := r.Bytes(); string(b) != "456789xy" || off != 14 || r.Total() != 22 {
		t.Fatalf("larger than the ring: %q %d %d", b, off, r.Total())
	}
}

// TestDaemon runs the real binary: a terminal outlives its attachment,
// comes back with its recent output, is listed, killed, and one daemon
// serves a key.
func TestDaemon(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "iwato-entrypoint")
	if out, err := exec.Command("go", "build", "-o", bin, "iwato/cmd/iwato-entrypoint").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	key := fmt.Sprintf("test-%d", os.Getpid())
	serve := exec.Command(bin, "serve", "-key", key)
	if err := serve.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { serve.Process.Kill(); serve.Wait(); os.Remove(filepath.Join(Dir(), key+".lock")) })
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if c, err := Dial(key); err == nil {
			c.Close()
			break
		} else if time.Now().After(deadline) {
			t.Fatal("the daemon does not listen:", err)
		}
	}
	// Another one leaves at once (attach -spawn starts such).
	if out, err := exec.Command(bin, "serve", "-key", key, "-detached").CombinedOutput(); err != nil {
		t.Fatalf("a second daemon: %v %s", err, out)
	}

	a := attach(t, bin, key, "main", "-cols", "80", "-rows", "24", "main", "--", "/bin/sh")
	if !a.hello.Created || a.hello.Cols != 80 {
		t.Fatalf("hello: %+v", a.hello)
	}
	a.send(KindInput, []byte("echo MARK-$((40+2))\n"))
	a.waitFor(t, "MARK-42")
	a.close()

	// The shell is still there, with what it wrote.
	b := attach(t, bin, key, "main", "main")
	if b.hello.Created || b.hello.Cols != 80 || b.hello.Rows != 24 {
		t.Fatalf("hello of a reattach: %+v", b.hello)
	}
	b.waitFor(t, "MARK-42")
	var l List
	if k, p := request(t, bin, "list", "-key", key); k != KindListed || json.Unmarshal(p, &l) != nil || len(l.Terminals) != 1 || l.Terminals[0].Name != "main" || l.Version != Version {
		t.Fatalf("list: %c %s", k, p)
	}
	if k, p := request(t, bin, "attach", "-key", key, "2"); k != KindError || !strings.Contains(string(p), ErrAbsent) {
		t.Fatalf("attaching a terminal that does not run: %c %s", k, p)
	}
	// Kill hangs up the shell, which tells its attachment.
	if k, p := request(t, bin, "kill", "-key", key, "main"); k != KindKilled {
		t.Fatalf("kill: %c %s", k, p)
	}
	if _, err := b.waitExit(); err != nil {
		t.Fatal("the attachment after a kill:", err)
	}
	if _, p := request(t, bin, "list", "-key", key); !strings.Contains(string(p), `"terminals":[]`) {
		t.Fatalf("list after the kill: %s", p)
	}

	// An exit ends only that terminal, also when what it left in the
	// background keeps the PTY open.
	c := attach(t, bin, key, "main", "main", "--", "/bin/sh")
	c.send(KindInput, []byte("sleep 20 & exit 3\n"))
	if st, err := c.waitExit(); err != nil || st != 3 {
		t.Fatal("exit status:", st, err)
	}

	// SIGTERM ends the daemon and its terminals.
	d := attach(t, bin, key, "2", "2", "--", "/bin/sh")
	d.send(KindInput, []byte("echo $$\n"))
	_ = serve.Process.Signal(syscall.SIGTERM)
	if _, err := d.waitExit(); err != nil {
		t.Fatal("the attachment when the daemon stopped:", err)
	}
	if err := serve.Wait(); err != nil {
		t.Fatal("the daemon's exit:", err)
	}
	if _, err := os.Stat(SocketPath(key)); !os.IsNotExist(err) {
		t.Fatal("the socket is left:", err)
	}
}

type attached struct {
	cmd   *exec.Cmd
	in    io.WriteCloser
	r     *bufio.Reader
	hello Hello
	seen  bytes.Buffer
}

func attach(t *testing.T, bin, key, name string, args ...string) *attached {
	t.Helper()
	cmd := exec.Command(bin, append([]string{"attach", "-key", key}, args...)...)
	in, _ := cmd.StdinPipe()
	out, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	a := &attached{cmd: cmd, in: in, r: bufio.NewReader(out)}
	t.Cleanup(a.close)
	k, p, err := ReadMessage(a.r)
	if err != nil || k != KindHello || json.Unmarshal(p, &a.hello) != nil {
		t.Fatalf("attach %s: %c %s %v", name, k, p, err)
	}
	return a
}

func (a *attached) send(kind byte, p []byte) { _ = WriteMessage(a.in, kind, p) }

func (a *attached) close() { a.in.Close(); a.cmd.Process.Kill(); a.cmd.Wait() }

func (a *attached) waitFor(t *testing.T, want string) {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		for !strings.Contains(a.seen.String(), want) {
			k, p, err := ReadMessage(a.r)
			if err != nil {
				done <- err
				return
			}
			if k == KindOutput {
				a.seen.Write(p)
			}
		}
		done <- nil
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("waiting for %q: %v (%q)", want, err, a.seen.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("no %q in %q", want, a.seen.String())
	}
}

// waitExit reads until the exit message.
func (a *attached) waitExit() (int, error) {
	done := make(chan struct{})
	timer := time.AfterFunc(15*time.Second, func() { a.cmd.Process.Kill() })
	defer timer.Stop()
	defer close(done)
	for {
		k, p, err := ReadMessage(a.r)
		if err != nil {
			return 0, err
		}
		if k == KindExit {
			var e Exit
			err := json.Unmarshal(p, &e)
			return e.Status, err
		}
	}
}

func request(t *testing.T, bin string, args ...string) (byte, []byte) {
	t.Helper()
	out, err := exec.Command(bin, args...).Output()
	k, p, rerr := ReadMessage(bytes.NewReader(out))
	if rerr != nil {
		t.Fatalf("%v: %v %v %q", args, err, rerr, out)
	}
	return k, p
}
