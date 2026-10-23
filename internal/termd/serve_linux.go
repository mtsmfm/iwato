package termd

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// Dir is the directory of the daemons' sockets and locks: the user's own
// (0700), so that only the sandbox user reaches them. It keeps the name of
// the daemon's first version (iwato-term), so that both binaries (the
// image's iwato-entrypoint and an older sandbox's iwato-term) reach the
// same daemon.
func Dir() string { return fmt.Sprintf("/tmp/iwato-term-%d", os.Getuid()) }

// SocketPath is the socket of the daemon with key.
func SocketPath(key string) string { return filepath.Join(Dir(), key+".sock") }

// ensureDir makes Dir, or checks that the existing one is the user's own
// directory that no one else can enter.
func ensureDir() (string, error) {
	dir := Dir()
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", err
	}
	return dir, checkDir(dir)
}

// checkDir checks that dir is the user's own directory that no one else
// can enter (another user could have made it first, to serve a daemon of
// its own there).
func checkDir(dir string) error {
	var st unix.Stat_t
	if err := unix.Lstat(dir, &st); err != nil {
		return err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFDIR || int(st.Uid) != os.Getuid() || st.Mode&0o077 != 0 {
		return fmt.Errorf("%s is not a private directory of this user", dir)
	}
	return nil
}

// ErrRunning is Serve's error when another daemon holds the key (only
// with standby false).
var ErrRunning = errors.New("another daemon serves this key")

// Serve runs the daemon for key until SIGTERM or SIGINT, which end its
// terminals. One daemon serves a key: with standby, Serve waits for the
// one running to end and then takes over (OpenShell's main process must
// not exit); without, it returns ErrRunning.
func Serve(key string, standby bool) error {
	if !ValidKey(key) {
		return errors.New("invalid key")
	}
	// Not PR_SET_DUMPABLE 0: OpenShell reads a connection's process and its
	// ancestors through /proc and denies the connection when it cannot read
	// the daemon's. Its seccomp filter refuses ptrace and process_vm_readv
	// to the sandbox's processes anyway.
	dir, err := ensureDir()
	if err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(dir, key+".lock"), os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	how := unix.LOCK_EX
	if !standby {
		how |= unix.LOCK_NB
	}
	for {
		err = unix.Flock(int(lock.Fd()), how)
		if err != unix.EINTR {
			break
		}
	}
	if errors.Is(err, unix.EWOULDBLOCK) {
		return ErrRunning
	} else if err != nil {
		return err
	}
	// The lock is ours: a socket there is a dead daemon's.
	path := SocketPath(key)
	_ = os.Remove(path)
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return err
	}
	ln.SetUnlinkOnClose(true)
	d := &daemon{started: time.Now(), terms: map[string]*term{}}
	// Its path tells Iwato which binary serves the CLI's commands (the
	// image's, or the one the WSLc driver placed in an older sandbox).
	d.path, _ = os.Executable()
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT)
	signal.Ignore(syscall.SIGHUP)
	go func() {
		<-sigs
		ln.Close()
	}()
	for {
		c, err := ln.AcceptUnix()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				break
			}
			continue
		}
		if !sameUser(c) {
			c.Close()
			continue
		}
		go d.serve(c)
	}
	d.endAll(time.Second)
	// The clients get the terminals' exits before the daemon leaves.
	flushed := make(chan struct{})
	go func() { d.writers.Wait(); close(flushed) }()
	select {
	case <-flushed:
	case <-time.After(2 * time.Second):
	}
	return nil
}

// sameUser checks the peer's uid (SO_PEERCRED).
func sameUser(c *net.UnixConn) bool {
	raw, err := c.SyscallConn()
	if err != nil {
		return false
	}
	ok := false
	_ = raw.Control(func(fd uintptr) {
		cred, err := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		ok = err == nil && int(cred.Uid) == os.Getuid()
	})
	return ok
}

type daemon struct {
	started time.Time
	path    string
	writers sync.WaitGroup // the clients' writers
	mu      sync.Mutex
	terms   map[string]*term
}

