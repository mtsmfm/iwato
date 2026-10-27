//go:build uitest

package main

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"iwato/internal/dav"
	"iwato/internal/termsync"
	"net"
	"path"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	v1 "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1"
)

// Fake is the runtime of the fake backend the browser checks drive (serve
// -fake, build tag uitest; fake_hooks.go): sandboxes and terminals in memory,
// with no command and no sandbox run. Most server tests use it too.
type Fake struct {
	mu      sync.Mutex
	started map[string]bool
	stopped map[string]bool
	history map[string]string // by sandbox/terminal
	dirs    map[string]string // the working directories cd set, by sandbox/terminal
	// terms are each sandbox's running terminals and their attachments.
	terms map[string]map[string][]*fakeTerm
	// renewed are terminals restarted once; before that, "2" shows as one
	// that a restart would update.
	renewed map[string]bool
	// drafts are each sandbox's proposals as OpenShell keeps them
	// (fake_policy.go).
	drafts map[string][]v1.PolicyChunk
	// policies are each sandbox's network policy revisions (fake_policy.go).
	policies map[string][]fakeRevision
	// config names the volumes; volumes are the persisted paths' (by name).
	config  Config
	volumes map[string]Volume
	// prov are the fake providers (fake_providers.go); termStarted is
	// when each terminal started ("sandbox/terminal").
	prov        fakeProviders
	termStarted map[string]time.Time
	// files are each sandbox's files (fake_files.go).
	files map[string]*dav.MemServer
	// shell runs OpenShell's policy code (fake_policy.go).
	shellOnce sync.Once
	shell     *OpenShell
	// termEnded is told when a terminal ends (autostop.go).
	termEnded func(sandbox string)
	// latency is added to each terminal connection's round trip.
	latency time.Duration
	// seedPolicy replaces dev's current rules (-fake-policy).
	seedPolicy map[string]v1.NetworkPolicyRule
	// gatewayLatency is added to each gateway call; calls counts them
	// (gatewayCall).
	gatewayLatency time.Duration
	callsMu        sync.Mutex
	calls          map[string]int
	// noGPU: the engine offers no GPU.
	noGPU bool
	// oldRuntime: sandboxes created with older runtime images, until
	// deleted (RecreateToUpdate).
	oldRuntime map[string]bool
}

func NewFake() *Fake {
	return &Fake{started: map[string]bool{}, stopped: map[string]bool{}, history: map[string]string{}, terms: map[string]map[string][]*fakeTerm{}, renewed: map[string]bool{}}
}
func (d *Fake) GPUAvailable(context.Context) bool { return !d.noGPU }
func (d *Fake) Status(_ context.Context, w Sandbox) Status {
	d.mu.Lock()
	defer d.mu.Unlock()
	s := "not-created"
	if d.started[w.Name] {
		s = "running"
	} else if d.stopped[w.Name] {
		s = "stopped"
	}
	return Status{Name: w.Name, State: s, RecreateToUpdate: s != "not-created" && d.oldRuntime[w.Name]}
}

// PinsRuntime: as the WSLc driver.
func (d *Fake) PinsRuntime() bool { return true }
func (d *Fake) Start(_ context.Context, w Sandbox) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, p := range w.volumePaths() {
		if n := persistVolume(d.config, w, p); d.volumes[n].Name == "" {
			d.addVolume(Volume{Name: n, Sandbox: w.Name, Path: p, CreatedAt: time.Now().UTC().Format(time.RFC3339)})
		}
	}
	// Created with the configuration's providers, as OpenShell.
	if !d.started[w.Name] && !d.stopped[w.Name] {
		if d.prov.attached == nil {
			d.prov.attached = map[string][]string{}
		}
		d.prov.attached[w.Name] = slices.Clone(w.Providers)
	}
	if !d.started[w.Name] {
		// A new main process.
		delete(d.terms, w.Name)
		d.markStarted(w.Name, mainTerm)
	}
	d.started[w.Name] = true
	delete(d.stopped, w.Name)
	return nil
}

