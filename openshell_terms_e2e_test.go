package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	v1 "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1"

	"iwato/internal/dav"
	"iwato/internal/hostterm"
	"iwato/internal/openshell/client"
	"iwato/internal/termd"
)

// TestOpenShellTerminalsSurviveRestartE2E checks, against e2e.sh's gateway
// (MODE=tls), that the terminals a sandbox's daemon (iwato-entrypoint)
// keeps outlive Iwato: a new Iwato finds them, shows what they showed,
// lists, kills them, and stopping the sandbox ends the daemon. A sandbox
// without the daemon keeps its terminals as before (its main process and
// execs) and its files on OpenShell's SFTP (the workspace); one made before
// the image carried the daemon runs it from the legacy path, where the
// driver places it on each start.
func TestOpenShellTerminalsSurviveRestartE2E(t *testing.T) {
	addr := os.Getenv("OPENSHELL_E2E_ADDR")
	if addr == "" {
		t.Skip("run by internal/openshell/wslc/e2e.sh")
	}
	tlsDir := os.Getenv("OPENSHELL_E2E_TLS")
	ca, cert, key := filepath.Join(tlsDir, "ca.crt"), filepath.Join(tlsDir, "client", "tls.crt"), filepath.Join(tlsDir, "client", "tls.key")
	c := defaultConfig()
	c.WSLC = os.Getenv("OPENSHELL_E2E_WSLC")
	b, err := NewEngine(c)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := client.Dial(addr, ca, cert, key)
	if err != nil {
		t.Fatal(err)
	}
	sdk, err := v1.NewClient(v1.Config{Address: addr, TLS: &v1.TLSConfig{CAFile: ca, CertFile: cert, KeyFile: key}})
	if err != nil {
		t.Fatal(err)
	}
	// A new Iwato each: nothing but the gateway in common.
	iwato := func() *OpenShell {
		o := &OpenShell{e: b, ready: make(chan struct{}), sdk: sdk, conn: conn, env: termEnv, terms: map[string]map[string]*hostterm.Session{}, imageDaemon: withDaemon, legacyPlaced: true}
		close(o.ready)
		return o
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	if err := enablePolicyAdvisor(ctx, sdk); err != nil {
		t.Fatal("policy advisor:", err)
	}
	attach := func(o *OpenShell, w Sandbox, ref TermRef) (Terminal, error) {
		c, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		return o.Attach(c, w, ref, 80, 24)
	}
	// Start places the agents' files in this machine's homeDir (the home
	// volume here): only the ones this test made go.
	for _, p := range []string{filepath.Join(homeDir, ".claude/CLAUDE.md"), filepath.Join(homeDir, ".codex/AGENTS.md")} {
		if _, err := os.Lstat(p); err != nil {
			t.Cleanup(func() { os.Remove(p); os.Remove(filepath.Dir(p)) })
		}
	}
	// The daemon's process (the fake runs sandboxes on this machine).
	daemonRuns := func(w Sandbox) bool {
		return exec.Command("pgrep", "-f", "iwato-(entrypoint|term) serve -key "+w.Name+"( |$)").Run() == nil
	}

	o := iwato()
	w := newSandbox("iwato-e2e-terms")
	t.Cleanup(func() { _ = iwato().Delete(context.Background(), w, true) })
	if err := o.Start(ctx, w); err != nil {
		t.Fatal("start:", err)
	}
	if !daemonRuns(w) {
		t.Fatal("no terminal daemon in the new sandbox")
	}
	mainTerminal, err := attach(o, w, TermRef{Name: mainTerm})
	if err != nil {
		t.Fatal("attach main:", err)
	}
	mainView := viewTerminal(t, mainTerminal, 80, 24)
	if err := mainView.Input([]byte("echo MAIN-$((6*7))\r")); err != nil {
		t.Fatal(err)
	}
	if !showsText(t, mainView, "MAIN-42", 30*time.Second) {
		t.Fatal("main did not run the command")
	}
	two, err := attach(o, w, TermRef{Name: "2", New: true})
	if err != nil {
		t.Fatal("attach 2:", err)
	}
	twoView := viewTerminal(t, two, 80, 24)
	if err := twoView.Input([]byte("echo TWO-$((3+4))\r")); err != nil {
		t.Fatal(err)
	}
	if !showsText(t, twoView, "TWO-7", 30*time.Second) {
		t.Fatal("terminal 2 did not run the command")
	}

	// Iwato restarts: its sessions go (their execs end); the daemon keeps
	// the terminals.
	o.termMu.Lock()
	held := maps2slice(o.terms[w.Name])
	o.termMu.Unlock()
	for _, s := range held {
		s.Close()
		<-s.Done()
	}
	o = iwato()
	// What starting Iwato resumes: the screens come back for the notifier.
	o.ResumeTerminals(ctx, func() []Sandbox { return []Sandbox{w} })
	resumed := false
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline) && !resumed; time.Sleep(time.Second) {
		scr := o.TerminalScreens(w)
		resumed = strings.Contains(strings.Join(scr["2"].lines, "\n"), "TWO-7") && strings.Contains(strings.Join(scr[mainTerm].lines, "\n"), "MAIN-42")
	}
	if !resumed {
		t.Fatalf("the resumed screens: %+v", o.TerminalScreens(w))
	}
	names, _, _, err := o.Terminals(ctx, w)
	if err != nil || !slices.Equal(names, []string{mainTerm, "2"}) {
		t.Fatal("terminals after a restart:", names, err)
	}
	if at, ok := o.TerminalStarted(w, "2"); !ok || time.Since(at) > 5*time.Minute {
		t.Fatal("terminal 2's start:", at, ok)
	}
	if r := o.SandboxRestartTerms(w, names); len(r) != 0 {
		t.Fatal("terminals that would need a restart of the sandbox:", r)
	}
	// Dropped once more, so that attaching replays the output.
	o.termMu.Lock()
	held = maps2slice(o.terms[w.Name])
	delete(o.terms, w.Name)
	o.termMu.Unlock()
	for _, s := range held {
		s.Close()
	}
	mainTerminal, err = attach(o, w, TermRef{Name: mainTerm})
	if err != nil {
		t.Fatal("attach main after a restart:", err)
	}
	if !showsText(t, viewTerminal(t, mainTerminal, 80, 24), "MAIN-42", 30*time.Second) {
		t.Fatal("main lost its screen")
	}
	two, err = attach(o, w, TermRef{Name: "2"})
	if err != nil {
		t.Fatal("attach 2 after a restart:", err)
	}
	if !showsText(t, viewTerminal(t, two, 80, 24), "TWO-7", 30*time.Second) {
		t.Fatal("terminal 2 lost its screen")
	}
	// Killing ends it in the sandbox. Main is a terminal like the others:
	// it leaves the list, and attaching it does not bring it back while
	// another runs.
	if err := o.KillTerminal(ctx, w, mainTerm); err != nil {
		t.Fatal("kill main:", err)
	}
	if names, _, _, err := o.Terminals(ctx, w); err != nil || !slices.Equal(names, []string{"2"}) {
		t.Fatal("terminals after killing main:", names, err)
	}
	if _, err := attach(o, w, TermRef{Name: mainTerm}); err == nil || !strings.Contains(err.Error(), "終了しています") {
		t.Fatal("attaching the killed main next to 2:", err)
	}
	if err := o.KillTerminal(ctx, w, "2"); err != nil {
		t.Fatal("kill 2:", err)
	}
	if _, err := attach(o, w, TermRef{Name: "2"}); err == nil || !strings.Contains(err.Error(), "終了しています") {
		t.Fatal("attaching the killed terminal:", err)
	}
	// With none left the list shows main, and attaching starts it again.
	if names, _, _, err := o.Terminals(ctx, w); err != nil || !slices.Equal(names, []string{mainTerm}) {
		t.Fatal("terminals after the kills:", names, err)
	}
	fresh, err := attach(o, w, TermRef{Name: mainTerm})
	if err != nil {
		t.Fatal("attach main after the kill:", err)
	}
	freshView := viewTerminal(t, fresh, 80, 24)
	if err := freshView.Input([]byte("echo FRESH-$((1+1))\r")); err != nil {
		t.Fatal(err)
	}
	if !showsText(t, freshView, "FRESH-2", 30*time.Second) {
		t.Fatal("no fresh main")
	}
	if text := freshView.Text(); strings.Contains(text, "MAIN-42") {
		t.Fatalf("the killed main's screen came back:\n%s", text)
	}
	if err := o.Stop(ctx, w); err != nil {
		t.Fatal("stop:", err)
	}
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline) && daemonRuns(w); time.Sleep(time.Second) {
	}
	if daemonRuns(w) {
		t.Fatal("the terminal daemon outlived the sandbox")
	}
	if err := o.Delete(ctx, w, true); err != nil {
		t.Fatal("delete:", err)
	}

	// A sandbox without the daemon (an image without it, or one that runs
	// since before an update): its main process and execs, as before.
	old := iwato()
	old.imageDaemon, old.termPath = func(context.Context, string) bool { return false }, "/nonexistent/iwato-term"
	l := newSandbox("iwato-e2e-legacy")
	t.Cleanup(func() {
		_ = iwato().Delete(context.Background(), l, true)
		_ = exec.Command("pkill", "-f", "iwato-(entrypoint|term) serve -key "+l.Name+"( |$)").Run()
	})
	if err := old.Start(ctx, l); err != nil {
		t.Fatal("start the old sandbox:", err)
	}
	legacyMain, err := attach(old, l, TermRef{Name: mainTerm})
	if err != nil {
		t.Fatal("attach the old sandbox's main:", err)
	}
	if d, known := old.daemons[l.Name]; !known || d.ok {
		t.Fatal("the old sandbox's terminals went through a daemon")
	}
	legacyView := viewTerminal(t, legacyMain, 80, 24)
	if err := legacyView.Input([]byte("echo LEGACY-$((5+5))\r")); err != nil {
		t.Fatal(err)
	}
	if !showsText(t, legacyView, "LEGACY-10", 30*time.Second) {
		t.Fatal("the old sandbox's main did not run the command")
	}
	extra, err := attach(old, l, TermRef{Name: "2", New: true})
	if err != nil {
		t.Fatal("attach an old sandbox's extra terminal:", err)
	}
	extraView := viewTerminal(t, extra, 80, 24)
	if err := extraView.Input([]byte("echo EXTRA-$((2*3))\r")); err != nil {
		t.Fatal(err)
	}
	if !showsText(t, extraView, "EXTRA-6", 30*time.Second) {
		t.Fatal("the old sandbox's extra terminal did not run the command")
	}
	if names, _, _, err := old.Terminals(ctx, l); err != nil || !slices.Equal(names, []string{mainTerm, "2"}) {
		t.Fatal("the old sandbox's terminals:", names, err)
	}
	if r := old.SandboxRestartTerms(l, []string{mainTerm, "2"}); !slices.Equal(r, []string{mainTerm}) {
		t.Fatal("what only a restart of the old sandbox renews:", r)
	}
	if err := old.KillTerminal(ctx, l, mainTerm); err == nil {
		t.Fatal("killed an old sandbox's main process")
	}
	// Its files are OpenShell's SFTP's, at the workspace.
	if root, err := old.FilesRoot(ctx, l); err != nil || root != (FilesRoot{Path: "/sandbox"}) {
		t.Fatal("the old sandbox's files:", root, err)
	}
	if tr, err := old.DialFiles(ctx, l); err != nil {
		t.Fatal("the old sandbox's files:", err)
	} else if _, ok := tr.(client.SFTP); !ok {
		tr.Close()
		t.Fatalf("the old sandbox's files are not OpenShell's SFTP: %T", tr)
	} else {
		tr.Close()
	}
	// The same sandbox once the daemon is there (here it always is: the
	// fake shares this machine's files): an attach starts the daemon.
	upgraded := iwato()
	three, err := attach(upgraded, l, TermRef{Name: "3", New: true})
	if err != nil {
		t.Fatal("attach through a started daemon:", err)
	}
	threeView := viewTerminal(t, three, 80, 24)
	if err := threeView.Input([]byte("echo THREE-$((1+2))\r")); err != nil {
		t.Fatal(err)
	}
	if !showsText(t, threeView, "THREE-3", 30*time.Second) || !daemonRuns(l) {
		t.Fatal("no terminal through a daemon that an attach started")
	}
	if err := upgraded.Stop(ctx, l); err != nil {
		t.Fatal("stop the old sandbox:", err)
	}
	if err := upgraded.Delete(ctx, l, true); err != nil {
		t.Fatal("delete the old sandbox:", err)
	}

	// A sandbox an older Iwato made before the image carried the daemon:
	// its main process runs it from the legacy path (where that Iwato's
	// driver put it on creation; here e2e.sh did), and the driver places
	// the current one there on each start (e2e.sh's -legacy-term).
	maker := iwato()
	maker.termPath = termd.LegacyPath
	lw := newSandbox("iwato-e2e-oldmain")
	t.Cleanup(func() { _ = iwato().Delete(context.Background(), lw, true) })
	if err := maker.Start(ctx, lw); err != nil {
		t.Fatal("start the sandbox with the legacy daemon:", err)
	}
	cur := iwato()
	for round := range 2 {
		lm, err := attach(cur, lw, TermRef{Name: mainTerm})
		if err != nil {
			t.Fatalf("round %d: attach the legacy daemon's main: %v", round, err)
		}
		lv := viewTerminal(t, lm, 80, 24)
		if err := lv.Input([]byte("echo OLDMAIN-$((3+4))\r")); err != nil {
			t.Fatal(err)
		}
		if !showsText(t, lv, "OLDMAIN-7", 30*time.Second) {
			t.Fatalf("round %d: the legacy daemon's main did not run the command", round)
		}
		lm.Close()
		if d := cur.daemons[lw.Name]; !d.ok || d.bin != termd.LegacyPath || !d.has(termd.CapSFTP) || !d.restartUpdates {
			t.Fatalf("round %d: the legacy daemon: %+v", round, d)
		}
		// Its files are the daemon's SFTP's, at the home.
		if root, err := cur.FilesRoot(ctx, lw); err != nil || root != (FilesRoot{Path: homeDir, Home: true}) {
			t.Fatalf("round %d: the legacy sandbox's files: %v %v", round, root, err)
		}
		tr, err := cur.DialFiles(ctx, lw)
		if err != nil {
			t.Fatal(err)
		}
		sc, err := dav.NewClient(tr)
		if err != nil {
			tr.Close()
			t.Fatalf("round %d: the daemon's SFTP: %v", round, err)
		}
		fi, err := sc.Stat("/")
		sc.Close()
		tr.Close()
		if err != nil || !fi.IsDir() {
			t.Fatalf("round %d: the daemon's SFTP root: %v %v", round, fi, err)
		}
		// A start places the daemon again (the sandbox is stopped then).
		if err := cur.Stop(ctx, lw); err != nil {
			t.Fatal("stop the legacy sandbox:", err)
		}
		if err := cur.Start(ctx, lw); err != nil {
			t.Fatal("start the legacy sandbox again:", err)
		}
	}
}

func maps2slice(m map[string]*hostterm.Session) []*hostterm.Session {
	out := make([]*hostterm.Session, 0, len(m))
	for _, s := range m {
		out = append(out, s)
	}
	return out
}