// term is one terminal: a process on a PTY, its recent output and its
// attached clients.
type term struct {
	name    string
	started time.Time
	cmd     *exec.Cmd
	ptm     *os.File
	inMu    sync.Mutex
	mu      sync.Mutex // what follows
	ring    *Ring
	cols    int
	rows    int
	clients map[*client]struct{}
	ended   bool
	done    chan struct{}
}

// client is one attached connection; out carries encoded messages to it.
type client struct {
	c    *net.UnixConn
	out  chan []byte
	once sync.Once
}

// clientQueue bounds the messages waiting for a client; one that falls that
// far behind is dropped (it attaches again and gets the recent output).
const clientQueue = 1024

func (cl *client) drop() { cl.once.Do(func() { close(cl.out) }) }

// cut drops a client that fell behind, and its connection: its writer may
// be stuck writing to it.
func (cl *client) cut() {
	cl.drop()
	cl.c.Close()
}

// send queues m for every client; t.mu is held.
func (t *term) send(m []byte) {
	for cl := range t.clients {
		select {
		case cl.out <- m:
		default:
			delete(t.clients, cl)
			cl.cut()
		}
	}
}

func (d *daemon) serve(c *net.UnixConn) {
	r := bufio.NewReaderSize(c, 64<<10)
	kind, p, err := ReadMessage(r)
	if err != nil {
		c.Close()
		return
	}
	switch kind {
	case KindList:
		_ = WriteJSON(c, KindListed, d.list())
		c.Close()
	case KindKill:
		var k Kill
		if json.Unmarshal(p, &k) != nil || !ValidName(k.Name) {
			_ = WriteJSON(c, KindError, Error{ErrInvalid, "invalid kill request"})
		} else if t := d.get(k.Name); t == nil {
			_ = WriteJSON(c, KindError, Error{ErrAbsent, "no such terminal"})
		} else {
			t.kill()
			_ = WriteMessage(c, KindKilled, nil)
		}
		c.Close()
	case KindOpen:
		var o Open
		if json.Unmarshal(p, &o) != nil || !ValidName(o.Name) {
			_ = WriteJSON(c, KindError, Error{ErrInvalid, "invalid open request"})
			c.Close()
			return
		}
		d.attach(c, r, o)
	default:
		_ = WriteJSON(c, KindError, Error{ErrInvalid, "unknown request"})
		c.Close()
	}
}

func (d *daemon) get(name string) *term {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.terms[name]
}

func (d *daemon) list() List {
	d.mu.Lock()
	ts := make([]*term, 0, len(d.terms))
	for _, t := range d.terms {
		ts = append(ts, t)
	}
	d.mu.Unlock()
	l := List{Version: Version, Capabilities: Capabilities, Path: d.path, DaemonStarted: d.started.UnixMilli(), Terminals: []Terminal{}}
	for _, t := range ts {
		t.mu.Lock()
		l.Terminals = append(l.Terminals, Terminal{Name: t.name, Started: t.started.UnixMilli(), Cols: t.cols, Rows: t.rows})
		t.mu.Unlock()
	}
	return l
}