// Stop keeps the fake history, like a stopped sandbox keeps its files.
func (d *Fake) Stop(_ context.Context, w Sandbox) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.started[w.Name] {
		delete(d.started, w.Name)
		d.stopped[w.Name] = true
		// Stopping ends the shells; the next start has only main.
		for name := range d.terms[w.Name] {
			if name != mainTerm {
				delete(d.history, w.Name+"/"+name)
			}
		}
		delete(d.terms, w.Name)
	}
	return nil
}
func (d *Fake) Attach(_ context.Context, w Sandbox, ref TermRef, cols, rows int) (Terminal, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.started[w.Name] {
		return nil, fmt.Errorf("sandbox is not started")
	}
	if !validTermName(ref.Name) {
		return nil, fmt.Errorf("invalid terminal name")
	}
	terms := d.terms[w.Name]
	if terms == nil {
		terms = map[string][]*fakeTerm{}
		d.terms[w.Name] = terms
	}
	if _, ok := terms[ref.Name]; !ok {
		if !ref.New && ref.Name != mainTerm {
			return nil, fmt.Errorf("this terminal session has ended")
		}
		if len(terms) >= 8 {
			return nil, fmt.Errorf("at most 8 terminal sessions can run at once")
		}
		terms[ref.Name] = nil
		d.markStarted(w.Name, ref.Name)
	}
	r, p := io.Pipe()
	prompt := w.Name
	if ref.Name != mainTerm {
		prompt += ":" + ref.Name
	}
	t := &fakeTerm{r: r, p: p, d: d, id: prompt, sandbox: w.Name, name: ref.Name, key: w.Name + "/" + ref.Name, done: make(chan struct{})}
	terms[ref.Name] = append(terms[ref.Name], t)
	history := d.history[t.key]
	go func() {
		t.output("\x1b[36mIWATO · FAKE\x1b[0m\r\n実際のシェルではありません。help / echo / clear を試せます。\r\n" + history + "\r\n" + w.Name + " $ ")
	}()
	conn := termsync.Wrap(t, t.Resize, cols, rows)
	if d.latency > 0 {
		conn = delayed(conn, d.latency)
	}
	return &fakeAttach{Conn: conn}, nil
}

// fakeAttach is an attachment to a fake terminal: its own termsync hub per attach.
type fakeAttach struct{ net.Conn }

func (*fakeAttach) Resize(int, int) error { return nil }

type fakeTerm struct {
	r        *io.PipeReader
	p        *io.PipeWriter
	d        *Fake
	id, line string
	// sandbox and name identify the terminal; key its history.
	sandbox, name, key string
	mu                 sync.Mutex
	cols, rows         int           // as the attached client sized it
	app                *ccscroll     // the full-screen app running, if any
	spin               chan struct{} // stops cclink's status row or tui
	spinEnd            string        // written when spin stops
	once               sync.Once
	done               chan struct{}
}

