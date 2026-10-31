package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func needSh(t *testing.T) {
	t.Helper()
	// The script runs in the Linux sandbox image (it reads /proc).
	if runtime.GOOS != "linux" {
		t.Skip("needs Linux")
	}
}

// waitUntil polls until cond holds.
func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if cond() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func fileExists(p string) bool { _, err := os.Lstat(p); return err == nil }

func pidAlive(pid int) bool {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	return err == nil && !strings.Contains(string(b), ") Z ")
}

func readPid(t *testing.T, p string) int {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatalf("%s: %q", p, b)
	}
	return n
}

// deadPid is the pid of a process that has exited and been reaped.
func deadPid(t *testing.T) int {
	t.Helper()
	c := exec.Command("true")
	if err := c.Run(); err != nil {
		t.Fatal(err)
	}
	return c.Process.Pid
}

// startFake runs a long-lived process whose name (comm) is name, for the
// "owner is alive" cases.
func startFake(t *testing.T, name string) int {
	t.Helper()
	bin := filepath.Join(t.TempDir(), name)
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("no sleep")
	}
	if err := os.Symlink(sleep, bin); err != nil {
		t.Fatal(err)
	}
	c := exec.Command(bin, "60")
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Process.Kill(); c.Wait() })
	return c.Process.Pid
}