// attach finds or starts the terminal and serves the client until it
// goes or the terminal ends.
func (d *daemon) attach(c *net.UnixConn, r *bufio.Reader, o Open) {
	d.mu.Lock()
	t := d.terms[o.Name]
	created := false
	if t == nil {
		var e *Error
		switch {
		case len(o.Cmd) == 0:
			e = &Error{ErrAbsent, "no such terminal"}
		case len(d.terms) >= MaxTerminals:
			e = &Error{ErrLimit, fmt.Sprintf("at most %d terminals", MaxTerminals)}
		default:
			var err error
			if t, err = d.start(o); err != nil {
				e = &Error{ErrStart, err.Error()}
			}
		}
		if e != nil {
			d.mu.Unlock()
			_ = WriteJSON(c, KindError, e)
			c.Close()
			return
		}
		created = true
	}
	d.mu.Unlock()

	cl := &client{c: c, out: make(chan []byte, clientQueue)}
	t.mu.Lock()
	if t.ended {
		t.mu.Unlock()
		_ = WriteJSON(c, KindError, Error{ErrAbsent, "the terminal has ended"})
		c.Close()
		return
	}
	cols, rows := t.cols, t.rows
	if o.Cols > 0 && o.Rows > 0 {
		cols, rows = o.Cols, o.Rows
	}
	replay, offset := t.ring.Bytes()
	hello, _ := json.Marshal(Hello{Version: Version, Capabilities: Capabilities, DaemonStarted: d.started.UnixMilli(), Started: t.started.UnixMilli(), Created: created, Cols: cols, Rows: rows, Offset: offset})
	cl.out <- Message(KindHello, hello)
	for len(replay) > 0 {
		n := min(len(replay), 64<<10)
		cl.out <- Message(KindOutput, replay[:n])
		replay = replay[n:]
	}
	t.clients[cl] = struct{}{}
	t.mu.Unlock()

	d.writers.Add(1)
	go func() {
		defer d.writers.Done()
		for m := range cl.out {
			if _, err := c.Write(m); err != nil {
				break
			}
		}
		c.Close()
	}()
	if !created {
		// A program that only redraws what changed would leave the new
		// screen blank: it gets a SIGWINCH (as the main process did, a
		// row shorter and back when the size is the same).
		go t.repaint(cols, rows)
	}
	for {
		kind, p, err := ReadMessage(r)
		if err != nil {
			break
		}
		switch kind {
		case KindInput:
			t.inMu.Lock()
			_, err = t.ptm.Write(p)
			t.inMu.Unlock()
		case KindResize:
			if cols, rows, ok := ParseResize(p); ok {
				t.resize(cols, rows)
			}
		case KindEnd:
			go t.kill()
		case KindCwd:
			m := Message(KindCwd, []byte(t.cwd()))
			t.mu.Lock()
			if _, ok := t.clients[cl]; ok {
				select {
				case cl.out <- m:
				default:
				}
			}
			t.mu.Unlock()
		}
		if err != nil {
			break
		}
	}
	t.mu.Lock()
	if _, ok := t.clients[cl]; ok {
		delete(t.clients, cl)
		cl.drop()
	}
	t.mu.Unlock()
}

// cwd is the working directory of the PTY's foreground process group
// leader, or of the terminal's first process when that has gone.
func (t *term) cwd() string {
	pgrp := 0
	if raw, err := t.ptm.SyscallConn(); err == nil {
		_ = raw.Control(func(fd uintptr) { pgrp, _ = unix.IoctlGetInt(int(fd), unix.TIOCGPGRP) })
	}
	for _, pid := range []int{pgrp, t.cmd.Process.Pid} {
		if pid <= 0 {
			continue
		}
		if dir, err := os.Readlink("/proc/" + strconv.Itoa(pid) + "/cwd"); err == nil && strings.HasPrefix(dir, "/") {
			return dir
		}
	}
	return ""
}

// repaintDelay is how long the terminal stays a row shorter: long enough
// for the program to handle the first SIGWINCH before the second (Node.js
// reads the size when it handles the signal and ignores an unchanged one).
const repaintDelay = 200 * time.Millisecond

func (t *term) repaint(cols, rows int) {
	t.mu.Lock()
	same := cols == t.cols && rows == t.rows
	t.mu.Unlock()
	if same && rows >= 2 {
		t.resize(cols, rows-1)
		time.Sleep(repaintDelay)
	}
	t.resize(cols, rows)
}

func (t *term) resize(cols, rows int) {
	if cols < 1 || rows < 1 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.ended {
		return
	}
	if setSize(t.ptm, cols, rows) == nil {
		t.cols, t.rows = cols, rows
	}
}