func (t *fakeTerm) Read(b []byte) (int, error) { return t.r.Read(b) }
func (t *fakeTerm) output(s string)            { _, _ = io.WriteString(t.p, s) }
func (t *fakeTerm) Write(b []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	select {
	case <-t.done:
		return 0, io.ErrClosedPipe
	default:
	}
	if t.spin != nil {
		close(t.spin)
		t.spin = nil
		t.output(t.spinEnd)
	}
	if t.app != nil {
		t.scrollInput(b)
		return len(b), nil
	}
	// Mouse reports (SGR, after the "mouse" command enables them) are shown
	// instead of typed, to check wheel/touch scrolling like tmux would get.
	if strings.HasPrefix(string(b), "\x1b[<") {
		kind := "mouse"
		if strings.HasPrefix(string(b), "\x1b[<64;") {
			kind = "wheel-up"
		} else if strings.HasPrefix(string(b), "\x1b[<65;") {
			kind = "wheel-down"
		}
		t.output("\r\n[" + kind + "]\r\n" + t.id + " $ ")
		return len(b), nil
	}
	for _, c := range string(b) {
		switch c {
		case '\r', '\n':
			line := t.line
			t.line = ""
			response := ""
			switch {
			case line == "help":
				response = "FAKE only: help, echo TEXT, cd DIR, url, framed, osc8, cclink, ccbold, ccwrap, cctable [N], logo, lines, colors, diff, mouse, ccscroll, tui MS [LINES], clear, exit (or Ctrl-D)\r\n本物のプロセスやサンドボックスは作成しません。"
			case line == "lines":
				// History to scroll through (and fetch).
				var b strings.Builder
				for i := 1; i <= 300; i++ {
					fmt.Fprintf(&b, "\x1b[3%dm%03d\x1b[0m 履歴の行です。 history line with some words to wrap on a narrow screen %d\r\n", i%7+1, i, i)
				}
				t.output(b.String())
				response = "300 lines"
			case line == "diff":
				// Rows with a background, as a diff shows them.
				response = "\x1b[48;5;52m367 -  if (cards.has(name)) selectCard(cards.get(name));   \x1b[0m\r\n" +
					"\x1b[48;5;22m367 +  // 作ったサンドボックスはすぐ起動する │ ─ ┼            \x1b[0m\r\n" +
					"\x1b[48;5;22m368 +  if (cards.has(name)) { selectCard(cards.get(name)); } \x1b[0m\r\n" +
					"369    } catch (error) {"
			case line == "colors":
				var b strings.Builder
				for i := 0; i < 16; i++ {
					fmt.Fprintf(&b, "\x1b[48;5;%dm  \x1b[0m", i)
				}
				b.WriteString("\r\n\x1b[1mbold\x1b[0m \x1b[3mitalic\x1b[0m \x1b[4munderline\x1b[0m \x1b[7minverse\x1b[0m \x1b[9mstrike\x1b[0m \x1b[38;2;255;160;80mtruecolor\x1b[0m 全角 ⏺ ✻ 🙂\r\n")
				b.WriteString("╭────────────╮\r\n│ box │ 罫線 │\r\n╰────────────╯")
				response = b.String()
			case line == "exit":
				t.output("\r\nlogout\r\n")
				go t.d.endTerminal(t.sandbox, t.name)
				return len(b), nil
			case line == "url":
				response = fakeLongURL
			case line == "logo":
				// Block elements as Claude Code draws its logo.
				response = "\x1b[38;5;174m ▐▛███▜▌\x1b[39m   Claude Code\r\n\x1b[38;5;174m▝▜█████▛▘\x1b[39m  Opus\r\n\x1b[38;5;174m  ▘▘ ▝▝\x1b[39m    /sandbox\r\n│ 要素 │ どこで渡すか │\r\n├──────┼──────────────┤\r\n│ OS・ツール │ イメージ (--from) │\r\n▀▄▌▐░▒▓▔▕▁▂▃▄▅▆▇█▉▊▋▌▍▎▏▖▗▘▙▚▛▜▝▞▟"
			case line == "osc8":
				// A URL as Claude Code prints it: each row its own hyperlink
				// (OSC 8, one id) to the whole URL.
				url := fakeLongURL + "&pad=" + strings.Repeat("y", max(t.cols, 20))
				width := max(t.cols-2, 20)
				var b strings.Builder
				for rest := url; rest != ""; {
					n := min(width, len(rest))
					fmt.Fprintf(&b, "\x1b]8;id=fake;%s\x07\x1b[38;5;208m%s\x1b[39m\x1b]8;;\x07\r\n", url, rest[:n])
					rest = rest[n:]
				}
				response = strings.TrimSuffix(b.String(), "\r\n")
			case line == "cclink":
				// Claude Code at work: Japanese, then a URL as an OSC 8
				// hyperlink in two SGR runs, inside a frame (the next row the
				// same URL as plain text), a status row redrawn until the next
				// key, and the cursor parked on the first URL.
				const u = "https://example.com/iwato/claude"
				link := "\x1b]8;;" + u + "\x07\x1b[38;5;153m" + u[:19] + "\x1b[4m" + u[19:] + "\x1b[24;39m\x1b]8;;\x07"
				t.output("\r\n╭──────────────────────────────────────────────╮\r\n│ 詳細は " + link + " を見てください │\r\n│ 参照: \x1b[38;5;153m" + u[:19] + "\x1b[1m" + u[19:] + "\x1b[0m        │\r\n╰──────────────────────────────────────────────╯\r\n\r\n" + t.id + " $ \x1b[4A\x1b[20G")
				stop := make(chan struct{})
				t.spin = stop
				t.spinEnd = fmt.Sprintf("\x1b[4B\x1b[%dG", len(t.id)+4)
				go func() {
					tick := time.NewTicker(40 * time.Millisecond)
					defer tick.Stop()
					for i := 0; ; i++ {
						select {
						case <-stop:
							return
						case <-t.done:
							return
						case <-tick.C:
							t.output(fmt.Sprintf("\x1b7\x1b[3B\r\x1b[38;5;174m%c\x1b[39m 考え中… %d\x1b8", []rune("✻✽✶✳")[i%4], i))
						}
					}
				}()
				return len(b), nil
			case line == "ccbold":
				// URLs in bold (SGR 1 … 22, as Claude Code prints them): in a
				// frame, as an OSC 8 hyperlink, wrapped by the application
				// inside the frame, and wrapped by the terminal.
				const u = "https://example.com/iwato/bold"
				bold := func(s string) string { return "\x1b[1m" + s + "\x1b[22m" }
				width := max(t.cols-4, 20)
				long := u + "/wrapped-by-the-application?pad=" + strings.Repeat("a", width)
				var b strings.Builder
				b.WriteString("╭" + strings.Repeat("─", width+2) + "╮\r\n")
				fmt.Fprintf(&b, "│ 太字 %s%s │\r\n", bold(u+"/1"), strings.Repeat(" ", max(width-len(u)-7, 0)))
				fmt.Fprintf(&b, "│ リンク \x1b]8;;%s\x07%s\x1b]8;;\x07%s │\r\n", u+"/2", bold(u+"/2"), strings.Repeat(" ", max(width-len(u)-9, 0)))
				for rest := long; rest != ""; {
					n := min(width, len(rest))
					fmt.Fprintf(&b, "│ %s%s │\r\n", bold(rest[:n]), strings.Repeat(" ", width-n))
					rest = rest[n:]
				}
				b.WriteString("╰" + strings.Repeat("─", width+2) + "╯\r\n")
				b.WriteString(bold(u + "/wrapped-by-the-terminal?pad=" + strings.Repeat("b", t.cols)))
				response = b.String()
			case line == "ccwrap":
				// Claude Code's own wrap of a long URL, broken inside its
				// scheme at the edge, the rest on rows indented by two.
				const u = "https://iwato-afp-2.tail9610ad.ts.net:8150/report/faces/index.html"
				width := max(t.cols-2, 40)
				words := "レポートの一番上に並べました: "
				lead := width - 7 - 30
				if lead < 0 {
					words, lead = "", width-7
				}
				var b strings.Builder
				b.WriteString("⏺ " + strings.Repeat("あ", lead/2) + strings.Repeat(" ", lead%2) + words + u[:7])
				for rest := u[7:]; rest != ""; {
					n := min(width, len(rest))
					b.WriteString("\r\n  " + rest[:n])
					rest = rest[n:]
				}
				response = b.String()
			case line == "cctable" || strings.HasPrefix(line, "cctable "):
				// Claude Code's Markdown tables at a narrow width: cells
				// hard-wrapped inside their column, padded by one, centred
				// vertically, ├┼┤ between rows. "cctable N" prints table N.
				response = fakeTables(t.cols, strings.TrimSpace(strings.TrimPrefix(line, "cctable")))
			case line == "framed":
				// A URL that an application wraps itself inside a frame, at
				// the terminal's edge (as Claude Code does).
				width := t.cols - 4
				if width < 20 {
					width = 20
				}
				var b strings.Builder
				// Long enough to need two rows at any width.
				for rest := fakeLongURL + "&pad=" + strings.Repeat("x", width); rest != ""; {
					n := min(width, len(rest))
					fmt.Fprintf(&b, "│ %-*s │\r\n", width, rest[:n])
					rest = rest[n:]
				}
				response = strings.TrimSuffix(b.String(), "\r\n")
			case line == "ccscroll":
				t.startScroll()
				return len(b), nil
			case strings.HasPrefix(line, "tui "):
				var ms, lines int
				fmt.Sscanf(line[4:], "%d %d", &ms, &lines)
				t.startTUI(time.Duration(max(ms, 10))*time.Millisecond, lines)
				return len(b), nil
			case line == "mouse":
				// What tmux sends with `set -g mouse on`.
				t.output("\x1b[?1000h\x1b[?1002h\x1b[?1006h")
				response = "mouse reporting on (scroll to test)"
			case strings.HasPrefix(line, "cd "):
				t.d.mu.Lock()
				if t.d.dirs == nil {
					t.d.dirs = map[string]string{}
				}
				dir := strings.TrimSpace(strings.TrimPrefix(line, "cd "))
				if !path.IsAbs(dir) {
					dir = path.Join(cmp.Or(t.d.dirs[t.key], homeDir+"/"+workDir), dir)
				}
				t.d.dirs[t.key] = path.Clean(dir)
				t.d.mu.Unlock()
			case strings.HasPrefix(line, "propose "):
				// An agent's proposal through policy.local.
				response = t.d.fakePropose(t.sandbox, strings.TrimSpace(strings.TrimPrefix(line, "propose ")))
			case strings.HasPrefix(line, "echo "):
				response = strings.TrimPrefix(line, "echo ")
			case line == "clear":
				t.output("\x1b[2J\x1b[H")
			case line != "":
				response = "FAKE: " + line + " (not executed)"
			}
			chunk := "\r\n" + response + "\r\n"
			t.d.mu.Lock()
			t.d.history[t.key] += t.id + " $ " + line + chunk
			if len(t.d.history[t.key]) > 16384 {
				t.d.history[t.key] = t.d.history[t.key][len(t.d.history[t.key])-16384:]
			}
			t.d.mu.Unlock()
			t.output(chunk + t.id + " $ ")
		case 3:
			t.line = ""
			t.output("^C\r\n" + t.id + " $ ")
		case 4:
			// Ctrl-D on an empty line ends the shell, as bash.
			if t.line == "" {
				t.output("\r\nlogout\r\n")
				go t.d.endTerminal(t.sandbox, t.name)
				return len(b), nil
			}
		case 127, 8:
			if len(t.line) > 0 {
				r := []rune(t.line)
				t.line = string(r[:len(r)-1])
				t.output("\b \b")
			}
		default:
			if c >= 32 && len(t.line) < 4096 {
				t.line += string(c)
				t.output(string(c))
			}
		}
	}
	return len(b), nil
}
func (t *fakeTerm) Resize(cols, rows int) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.cols, t.rows = cols, rows
	if t.app != nil {
		t.drawScroll()
	}
	return nil
}