func TestDesktopScriptRemovesStaleX11State(t *testing.T) {
	needSh(t)
	d := prepareDesktop(t)
	tmp := filepath.Join(d, "tmp")
	dead, live := deadPid(t), startFake(t, "Xvfb")
	write := func(n int, pid int, sock bool) {
		lock := filepath.Join(tmp, fmt.Sprintf(".X%d-lock", n))
		if err := os.WriteFile(lock, []byte(fmt.Sprintf("%10d\n", pid)), 0o444); err != nil {
			t.Fatal(err)
		}
		if sock {
			if err := os.WriteFile(filepath.Join(tmp, ".X11-unix", fmt.Sprintf("X%d", n)), nil, 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}
	write(99, dead, true) // the server was killed
	write(98, 1, true)    // pid reused by an unrelated process (not an X server)
	write(97, live, true) // a live server
	os.WriteFile(filepath.Join(tmp, ".Xfoo-lock"), []byte("1\n"), 0o444)
	out := runDesktopIn(t, d, "IWATO_DXG=/nonexistent/dxg")
	for _, gone := range []string{".X99-lock", ".X11-unix/X99", ".X98-lock", ".X11-unix/X98"} {
		if fileExists(filepath.Join(tmp, gone)) {
			t.Errorf("%s was kept\n%s", gone, out)
		}
	}
	for _, kept := range []string{".X97-lock", ".X11-unix/X97", ".Xfoo-lock"} {
		if !fileExists(filepath.Join(tmp, kept)) {
			t.Errorf("%s was removed", kept)
		}
	}
	if !strings.Contains(out, "selkies-session --session xfce") || !strings.Contains(out, "removed 2 stale") {
		t.Errorf("%s", out)
	}
}

func TestDesktopScriptRemovesStaleSessionFiles(t *testing.T) {
	needSh(t)
	d := prepareDesktop(t)
	dead := deadPid(t)
	deadS := strconv.Itoa(dead)
	// PulseAudio: the pid file and the socket next to it.
	pulse := filepath.Join(d, "run", "pulse")
	livePulse := filepath.Join(d, "run2", "pulse")
	for dir, pid := range map[string]string{pulse: deadS, livePulse: strconv.Itoa(startFake(t, "pulseaudio"))} {
		os.MkdirAll(dir, 0o700)
		os.WriteFile(filepath.Join(dir, "pid"), []byte(pid+"\n"), 0o600)
		os.WriteFile(filepath.Join(dir, "native"), nil, 0o600)
	}
	// D-Bus session file of an old session.
	sb := filepath.Join(d, ".dbus", "session-bus")
	os.MkdirAll(sb, 0o700)
	os.WriteFile(filepath.Join(sb, "abc-0"), []byte("DBUS_SESSION_BUS_ADDRESS=x\nDBUS_SESSION_BUS_PID="+deadS+"\n"), 0o600)
	// Firefox: a dead profile lock, a live one, and a loose .parentlock.
	ff := func(name, target string) string {
		p := filepath.Join(d, ".mozilla", "firefox", name)
		os.MkdirAll(p, 0o700)
		os.WriteFile(filepath.Join(p, ".parentlock"), nil, 0o600)
		if target != "" {
			os.Symlink(target, filepath.Join(p, "lock"))
		}
		return p
	}
	deadProf := ff("dead.default", "127.0.0.1:+"+deadS)
	liveProf := ff("live.default", "127.0.0.1:+"+strconv.Itoa(startFake(t, "firefox")))
	looseProf := ff("loose.default", "")
	out := runDesktopIn(t, d, "IWATO_DXG=/nonexistent/dxg", "XDG_RUNTIME_DIR="+filepath.Join(d, "run"))
	// XDG_RUNTIME_DIR/pulse is the dead one; the live one is not under it.
	for _, p := range []string{filepath.Join(pulse, "pid"), filepath.Join(pulse, "native"), filepath.Join(sb, "abc-0"), filepath.Join(deadProf, "lock"), filepath.Join(deadProf, ".parentlock")} {
		if fileExists(p) {
			t.Errorf("%s was kept\n%s", p, out)
		}
	}
	for _, p := range []string{filepath.Join(livePulse, "pid"), filepath.Join(liveProf, "lock"), filepath.Join(liveProf, ".parentlock")} {
		if !fileExists(p) {
			t.Errorf("%s was removed", p)
		}
	}
	// firefox runs (the fake), so the loose .parentlock stays.
	if !fileExists(filepath.Join(looseProf, ".parentlock")) {
		t.Error("a .parentlock was removed while firefox runs")
	}
	// The user's own profile files are untouched.
	if !fileExists(filepath.Join(deadProf)) {
		t.Error("the profile is gone")
	}
}

func TestDesktopScriptRefusesASecondCopy(t *testing.T) {
	needSh(t)
	d := prepareDesktop(t)
	// A live iwato-desktop whose pid the pidfile names.
	script := filepath.Join(d, "iwato-desktop")
	os.WriteFile(script, []byte("#!/bin/sh\nsleep 60 &\nwait\n"), 0o755)
	c := exec.Command("/bin/sh", script)
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { syscall.Kill(c.Process.Pid, syscall.SIGKILL); c.Wait() })
	pf := filepath.Join(d, ".cache", "iwato-desktop.pid")
	os.MkdirAll(filepath.Dir(pf), 0o700)
	os.WriteFile(pf, []byte(strconv.Itoa(c.Process.Pid)+"\n"), 0o600)
	// A lock that would be cleaned if this copy went on.
	os.WriteFile(filepath.Join(d, "tmp", ".X96-lock"), []byte("1\n"), 0o444)
	out := runDesktopIn(t, d, "IWATO_DXG=/nonexistent/dxg")
	if !strings.Contains(out, "already running") || strings.Contains(out, "selkies-session") {
		t.Fatalf("%s", out)
	}
	if !fileExists(filepath.Join(d, "tmp", ".X96-lock")) || readPid(t, pf) != c.Process.Pid {
		t.Fatal("a second copy touched the running one's state")
	}
}

// A normal run ends without leaving its pidfile, and a stale one is replaced.
func TestDesktopScriptPidfileLifecycle(t *testing.T) {
	needSh(t)
	d := prepareDesktop(t)
	pf := filepath.Join(d, ".cache", "iwato-desktop.pid")
	os.MkdirAll(filepath.Dir(pf), 0o700)
	os.WriteFile(pf, []byte(strconv.Itoa(deadPid(t))+"\n"), 0o600)
	out := runDesktopIn(t, d, "IWATO_DXG=/nonexistent/dxg")
	if !strings.Contains(out, "selkies-session --session xfce") {
		t.Fatalf("%s", out)
	}
	if fileExists(pf) {
		t.Fatal("the pidfile is left behind")
	}
}

// TERM (what stopping the sandbox sends through the supervisor, when it
// reaches the desktop at all) stops selkies-session and its children.
func TestDesktopScriptStopsItsSessionOnTERM(t *testing.T) {
	needSh(t)
	if _, err := exec.LookPath("pgrep"); err != nil {
		t.Skip("no pgrep")
	}
	d := prepareDesktop(t)
	// A session that ignores TERM in its children's way: a shell with a
	// sleeping child, like selkies-session's Xvfb/PulseAudio/desktop.
	stub := "#!/bin/sh\necho $$ > " + d + "/session.pid\nsleep 60 &\necho $! > " + d + "/child.pid\nwait\n"
	if err := os.WriteFile(filepath.Join(d, "bin", "selkies-session"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	c := exec.Command("sh", filepath.Join(d, "sandboximage", "desktop", "iwato-desktop"), "run")
	c.Env = []string{"PATH=" + filepath.Join(d, "bin") + ":" + os.Getenv("PATH"), "HOME=" + d, "IWATO_TMP=" + filepath.Join(d, "tmp"), "IWATO_DXG=/nonexistent/dxg"}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Process.Kill() })
	waitUntil(t, "the session", func() bool { return fileExists(filepath.Join(d, "child.pid")) })
	time.Sleep(100 * time.Millisecond)
	session, child := readPid(t, filepath.Join(d, "session.pid")), readPid(t, filepath.Join(d, "child.pid"))
	c.Process.Signal(syscall.SIGTERM)
	done := make(chan error, 1)
	go func() { done <- c.Wait() }()
	select {
	case err := <-done:
		if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 143 {
			t.Fatalf("exit: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("did not stop on TERM")
	}
	waitUntil(t, "the session to end", func() bool { return !pidAlive(session) && !pidAlive(child) })
	if fileExists(filepath.Join(d, ".cache", "iwato-desktop.pid")) {
		t.Error("the pidfile is left behind")
	}
}

// cliResult is what `iwato-desktop <args>` printed and exited with.
type cliResult struct {
	out  string
	code int
}

// runCLI runs the prepared iwato-desktop with args (later env entries win).
// It returns only when the command's output is closed, which a desktop that
// kept it open would prevent: start must send the desktop's output to the log.
func runCLI(t *testing.T, d string, args []string, env ...string) cliResult {
	t.Helper()
	needSh(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, "sh", append([]string{filepath.Join(d, "sandboximage", "desktop", "iwato-desktop")}, args...)...)
	c.WaitDelay = 2 * time.Second
	c.Env = append([]string{"PATH=" + filepath.Join(d, "bin") + ":" + os.Getenv("PATH"), "HOME=" + d, "IWATO_TMP=" + filepath.Join(d, "tmp"), "IWATO_DXG=/nonexistent/dxg"}, env...)
	out, err := c.CombinedOutput()
	code := 0
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok || ctx.Err() != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		code = ee.ExitCode()
	}
	return cliResult{string(out), code}
}

// startableDesktop is prepareDesktop with a selkies-session that stays up (it
// prints a line to the log and sleeps, with a child like the real session),
// and a cleanup that stops whatever the test started.
func startableDesktop(t *testing.T) string {
	t.Helper()
	needSh(t)
	d := prepareDesktop(t)
	stub := "#!/bin/sh\necho \"selkies-session $*\"\nsleep 60 &\nwait\n"
	if err := os.WriteFile(filepath.Join(d, "bin", "selkies-session"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if b, err := os.ReadFile(filepath.Join(d, ".cache", "iwato-desktop.pid")); err == nil {
			if n, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
				syscall.Kill(n, syscall.SIGTERM)
			}
		}
	})
	return d
}

func TestDesktopStartIsDetachedAndIdempotent(t *testing.T) {
	d := startableDesktop(t)
	pf, lg := filepath.Join(d, ".cache", "iwato-desktop.pid"), filepath.Join(d, ".cache", "iwato-desktop.log")
	if r := runCLI(t, d, nil); r.code != 0 || !strings.Contains(r.out, "started") || !strings.Contains(r.out, "6080") || !strings.Contains(r.out, "「ポート」") || !strings.Contains(r.out, lg) {
		t.Fatalf("first start (no argument): %+v", r)
	}
	pid := readPid(t, pf)
	if !pidAlive(pid) {
		t.Fatal("the desktop is not running after start returned")
	}
	waitUntil(t, "the session's output in the log", func() bool {
		b, _ := os.ReadFile(lg)
		return strings.Contains(string(b), "selkies-session --session xfce")
	})
	// A second start, explicit or not, starts nothing and succeeds.
	for _, args := range [][]string{nil, {"start"}} {
		r := runCLI(t, d, args)
		if r.code != 0 || !strings.Contains(r.out, "already running") || !strings.Contains(r.out, strconv.Itoa(pid)) {
			t.Fatalf("second start %v: %+v", args, r)
		}
	}
	if b, _ := os.ReadFile(lg); strings.Count(string(b), "selkies-session --session") != 1 || readPid(t, pf) != pid {
		t.Fatalf("a second copy was started:\n%s", b)
	}
}

// pathWithout is a directory of links to the system's programs, except name:
// the sandbox image has the real desktop in /usr/bin.
func pathWithout(t *testing.T, name string) string {
	t.Helper()
	dir := t.TempDir()
	for _, from := range []string{"/usr/bin", "/bin"} {
		entries, err := os.ReadDir(from)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.Name() != name {
				os.Symlink(filepath.Join(from, e.Name()), filepath.Join(dir, e.Name()))
			}
		}
	}
	return dir
}