// start runs o.Cmd on a new PTY; d.mu is held.
func (d *daemon) start(o Open) (*term, error) {
	cols, rows := o.Cols, o.Rows
	if cols <= 0 || rows <= 0 {
		cols, rows = 80, 24
	}
	ptm, pts, err := openPTY(cols, rows)
	if err != nil {
		return nil, err
	}
	defer pts.Close()
	cmd := exec.Command(o.Cmd[0], o.Cmd[1:]...)
	cmd.Env = append(withoutKey(o.Env, "TERM"), "TERM=xterm-256color")
	if o.Dir != "" {
		if fi, err := os.Stat(o.Dir); err == nil && fi.IsDir() {
			cmd.Dir = o.Dir
		}
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = pts, pts, pts
	// Its own session with the PTY as its controlling terminal: job
	// control works, and a hangup reaches what runs in it.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := cmd.Start(); err != nil {
		ptm.Close()
		return nil, err
	}
	t := &term{name: o.Name, started: time.Now(), cmd: cmd, ptm: ptm, ring: NewRing(RingSize), cols: cols, rows: rows, clients: map[*client]struct{}{}, done: make(chan struct{})}
	d.terms[o.Name] = t
	go d.run(t)
	return t, nil
}

func withoutKey(env []string, key string) []string {
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if !strings.HasPrefix(kv, key+"=") {
			out = append(out, kv)
		}
	}
	return out
}

// run copies the terminal's output until its process has ended (and what
// it wrote last has been read), then tells the clients and forgets it.
func (d *daemon) run(t *term) {
	output := make(chan struct{})
	go func() {
		defer close(output)
		buf := make([]byte, 32<<10)
		for {
			n, err := t.ptm.Read(buf)
			if n > 0 {
				t.mu.Lock()
				t.ring.Write(buf[:n])
				t.send(Message(KindOutput, append([]byte(nil), buf[:n]...)))
				t.mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()
	_ = t.cmd.Wait()
	// Processes it left in the background may keep the PTY open: what is
	// still to come is read for a moment only.
	_ = t.ptm.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	<-output
	status := 0
	if ws, ok := t.cmd.ProcessState.Sys().(syscall.WaitStatus); ok {
		switch {
		case ws.Signaled():
			status = 128 + int(ws.Signal())
		default:
			status = ws.ExitStatus()
		}
	}
	d.mu.Lock()
	if d.terms[t.name] == t {
		delete(d.terms, t.name)
	}
	d.mu.Unlock()
	exit, _ := json.Marshal(Exit{Status: status})
	t.mu.Lock()
	t.ended = true
	t.ptm.Close()
	for cl := range t.clients {
		select {
		case cl.out <- Message(KindExit, exit):
		default:
		}
		cl.drop()
	}
	t.clients = nil
	t.mu.Unlock()
	close(t.done)
}

// killGrace is how long a terminal has after its hangup.
const killGrace = 3 * time.Second

// kill hangs the terminal up and kills what is left of its session after
// killGrace; it returns once the terminal has ended.
func (t *term) kill() { t.end(killGrace) }

func (t *term) end(grace time.Duration) {
	pid := t.cmd.Process.Pid
	_ = syscall.Kill(-pid, syscall.SIGHUP)
	_ = syscall.Kill(-pid, syscall.SIGCONT)
	select {
	case <-t.done:
		return
	case <-time.After(grace):
	}
	killSession(pid)
	select {
	case <-t.done:
	case <-time.After(5 * time.Second):
	}
}

// killSession sends SIGKILL to every process of the session sid.
func killSession(sid int) {
	_ = syscall.Kill(-sid, syscall.SIGKILL)
	ents, _ := os.ReadDir("/proc")
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		b, err := os.ReadFile("/proc/" + e.Name() + "/stat")
		if err != nil {
			continue
		}
		// pid (comm) state ppid pgrp session ...; comm may hold anything.
		i := strings.LastIndexByte(string(b), ')')
		if i < 0 {
			continue
		}
		f := strings.Fields(string(b[i+1:]))
		if len(f) > 3 && f[3] == strconv.Itoa(sid) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}
}

// endAll ends every terminal (the daemon is stopping).
func (d *daemon) endAll(grace time.Duration) {
	d.mu.Lock()
	ts := make([]*term, 0, len(d.terms))
	for _, t := range d.terms {
		ts = append(ts, t)
	}
	d.mu.Unlock()
	var wg sync.WaitGroup
	for _, t := range ts {
		wg.Add(1)
		go func() { defer wg.Done(); t.end(grace) }()
	}
	wg.Wait()
}

// openPTY opens a PTY pair of cols×rows. The master is non-blocking, so
// that reads on it can have a deadline (never call its Fd: that makes it
// blocking).
func openPTY(cols, rows int) (ptm, pts *os.File, err error) {
	fd, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, err
	}
	n, err := 0, unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0)
	if err == nil {
		n, err = unix.IoctlGetInt(fd, unix.TIOCGPTN)
	}
	if err == nil {
		err = unix.IoctlSetWinsize(fd, unix.TIOCSWINSZ, &unix.Winsize{Row: uint16(rows), Col: uint16(cols)})
	}
	if err != nil {
		unix.Close(fd)
		return nil, nil, err
	}
	ptm = os.NewFile(uintptr(fd), "/dev/ptmx")
	pts, err = os.OpenFile("/dev/pts/"+strconv.Itoa(n), os.O_RDWR|syscall.O_NOCTTY|syscall.O_CLOEXEC, 0)
	if err != nil {
		ptm.Close()
		return nil, nil, err
	}
	return ptm, pts, nil
}