// Terminals lists the running fake terminals, main first.
func (d *Fake) Terminals(_ context.Context, w Sandbox) ([]string, []string, bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.started[w.Name] {
		return nil, nil, false, fmt.Errorf("sandbox is not started")
	}
	names := []string{}
	for n := range d.terms[w.Name] {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool {
		a, b := names[i], names[j]
		if (a == mainTerm) != (b == mainTerm) {
			return a == mainTerm
		}
		if len(a) != len(b) {
			return len(a) < len(b)
		}
		return a < b
	})
	// Main alone when none runs (attaching starts it), as OpenShell's daemon.
	if len(names) == 0 {
		names = []string{mainTerm}
	}
	outdated := []string{}
	if _, ok := d.terms[w.Name]["2"]; ok && !d.renewed[w.Name+"/2"] {
		outdated = append(outdated, "2")
	}
	return names, outdated, true, nil
}

func (d *Fake) KillTerminal(_ context.Context, w Sandbox, name string) error {
	d.endTerminal(w.Name, name)
	d.mu.Lock()
	d.renewed[w.Name+"/"+name] = true
	d.mu.Unlock()
	return nil
}

// endTerminal forgets a terminal and its history and detaches its viewers.
func (d *Fake) endTerminal(sandbox, name string) {
	d.mu.Lock()
	attached, ran := d.terms[sandbox][name]
	delete(d.terms[sandbox], name)
	delete(d.history, sandbox+"/"+name)
	ended := d.termEnded
	d.mu.Unlock()
	for _, t := range attached {
		t.Close()
	}
	if ran && ended != nil {
		ended(sandbox)
	}
}