func TestDesktopStartWithoutTheDesktop(t *testing.T) {
	d := prepareDesktop(t)
	r := runCLI(t, d, []string{"start"}, "PATH="+pathWithout(t, "selkies-session"))
	if r.code != 1 || !strings.Contains(r.out, "selkies-session not found") {
		t.Fatalf("%+v", r)
	}
	if fileExists(filepath.Join(d, ".cache", "iwato-desktop.pid")) {
		t.Error("a pidfile was written")
	}
}

func TestDesktopStatus(t *testing.T) {
	d := startableDesktop(t)
	lg := filepath.Join(d, ".cache", "iwato-desktop.log")
	if r := runCLI(t, d, []string{"status"}); r.code != 1 || !strings.Contains(r.out, "not running") || !strings.Contains(r.out, lg) {
		t.Fatalf("before start: %+v", r)
	}
	os.MkdirAll(filepath.Join(d, ".cache"), 0o700)
	os.WriteFile(filepath.Join(d, ".cache", "iwato-desktop.pid"), []byte(strconv.Itoa(deadPid(t))+"\n"), 0o600)
	if r := runCLI(t, d, []string{"status"}); r.code != 1 {
		t.Fatalf("stale pidfile: %+v", r)
	}
	runCLI(t, d, []string{"start"})
	pid := readPid(t, filepath.Join(d, ".cache", "iwato-desktop.pid"))
	if r := runCLI(t, d, []string{"status"}); r.code != 0 || !strings.Contains(r.out, "running (pid "+strconv.Itoa(pid)+")") || !strings.Contains(r.out, lg) {
		t.Fatalf("running: %+v", r)
	}
}

