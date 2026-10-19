package main

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	v1 "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1"

	"iwato/internal/hostterm"
	"iwato/internal/openshell/client"
	"iwato/internal/termsync/termsynctest"
)

// TestOpenShellE2E drives the OpenShell runtime against the gateway that
// internal/openshell/wslc/e2e.sh runs (MODE=tls), with its fake wslc. It
// is skipped otherwise.
func TestOpenShellE2E(t *testing.T) {
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
	// As Iwato.exe with the standard image, which carries the daemon (here
	// this machine's /usr/local/lib/iwato, where e2e.sh puts it).
	o := &OpenShell{e: b, ready: make(chan struct{}), sdk: sdk, conn: conn, env: termEnv, terms: map[string]map[string]*hostterm.Session{}, imageDaemon: withDaemon}
	close(o.ready)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	// prepare turns the Policy Advisor on before any sandbox starts.
	if err := enablePolicyAdvisor(ctx, sdk); err != nil {
		t.Fatal("policy advisor:", err)
	}
	// As the page does: the request that attaches ends right after.
	attach := func(w Sandbox, ref TermRef, cols, rows int) (Terminal, error) {
		c, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		return o.Attach(c, w, ref, cols, rows)
	}
	w := newSandbox("iwato-e2e")
	// The fake wslc runs the sandbox on this machine and mounts no volume:
	// this machine's homeDir (e2e.sh makes it when missing) stands for the
	// home volume, where Iwato places the agents' files.
	if fi, err := os.Stat(homeDir); err != nil || !fi.IsDir() {
		t.Fatalf("no %s, which stands for the home volume here (e2e.sh makes it): %v", homeDir, err)
	}
	claudeMD, codexMD := filepath.Join(homeDir, ".claude/CLAUDE.md"), filepath.Join(homeDir, ".codex/AGENTS.md")
	for _, p := range []string{claudeMD, codexMD} {
		if _, err := os.Lstat(p); err == nil {
			t.Fatalf("%s is there (from an earlier run, or the owner's own): this test would change it", p)
		}
	}
	// Terminals start in ~/work, made when missing: removed when this test
	// made it (and left it empty).
	work := filepath.Join(homeDir, workDir)
	_, workErr := os.Lstat(work)
	t.Cleanup(func() {
		for _, p := range []string{claudeMD, codexMD, filepath.Dir(claudeMD), filepath.Dir(codexMD)} {
			os.Remove(p)
		}
		if workErr != nil {
			os.Remove(work)
		}
	})
	if err := o.Start(ctx, w); err != nil {
		t.Fatal("start:", err)
	}
	if s := o.Status(ctx, w); s.State != "running" {
		t.Fatalf("status after start: %+v", s)
	}
	// The main terminal is one of the terminal daemon's (iwato-entrypoint, the
	// sandbox's main process), started by the first attach.
	term, err := attach(w, TermRef{Name: mainTerm}, 80, 24)
	if err != nil {
		t.Fatal("attach:", err)
	}
	view := viewTerminal(t, term, 80, 24)
	// The image's login shell in ~/work (the workspace, the image's
	// WORKDIR /sandbox, is outside the home Iwato creates the sandbox with).
	if err := view.Input([]byte("echo E2E-$((40+2)) $PWD $HOME $(id -un)\r")); err != nil {
		t.Fatal(err)
	}
	if !showsText(t, view, "E2E-42 "+work+" "+homeDir+" dev", 30*time.Second) {
		t.Fatal("the main terminal did not run the command in ~/work")
	}
	term.Close()
	// Reattaching draws what the terminal showed.
	term, err = attach(w, TermRef{Name: mainTerm}, 100, 30)
	if err != nil {
		t.Fatal("reattach:", err)
	}
	reattached := viewTerminal(t, term, 100, 30)
	if !showsText(t, reattached, "E2E-42 "+homeDir, 10*time.Second) {
		t.Fatal("reattaching lost the screen")
	}
	// Something that shows the sizes it is told about, for the next attach
	// (sh -c: the foreground process group is the one SIGWINCH reaches).
	if err := reattached.Input([]byte(`sh -c 'trap "echo WINCH \$(stty size)" WINCH; while :; do sleep 0.1; done'` + "\r")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Second)
	term.Close()
	// A new iwato (its session gone) gets the screen back from the output
	// the daemon replays on attach.
	o.termMu.Lock()
	held := o.terms[w.Name][mainTerm]
	delete(o.terms[w.Name], mainTerm)
	o.termMu.Unlock()
	held.Close()
	<-held.Done()
	// The same size as before: the shell's program still gets a SIGWINCH
	// (a row shorter and back), so that it draws its whole screen again.
	term, err = attach(w, TermRef{Name: mainTerm}, 100, 30)
	if err != nil {
		t.Fatal("attach after the session:", err)
	}
	fresh := viewTerminal(t, term, 100, 30)
	if !showsText(t, fresh, "E2E-42 "+homeDir, 30*time.Second) {
		t.Fatal("the replayed output did not restore the screen")
	}
	if !showsText(t, fresh, "WINCH 30 100", 10*time.Second) {
		t.Fatal("attaching did not make the main process redraw")
	}
	if err := fresh.Input([]byte("\x03")); err != nil {
		t.Fatal(err)
	}
	term.Close()
	// Another terminal is a shell of its own.
	other, err := attach(w, TermRef{Name: "2", New: true}, 80, 24)
	if err != nil {
		t.Fatal("attach 2:", err)
	}
	// The image carries the Policy Advisor guide (NVIDIA/OpenShell#3698;
	// here e2e.sh puts it on this machine).
	otherView := viewTerminal(t, other, 80, 24)
	if err := otherView.Input([]byte("echo OTHER-$((1+1)) $PWD $HOME $(test -r /etc/openshell/skills/policy_advisor.md && echo GUIDE)\r")); err != nil {
		t.Fatal(err)
	}
	if !showsText(t, otherView, "OTHER-2 "+work+" "+homeDir+" GUIDE", 30*time.Second) {
		t.Fatal("the second terminal is not in ~/work, or the Policy Advisor guide is missing")
	}
	// Start placed the agents' files, the sandbox user's, pointing at
	// /AGENTS.md.
	if err := otherView.Input([]byte("echo AGENT-FILES $(stat -c %u:%a ~/.claude ~/.claude/CLAUDE.md) $(cat ~/.claude/CLAUDE.md) $(stat -c %u ~/.codex) $(readlink ~/.codex/AGENTS.md)\r")); err != nil {
		t.Fatal(err)
	}
	if !showsText(t, otherView, "AGENT-FILES 1000:700 1000:644 @/AGENTS.md 1000 /AGENTS.md", 30*time.Second) {
		t.Fatal("the agents' files are missing, wrong or not the sandbox user's")
	}
	other.Close()
	names, _, _, err := o.Terminals(ctx, w)
	if err != nil || !slices.Equal(names, []string{mainTerm, "2"}) {
		t.Fatal("terminals:", names, err)
	}
	if err := o.KillTerminal(ctx, w, "2"); err != nil {
		t.Fatal(err)
	}

	// The sandbox's draft reads as the proposal view. Proposals themselves
	// cannot be made here: the fake wslc gives the sandbox neither its own
	// network nor PID namespace, so neither policy.local (policy DNS) nor a
	// blocked connection (binary identity) reaches the Policy Advisor.
	// TestProposalDecisionsFollowTheDraft covers the decisions.
	v, err := o.Proposals(ctx, w)
	if err != nil {
		t.Fatal("proposal view:", err)
	}
	if !v.Available || len(v.Pending) != 0 {
		t.Fatalf("proposal view of a new sandbox: %+v", v)
	}
	// The shell ending (here with a failure, as after Ctrl-D following a
	// failed command) ends only the main terminal: the sandbox runs on,
	// and attaching gives a fresh shell.
	term, err = attach(w, TermRef{Name: mainTerm}, 80, 24)
	if err != nil {
		t.Fatal("attach to exit:", err)
	}
	exitView := viewTerminal(t, term, 80, 24)
	if err := exitView.Input([]byte("exit 3\r")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-term.(*hostterm.Client).Exited():
	case <-time.After(30 * time.Second):
		t.Fatal("the main terminal did not end with its shell")
	}
	term.Close()
	if s := o.Status(ctx, w); s.State != "running" {
		t.Fatalf("status after the shell exited: %+v", s)
	}
	term, err = attach(w, TermRef{Name: mainTerm}, 80, 24)
	if err != nil {
		t.Fatal("attach after the shell exited:", err)
	}
	again := viewTerminal(t, term, 80, 24)
	if err := again.Input([]byte("echo AGAIN-$((2+3))\r")); err != nil {
		t.Fatal(err)
	}
	if !showsText(t, again, "AGAIN-5", 30*time.Second) {
		t.Fatal("no fresh shell after the shell exited")
	}
	term.Close()
	if err := o.Stop(ctx, w); err != nil {
		t.Fatal("stop:", err)
	}
	if s := o.Status(ctx, w); s.State != "stopped" {
		t.Fatalf("status after stop: %+v", s)
	}
	// The owner's own files: kept, and a removed one comes back.
	if err := os.WriteFile(claudeMD, []byte("mine\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	os.Remove(codexMD)
	if err := o.Start(ctx, w); err != nil {
		t.Fatal("start again:", err)
	}
	if b, err := os.ReadFile(claudeMD); err != nil || string(b) != "mine\n" {
		t.Fatalf("the owner's CLAUDE.md after a start: %q %v", b, err)
	}
	if l, err := os.Readlink(codexMD); err != nil || l != "/AGENTS.md" {
		t.Fatalf("~/.codex/AGENTS.md after a start: %q %v", l, err)
	}
	// The home's volume, only.
	vs, err := b.volumes(ctx)
	paths := []string{}
	for _, v := range vs {
		if v.CreatedAt == "" || v != (Volume{Name: persistVolume(c, w, v.Path), Sandbox: w.Name, Path: v.Path, CreatedAt: v.CreatedAt}) {
			t.Fatalf("volume list: %+v", vs)
		}
		paths = append(paths, v.Path)
	}
	slices.Sort(paths)
	if err != nil || !slices.Equal(paths, []string{homeDir}) {
		t.Fatalf("volume list: %+v %v", vs, err)
	}
	if err := o.Delete(ctx, w, true); err != nil {
		t.Fatal("delete:", err)
	}
	if s := o.Status(ctx, w); s.State != "not-created" {
		t.Fatalf("status after delete: %+v", s)
	}
	for _, p := range []string{homeDir} {
		if d, _ := b.inspect(ctx, "volume", persistVolume(c, w, p)); d != nil {
			t.Fatal("purge kept the volume of", p)
		}
	}
}

// withDaemon stands for an image of the convention (it carries the daemon).
func withDaemon(context.Context, string) bool { return true }

// viewTerminal attaches a termsync client to t at cols×rows, as a browser.
func viewTerminal(t *testing.T, term Terminal, cols, rows int) *termsynctest.Client {
	t.Helper()
	v, err := termsynctest.Start(term, cols, rows)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// showsText reports whether want shows on the terminal within limit.
func showsText(t *testing.T, v *termsynctest.Client, want string, limit time.Duration) bool {
	t.Helper()
	text, err := v.WaitFor(want, limit)
	if err != nil {
		t.Logf("waiting for %q: %v; the terminal shows:\n%s", want, err, strings.TrimSpace(text))
	}
	return err == nil
}