// OnTerminalEnd registers f, told when a fake terminal ends.
func (d *Fake) OnTerminalEnd(f func(sandbox string)) {
	d.mu.Lock()
	d.termEnded = f
	d.mu.Unlock()
}

// TerminalsIdle tells whether the running fake sandbox has no terminal.
func (d *Fake) TerminalsIdle(_ context.Context, w Sandbox) (bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.started[w.Name] && len(d.terms[w.Name]) == 0, nil
}
func (t *fakeTerm) Close() error {
	t.once.Do(func() {
		close(t.done)
		t.r.Close()
		t.p.Close()
		t.d.mu.Lock()
		if list, ok := t.d.terms[t.sandbox][t.name]; ok {
			t.d.terms[t.sandbox][t.name] = slices.DeleteFunc(list, func(o *fakeTerm) bool { return o == t })
		}
		t.d.mu.Unlock()
	})
	return nil
}

// fakeLongURL is wider than a phone terminal, to exercise wrapped links.
const fakeLongURL = "https://example.com/iwato/fake/a-very-long-path-that-wraps-across-terminal-lines?with=query&and=more-parameters"

// PolicyRules is the fake sandbox's latest policy (nil before it starts).
func (d *Fake) PolicyRules(_ context.Context, w Sandbox) (map[string]v1.NetworkPolicyRule, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.started[w.Name] && !d.stopped[w.Name] {
		return nil, nil
	}
	revs := d.fakePolicy(w.Name)
	return cloneRules(revs[len(revs)-1].rules), nil
}