func TestDesktopStopEndsTheSessionAndCleansUp(t *testing.T) {
	if _, err := exec.LookPath("pgrep"); err != nil {
		t.Skip("no pgrep")
	}
	d := startableDesktop(t)
	pf := filepath.Join(d, ".cache", "iwato-desktop.pid")
	if r := runCLI(t, d, []string{"stop"}); r.code != 0 || !strings.Contains(r.out, "not running") {
		t.Fatalf("stop when not running: %+v", r)
	}
	runCLI(t, d, []string{"start"})
	pid := readPid(t, pf)
	// The session's child (the stub's sleep) must go too.
	var children []int
	waitUntil(t, "the session's children", func() bool {
		children = nil
		var walk func(p int)
		walk = func(p int) {
			b, _ := exec.Command("pgrep", "-P", strconv.Itoa(p)).Output()
			for _, f := range strings.Fields(string(b)) {
				n, _ := strconv.Atoi(f)
				children = append(children, n)
				walk(n)
			}
		}
		walk(pid)
		return len(children) >= 2 // selkies-session and its sleep
	})
	if r := runCLI(t, d, []string{"stop"}); r.code != 0 || !strings.Contains(r.out, "stopped") {
		t.Fatalf("stop: %+v", r)
	}
	waitUntil(t, "the processes to end", func() bool {
		for _, p := range append(children, pid) {
			if pidAlive(p) {
				return false
			}
		}
		return true
	})
	if fileExists(pf) {
		t.Error("the pidfile is left behind")
	}
	if r := runCLI(t, d, []string{"status"}); r.code != 1 {
		t.Fatalf("status after stop: %+v", r)
	}
}