// setSize sizes the PTY.
func setSize(ptm *os.File, cols, rows int) error {
	raw, err := ptm.SyscallConn()
	if err != nil {
		return err
	}
	cerr := raw.Control(func(fd uintptr) {
		err = unix.IoctlSetWinsize(int(fd), unix.TIOCSWINSZ, &unix.Winsize{Row: uint16(rows), Col: uint16(cols)})
	})
	if cerr != nil {
		return cerr
	}
	return err
}

// the client side, used by the CLI (cmd/iwato-entrypoint)

// Conn is a connection to a daemon.
type Conn = *net.UnixConn

// Dial connects to the daemon with key. It reaches only a daemon in this
// user's private directory: the open request carries the environment.
func Dial(key string) (*net.UnixConn, error) {
	if err := checkDir(Dir()); err != nil {
		return nil, err
	}
	c, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: SocketPath(key), Net: "unix"})
	if err != nil {
		return nil, err
	}
	if !sameUser(c) {
		c.Close()
		return nil, errors.New("the terminal daemon's socket is another user's")
	}
	return c, nil
}

// DialOrSpawn connects to the daemon with key, starting one (self, in a
// session of its own so that it outlives the exec that started it) when
// there is none.
func DialOrSpawn(key, self string) (*net.UnixConn, error) {
	c, err := Dial(key)
	if err == nil {
		return c, nil
	}
	if _, err := ensureDir(); err != nil {
		return nil, err
	}
	cmd := exec.Command(self, "serve", "-key", key, "-detached")
	cmd.Env = []string{}
	cmd.Dir = "/"
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	go func() { _ = cmd.Wait() }()
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		if c, err = Dial(key); err == nil || time.Now().After(deadline) {
			return c, err
		}
	}
}

// Relay sends the open request, then copies messages between the daemon
// and in/out until the daemon closes the connection.
func Relay(c *net.UnixConn, o Open, in io.Reader, out io.Writer) error {
	if err := WriteJSON(c, KindOpen, o); err != nil {
		return err
	}
	go func() {
		_, _ = io.Copy(c, in)
		_ = c.CloseWrite()
	}()
	_, err := io.Copy(out, c)
	return err
}

// Request sends a list or kill request and copies the answer (one message)
// to out.
func Request(c *net.UnixConn, kind byte, v any, out io.Writer) error {
	var err error
	if v == nil {
		err = WriteMessage(c, kind, nil)
	} else {
		err = WriteJSON(c, kind, v)
	}
	if err != nil {
		return err
	}
	k, p, err := ReadMessage(c)
	if err != nil {
		return err
	}
	return WriteMessage(out, k, p)
}