// Delete forgets the fake sandbox; purge also clears its terminal history.
func (d *Fake) Delete(_ context.Context, w Sandbox, purge bool) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.started, w.Name)
	delete(d.stopped, w.Name)
	delete(d.policies, w.Name)
	delete(d.drafts, w.Name)
	d.openShell().forgetDraft(w.Name)
	delete(d.prov.attached, w.Name)
	delete(d.files, w.Name)
	delete(d.oldRuntime, w.Name)
	if purge {
		delete(d.history, w.Name)
		for _, p := range w.volumePaths() {
			delete(d.volumes, persistVolume(d.config, w, p))
		}
	}
	return nil
}

func (d *Fake) addVolume(v Volume) {
	if d.volumes == nil {
		d.volumes = map[string]Volume{}
	}
	d.volumes[v.Name] = v
}

// seedVolumes names the volumes after c and adds some: the first
// sandbox's (in use), a deleted sandbox's with its path, and one made
// before the path labels.
func (d *Fake) seedVolumes(c Config) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.config = c
	at := func(days int) string { return time.Now().AddDate(0, 0, -days).UTC().Format(time.RFC3339) }
	if len(c.Sandboxes) > 0 && len(c.Sandboxes[0].LegacyPersist) > 0 {
		w := c.Sandboxes[0]
		d.addVolume(Volume{Name: persistVolume(c, w, w.LegacyPersist[0]), Sandbox: w.Name, Path: w.LegacyPersist[0], CreatedAt: at(12)})
	}
	old := newSandbox("old-app")
	d.addVolume(Volume{Name: persistVolume(c, old, "/sandbox/data"), Sandbox: old.Name, Path: "/sandbox/data", CreatedAt: at(30)})
	d.addVolume(Volume{Name: persistVolume(c, old, "/sandbox/.cache"), Sandbox: old.Name, Path: "/sandbox/.cache", CreatedAt: at(30)})
	d.addVolume(Volume{Name: persistVolume(c, newSandbox("scratch"), "/sandbox/tmp-work"), CreatedAt: at(90)})
}