// A desktop that ignores TERM is killed after the wait.
func TestDesktopStopKillsWhatIgnoresTERM(t *testing.T) {
	needSh(t)
	d := prepareDesktop(t)
	script := filepath.Join(d, "iwato-desktop")
	os.WriteFile(script, []byte("#!/bin/sh\ntrap '' TERM\n: > "+d+"/ready\nwhile :; do sleep 1; done\n"), 0o755)
	c := exec.Command("/bin/sh", script)
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { c.Wait(); close(done) }()
	t.Cleanup(func() { syscall.Kill(c.Process.Pid, syscall.SIGKILL) })
	waitUntil(t, "the trap", func() bool { return fileExists(filepath.Join(d, "ready")) })
	pf := filepath.Join(d, ".cache", "iwato-desktop.pid")
	os.MkdirAll(filepath.Dir(pf), 0o700)
	os.WriteFile(pf, []byte(strconv.Itoa(c.Process.Pid)+"\n"), 0o600)
	r := runCLI(t, d, []string{"stop"}, "IWATO_STOP_WAIT=3")
	if r.code != 0 || !strings.Contains(r.out, "killed") {
		t.Fatalf("%+v", r)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("still running")
	}
	if fileExists(pf) {
		t.Error("the pidfile is left behind")
	}
}