// Volumes lists the fake volumes as the engine reports them (labels only).
func (d *Fake) Volumes(context.Context) ([]Volume, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	list := []Volume{}
	for _, v := range d.volumes {
		list = append(list, v)
	}
	return list, nil
}

func (d *Fake) RemoveVolume(_ context.Context, name string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.volumes[name].Name == "" {
		return fmt.Errorf("volume %s not found", name)
	}
	delete(d.volumes, name)
	return nil
}

// TermDir is where the terminal's last cd went ("" before any).
func (d *Fake) TermDir(_ context.Context, w Sandbox, name string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.dirs[w.Name+"/"+name]
}

func (d *Fake) markStarted(sandbox, name string) {
	if d.termStarted == nil {
		d.termStarted = map[string]time.Time{}
	}
	d.termStarted[sandbox+"/"+name] = time.Now()
}

// Reset forgets every fake sandbox, volume and provider.
func (d *Fake) Reset(_ context.Context, sandboxes []Sandbox, step func(code string, params map[string]any, text string)) []resetWarn {
	step("sandboxes", nil, "deleting the sandboxes")
	d.mu.Lock()
	var open []*fakeTerm
	for _, byName := range d.terms {
		for _, list := range byName {
			open = append(open, list...)
		}
	}
	d.started, d.stopped, d.history, d.renewed = map[string]bool{}, map[string]bool{}, map[string]string{}, map[string]bool{}
	d.terms, d.termStarted = map[string]map[string][]*fakeTerm{}, nil
	d.policies, d.drafts, d.files, d.volumes = nil, nil, nil, nil
	d.prov = fakeProviders{list: map[string]ProviderInfo{}, attached: map[string][]string{}}
	d.mu.Unlock()
	for _, t := range open {
		_ = t.Close()
	}
	for _, sb := range sandboxes {
		d.openShell().forgetDraft(sb.Name)
	}
	step("gatewayImages", nil, "deleting the gateway and the images")
	return nil
}

func (d *Fake) PrepareAgain(context.Context) {}

// startTUI keeps a terminal busy: as Claude Code at work, redrawing the
// whole alternate screen every period until the next key, or printing a
// numbered line every period, lines in all.
func (t *fakeTerm) startTUI(period time.Duration, lines int) {
	log := lines > 0
	stop := make(chan struct{})
	t.spin = stop
	t.spinEnd = ""
	if !log {
		t.output("\x1b[?1049h\x1b[?25l")
		t.spinEnd = "\x1b[?25h\x1b[?1049l"
	}
	rows := max(t.rows, 5)
	go func() {
		tick := time.NewTicker(period)
		defer tick.Stop()
		for i := 1; ; i++ {
			select {
			case <-stop:
				return
			case <-t.done:
				return
			case <-tick.C:
			}
			if log {
				t.output(fmt.Sprintf("\r\nlog %d https://example.com/log/%d", i, i))
				if i == lines {
					return
				}
				continue
			}
			var b strings.Builder
			b.WriteString("\x1b[?2026h\x1b[H")
			for y := range rows - 1 {
				fmt.Fprintf(&b, "\x1b[3%dm⏺\x1b[0m frame %d row %d: Reading \x1b[1mweb/term.js\x1b[0m https://example.com/doc/%d\x1b[K\r\n", (i+y)%7+1, i, y, i+y)
			}
			fmt.Fprintf(&b, "\x1b[38;5;174m%c\x1b[39m Thinking… frame %d\x1b[K\x1b[?2026l", []rune("✻✽✶✳")[i%4], i)
			t.output(b.String())
		}
	}()
}

// fakeTables draws the cctable tables as Claude Code does: hard-wrapped
// cells padded by one, centred vertically, ├┼┤ between body rows.
func fakeTables(cols int, which string) string {
	wide := func(r rune) int {
		if r >= 0x3000 {
			return 2
		}
		return 1
	}
	width := func(s string) (n int) {
		for _, r := range s {
			n += wide(r)
		}
		return n
	}
	wrap := func(s string, w int) []string {
		var lines []string
		line, n := "", 0
		for _, r := range s {
			if n+wide(r) > w {
				lines, line, n = append(lines, line), "", 0
			}
			line, n = line+string(r), n+wide(r)
		}
		return append(lines, line)
	}
	rule := func(ws []int, l, m, r string) string {
		s := "  " + l
		for i, w := range ws {
			s += strings.Repeat("─", w+2)
			if i < len(ws)-1 {
				s += m
			} else {
				s += r
			}
		}
		return s + "\r\n"
	}
	row := func(ws []int, cells []string, center bool) string {
		wrapped := make([][]string, len(cells))
		h := 1
		for i, c := range cells {
			wrapped[i] = wrap(c, ws[i])
			h = max(h, len(wrapped[i]))
		}
		var b strings.Builder
		for y := range h {
			b.WriteString("  │")
			for i, lines := range wrapped {
				text := ""
				if k := y - (h-len(lines))/2; k >= 0 && k < len(lines) {
					text = lines[k]
				}
				pad := ws[i] - width(text)
				left := 0
				if center {
					left = pad / 2
				}
				b.WriteString(" " + strings.Repeat(" ", left) + text + strings.Repeat(" ", pad-left) + " │")
			}
			b.WriteString("\r\n")
		}
		return b.String()
	}
	table := func(ws []int, header []string, body ...[]string) string {
		s := rule(ws, "┌", "┬", "┐") + row(ws, header, true)
		for _, r := range body {
			s += rule(ws, "├", "┼", "┤") + row(ws, r, false)
		}
		return s + rule(ws, "└", "┴", "┘")
	}
	const u = "https://example.com/iwato/table/"
	tables := []string{
		table([]int{6, min(max(cols-15, 20), 28)}, []string{"Name", "URL"},
			[]string{"report", "https://iwato-afp-2.tail9610ad.ts.net:8150/report/faces/index.html"},
			[]string{"docs", u + "docs"}),
		table([]int{3, min(max(cols-23, 16), 24), 8}, []string{"#", "URL", "メモ"},
			[]string{"1", u + "middle-column", "中央の列"}),
		table([]int{4, 12}, []string{"件", "説明"},
			[]string{"表", "開く " + u + "scheme"}),
		table([]int{min(max((cols-9)/2, 16), 20), min(max((cols-9)/2, 16), 20)}, []string{"左", "右"},
			[]string{u + "left", u + "right"}),
		// The same rows pasted into a code block of an assistant message.
		"⏺ コードブロックの表:\r\n\r\n" +
			"  │ report │ https://iwato-afp-2.tail9610 │\r\n" +
			"  │        │ ad.ts.net:8150/report/faces/ │\r\n" +
			"  │        │ index.html                   │\r\n",
	}
	var b strings.Builder
	b.WriteString("⏺ 表の URL:\r\n")
	for i, t := range tables {
		if which == "" || which == fmt.Sprint(i+1) {
			b.WriteString("\r\n" + t)
		}
	}
	return strings.TrimSuffix(b.String(), "\r\n")
}