// Xvfb starts xkbcomp in a child that calls setgid(getgid()) and
// setuid(getuid()); the sandbox's seccomp filter makes both fail, so the
// session is started with the shim preloaded (root cause: docs/dev/gpu.md).
func TestDesktopScriptPreloadsTheSetuidShim(t *testing.T) {
	needSh(t)
	d := prepareDesktop(t)
	stub := "#!/bin/sh\necho \"PRELOAD=[${LD_PRELOAD:-}]\"\n"
	if err := os.WriteFile(filepath.Join(d, "bin", "selkies-session"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	lib := filepath.Join(d, "libiwato-noid.so")
	if err := os.WriteFile(lib, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := runDesktopIn(t, d, "IWATO_DXG=/nonexistent/dxg", "IWATO_NOID_LIB="+lib, "LD_PRELOAD=/other.so")
	if !strings.Contains(out, "PRELOAD=["+lib+" /other.so]") {
		t.Errorf("the shim is not preloaded ahead of an existing LD_PRELOAD:\n%s", out)
	}
	out = runDesktopIn(t, d, "IWATO_DXG=/nonexistent/dxg", "IWATO_NOID_LIB="+filepath.Join(d, "missing.so"))
	if !strings.Contains(out, "PRELOAD=[]") || !strings.Contains(out, "not found") {
		t.Errorf("a missing shim must be reported and not preloaded:\n%s", out)
	}
}

// The shim is built in the image, shipped as source, and the doctor script is
// installed.
func TestSandboxImageBuildsTheSetuidShim(t *testing.T) {
	b, err := imageFiles.ReadFile("sandboximage/Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"COPY desktop/iwato-noid.c", "gcc -shared -fPIC", "/usr/local/lib/iwato/libiwato-noid.so", "build-essential", "desktop: cannot build the setuid shim", "iwato-desktop-doctor"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("Dockerfile lacks %q", want)
		}
	}
	c, err := imageFiles.ReadFile("sandboximage/desktop/iwato-noid.c")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"int setuid(", "int setgid(", "RTLD_NEXT"} {
		if !strings.Contains(string(c), want) {
			t.Errorf("iwato-noid.c lacks %q", want)
		}
	}
}

// A terminal exports BROWSER=iwato-browser (it writes to /dev/tty); the desktop
// session has no controlling terminal, so the session gets Firefox instead, and
// turns off Firefox's own sandbox, which cannot be set up under Landlock and
// seccomp. Root cause of "Failed to execute default Web Browser":
// docs/dev/gpu.md.
func TestDesktopScriptUsesFirefoxNotIwatoBrowser(t *testing.T) {
	needSh(t)
	d := prepareDesktop(t)
	stub := "#!/bin/sh\necho \"BROWSER=[$BROWSER] SESSION=[$IWATO_DESKTOP_SESSION] C=[$MOZ_DISABLE_CONTENT_SANDBOX] G=[$MOZ_DISABLE_GMP_SANDBOX] R=[$MOZ_DISABLE_RDD_SANDBOX] S=[$MOZ_DISABLE_SOCKET_PROCESS_SANDBOX]\"\n"
	if err := os.WriteFile(filepath.Join(d, "bin", "selkies-session"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	out := runDesktopIn(t, d, "IWATO_DXG=/nonexistent/dxg", "BROWSER=/usr/local/bin/iwato-browser")
	want := "BROWSER=[firefox] SESSION=[1] C=[1] G=[1] R=[1] S=[1]"
	if !strings.Contains(out, want) {
		t.Errorf("the session must get %s:\n%s", want, out)
	}
}

// /etc/iwato/shell/envs: iwato-browser in the web terminal, Firefox in the
// terminals of the desktop session.
func TestShellEnvsPicksTheBrowserBySession(t *testing.T) {
	needSh(t)
	d := t.TempDir()
	if err := writeImageContext(d); err != nil {
		t.Fatal(err)
	}
	envs := filepath.Join(d, "sandboximage", "etc", "shell", "envs")
	run := func(extra ...string) string {
		c := exec.Command("bash", "-c", `. "$1"; printf %s "$BROWSER"`, "x", envs)
		c.Env = append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + d}, extra...)
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		return string(out)
	}
	if got := run(); got != "/usr/local/bin/iwato-browser" {
		t.Errorf("web terminal BROWSER = %q", got)
	}
	if got := run("IWATO_DESKTOP_SESSION=1"); got != "firefox" {
		t.Errorf("desktop BROWSER = %q", got)
	}
}

// The image makes Firefox the default browser for everything that does not read
// $BROWSER, and the sandbox may use /dev/shm (Firefox's shared memory).
func TestSandboxImageSetsFirefoxAsTheDefaultBrowser(t *testing.T) {
	b, err := imageFiles.ReadFile("sandboximage/Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"update-alternatives --install /usr/bin/x-www-browser x-www-browser /usr/bin/firefox",
		"update-alternatives --install /usr/bin/gnome-www-browser gnome-www-browser /usr/bin/firefox",
		"WebBrowser=firefox", "/etc/xdg/xfce4/helpers.rc",
		"x-scheme-handler/http=%s", "x-scheme-handler/https=%s", "text/html=%s", "/etc/xdg/mimeapps.list",
		"/usr/share/xfce4/helpers/firefox.desktop", "desktop: Mozilla's package installed no firefox*.desktop",
	} {
		if !strings.Contains(string(b), want) {
			t.Errorf("Dockerfile lacks %q", want)
		}
	}
	if strings.Index(string(b), "x-scheme-handler/http=") < strings.Index(string(b), "install -y --no-install-recommends firefox") {
		t.Error("the default browser is set before Firefox is installed")
	}
	w := newSandbox("dev")
	if !strings.Contains(strings.Join(sandboxPolicy(w).Filesystem.ReadWrite, " "), "/dev/shm") {
		t.Error("/dev/shm is not writable")
	}
	a, _ := imageFiles.ReadFile("sandboximage/AGENTS.md")
	for _, want := range []string{"iwato-browser", "Firefox"} {
		if !strings.Contains(string(a), want) {
			t.Errorf("sandboximage/AGENTS.md lacks %q", want)
		}
	}
}
