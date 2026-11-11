package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"iwato/internal/ingress/local"
)

func main() { os.Exit(app(os.Args[1:])) }

// appCtx ends serve as Ctrl+C does (the tray's Quit: app_windows.go).
var appCtx = context.Background()

func run(args []string) (err error) {
	// Started by an update (update_restart_*.go): the old process's port.
	restartAddr := os.Getenv(restartListenEnv)
	restored := os.Getenv(restartRestoredEnv) != ""
	_ = os.Unsetenv(restartListenEnv)
	_ = os.Unsetenv(restartRestoredEnv)
	served := false
	if restartAddr != "" && !restored {
		defer func() {
			if err != nil && !served {
				err = rollBack(restartAddr, err)
			}
		}()
	}
	// `iwato` alone creates the configuration if needed and serves.
	if len(args) == 0 {
		args = []string{"serve"}
	}
	command := args[0]
	if command == "help" || command == "-h" || command == "--help" {
		usage()
		return nil
	}
	f := flag.NewFlagSet(command, flag.ContinueOnError)
	configFile := f.String("config", "iwato.json", "configuration JSON")
	listen := f.String("listen", "", "loopback bind address (default: the port in the configuration, taken at the first start)")
	publicOrigin := f.String("public-origin", "", "https origin of `tailscale serve` in front of the listener (phones on the tailnet)")
	sessionHours := f.Int("session-hours", 0, "fixes a browser sign-in's lifetime, in hours (1-720; without it the settings' limits: 7 days unused, 30 at most)")
	browser := f.Bool("browser", browserByDefault(), "open the page in the browser (or the installed app) at start")
	background := f.Bool("background", false, "started at sign-in: no page, and a taken port is waited for")
	verbose := f.Bool("verbose", false, "also show the debug log (gateway, driver, image builds, requests) on the console")
	// -fake exists only in builds with the tag uitest (fake_hooks.go).
	var fakeFlags func() (*fakeRun, error)
	if fakeHooks != nil {
		fakeFlags = fakeHooks.flags(f)
	}
	if e := f.Parse(args[1:]); e != nil {
		return e
	}
	if command == "version" {
		b := currentBuild()
		fmt.Println("iwato", version, b.Commit, b.Built, runtime.GOOS, runtime.GOARCH)
		return nil
	}
	if command != "serve" {
		usage()
		return errors.New("unknown command")
	}
	var fake *fakeRun
	if fakeFlags != nil {
		var e error
		if fake, e = fakeFlags(); e != nil {
			return e
		}
	}
	var c Config
	var e error
	if fake != nil {
		c = fake.config
	} else {
		created, e := ensureConfig(*configFile)
		if e != nil {
			return e
		}
		if created {
			fmt.Println("Created", *configFile, "(no sandboxes yet: create one in the UI).")
		}
		if c, e = readConfig(*configFile); e != nil {
			return e
		}
	}
	var taken net.Listener
	if fake == nil && *listen == "" && restartAddr == "" {
		prompt := startPrompt()
		if *background {
			prompt = waitPrompt(2*time.Minute, 5*time.Second)
		}
		var running *launchTarget
		if taken, running, e = takePort(*configFile, &c, prompt); e != nil {
			return e
		}
		if running != nil {
			if *background {
				return nil
			}
			return openRunning(*running, *browser)
		}
	}
	ctx, cancel := signal.NotifyContext(appCtx, os.Interrupt)
	defer cancel()
	var rt Runtime
	var eng *Engine
	if fake != nil {
		rt = fake.rt
	} else {
		if eng, e = NewEngine(c); e != nil {
			return e
		}
		rt = NewOpenShell(eng, filepath.Dir(*configFile))
	}
	if *sessionHours != 0 && (*sessionHours < 1 || *sessionHours > 720) {
		return errors.New("session-hours must be 1-720")
	}
	opts := serveOptions{publicOrigin: *publicOrigin, sessionTTL: time.Duration(*sessionHours) * time.Hour, verbose: *verbose,
		listener: taken, openBrowser: *browser && !*background && fake == nil && restartAddr == ""}
	if restartAddr != "" {
		*listen, opts.listenWait = restartAddr, 30*time.Second
	}
	opts.serving = func() { served = true }
	if fake == nil {
		opts.serving = func() {
			served = true
			go removeOldExe()
		}
		opts.pushFile = filepath.Join(filepath.Dir(*configFile), "webpush.json")
		opts.configPath = *configFile
		opts.logFile = filepath.Join(filepath.Dir(*configFile), logFileName)
	}
	if failStart != nil {
		return failStart
	}
	return serve(ctx, c, rt, eng, fake, *listen, opts)
}

// failStart is set in uitest builds only (fake_update.go).
var failStart error

type serveOptions struct {
	publicOrigin, pushFile, configPath string
	// logFile is the debug log (none when empty); verbose also shows it on
	// the console.
	logFile    string
	verbose    bool
	sessionTTL time.Duration
	// listenWait: retry the listen address this long (a restart).
	listenWait time.Duration
	// serving is called once the listener is up, just before serving.
	serving func()
	// listener was taken by run (takePort); without it serve listens on addr.
	listener net.Listener
	// openBrowser opens the page once serving.
	openBrowser bool
}

func openRunning(t launchTarget, browser bool) error {
	if browser {
		if err := openPage(t, true); err == nil {
			fmt.Println("Iwato is already running: opened it.")
			return nil
		}
	}
	fmt.Printf("Iwato is already running. Open this URL in a browser:\n%s\nSingle use, valid until %s (10 minutes). Do not share it.\n", t.url(), time.Now().Add(10*time.Minute).Format("15:04"))
	return nil
}

func serve(ctx context.Context, c Config, rt Runtime, eng *Engine, fake *fakeRun, addr string, opts serveOptions) error {
	// quit ends the server as Ctrl+C does (an update's restart).
	ctx, quit := context.WithCancel(ctx)
	defer quit()
	var publicHost, publicOrigin string
	if opts.publicOrigin != "" {
		var e error
		if publicHost, publicOrigin, e = parsePublicOrigin(opts.publicOrigin); e != nil {
			return e
		}
	}
	// The debug log goes to the page's log view and the log file (and the
	// console with -verbose); listenLocal's notice is kept for the ring.
	var early logRing
	restore, logErr := setupLogging(os.Stderr, &early, opts.logFile, opts.verbose)
	defer func() { restore() }()
	ln, e := opts.listener, error(nil)
	if ln == nil {
		ln, e = listenLocalWait(addr, opts.listenWait)
	}
	if e != nil {
		return e
	}
	defer ln.Close()
	s := newServer(c, rt, fake != nil, ln.Addr().String())
	// The installed app belongs to the kept port: open it there however the
	// port was taken (also after an update's restart or with -listen).
	pwa := c.Port != 0 && ln.Addr().(*net.TCPAddr).Port == c.Port
	if opts.configPath != "" {
		key, remove, err := writeLaunchKey(filepath.Dir(opts.configPath))
		if err != nil {
			noticef("cannot write %s (starting Iwato again will not open this one): %v", launchKeyFile, english(err))
		} else {
			s.launchKey = key
			defer remove()
		}
	}
	restore()
	restore, logErr = setupLogging(os.Stderr, &s.logs, opts.logFile, opts.verbose)
	lines, _ := early.since(-1)
	for _, l := range lines {
		fmt.Fprintln(&s.logs, l)
	}
	s.ctx = ctx
	s.setPublic(publicHost, publicOrigin)
	s.fixedPublic = publicOrigin != ""
	s.configPath = opts.configPath
	if opts.sessionTTL > 0 {
		s.sessionTTL = opts.sessionTTL
	}
	if opts.configPath != "" {
		if e := s.loadSessions(filepath.Join(filepath.Dir(opts.configPath), "sessions.json")); e != nil {
			return fmt.Errorf("session store: %w", e)
		}
	}
	if opts.configPath != "" {
		if s.davKeys, e = loadDavKeys(filepath.Join(filepath.Dir(opts.configPath), "dav-keys.json")); e != nil {
			return fmt.Errorf("file credentials: %w", e)
		}
	}
	// The startup checks (checks.go). Without a usable engine the page
	// shows the failed check and the preparation waits for the engine.
	switch {
	case eng != nil:
		s.checks = newCheckState(func(ctx context.Context) []check {
			return diagnose(ctx, eng, opts.configPath, len(s.sandboxList()))
		})
	case fake != nil:
		fake.setup(s)
	}
	var notice string
	if s.checks != nil {
		checks, _ := s.checks.refresh(ctx)
		go s.checks.waitEngine(ctx, 5*time.Second)
		if down := engineDown(checks); down != nil {
			notice = engineNames[down.Name] + " is not usable: see the browser. Iwato goes on once it is."
		} else {
			for _, ch := range checks {
				if ch.Level == "fail" {
					notice = "A startup check failed: see the browser (Settings)."
					break
				}
			}
		}
	}
	// Before Prepare, which imports them.
	s.loadProfiles()
	if o, ok := rt.(*OpenShell); ok {
		o.engineErr = s.checks.engineErr
		go s.checks.whenReady(ctx, func() { o.Prepare(ctx) })
		go o.ResumeTerminals(ctx, s.sandboxList)
		go s.watchGateway(ctx, o)
		go s.usageLoop(ctx)
	}
	if opts.configPath != "" {
		s.ingress.dir = filepath.Dir(opts.configPath)
		// The owner's network presets (presets.go).
		setUserPresetDir(filepath.Join(filepath.Dir(opts.configPath), "presets"))
	}
	go s.ingress.loop(ctx)
	if opts.configPath != "" {
		s.resources.disk.file = filepath.Join(filepath.Dir(opts.configPath), "resources.json")
	}
	go s.resources.loop(ctx)
	if fake == nil && opts.configPath != "" {
		if s.update = newSelfUpdate(); s.update != nil {
			s.update.restart = quit
			go s.update.loop(ctx)
		}
	}
	if eng != nil && opts.configPath != "" {
		dir := filepath.Dir(opts.configPath)
		s.images = &imageManager{}
		s.imageUpdates = &imageUpdates{src: engineImages{eng, dir}}
		var mu sync.Mutex
		running, again := false, false
		s.retryImages = func() {
			mu.Lock()
			if running {
				again = true
				mu.Unlock()
				return
			}
			running = true
			mu.Unlock()
			go func() {
				for {
					s.prepareImages(ctx, eng, dir)
					mu.Lock()
					if !again || ctx.Err() != nil {
						running = false
						mu.Unlock()
						return
					}
					again = false
					mu.Unlock()
				}
			}()
		}
		s.imageCleanup = func() {
			c, cancel := context.WithTimeout(ctx, 5*time.Minute)
			defer cancel()
			if err := eng.CleanupImages(c, dir); err != nil {
				log.Println("old image cleanup:", safeText(english(err)))
			}
		}
		go s.checks.whenReady(ctx, s.retryImages)
	}
	if opts.pushFile != "" {
		subject := "mailto:owner@iwato.invalid"
		if publicOrigin != "" {
			subject = publicOrigin
		}
		if s.push, e = OpenPushStore(opts.pushFile, subject); e != nil {
			return fmt.Errorf("web push store: %w", e)
		}
		s.notifier = newNotifier(s)
		go s.notifier.run(ctx)
	}
	h := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
	fmt.Printf("Iwato %s\n", version)
	if fake != nil {
		fmt.Println("Fake backend for UI tests: no WSLc, OpenShell or real shell")
	}
	switch {
	case logErr != nil:
		noticef("cannot open the log file (the log is only in the UI's Log): %v", english(logErr))
	case opts.logFile != "" && opts.verbose:
		fmt.Printf("Log: %s (-verbose: also here)\n", opts.logFile)
	case opts.logFile != "":
		fmt.Printf("Log: %s and the UI's Log (-verbose also shows it here)\n", opts.logFile)
	}
	_, preparing := rt.(preparer)
	preparing = preparing || s.images != nil
	header := "Iwato started"
	if preparing && s.checks.engine() == nil {
		header += " (preparing the images and OpenShell: the browser shows the progress)"
	}
	fmt.Print(s.loginBlock(header, "", true))
	if notice != "" {
		fmt.Println(notice)
	}
	if preparing {
		go s.announcePreparation(ctx, os.Stdout, time.Second)
	}
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-ctx.Done()
		shutdown, cancel := timeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = h.Shutdown(shutdown)
	}()
	if opts.serving != nil {
		opts.serving()
	}
	if fake == nil && opts.configPath != "" {
		s.autostart = newAutostart(opts.configPath)
	}
	currentApp.Store(&appActions{logFile: opts.logFile, autostart: s.autostart, usage: s.usageSummary, open: func() {
		token, _ := s.newLaunchToken()
		if err := openPage(launchTarget{s.origin, token, s.publicOrigin()}, pwa); err != nil {
			noticef("cannot open the browser: %v", err)
		}
	}})
	defer currentApp.Store(nil)
	if s.autostart != nil {
		if on, err := s.autostart.Enabled(); err == nil {
			notifyAutostart(on)
		}
	}
	if opts.openBrowser {
		s.mu.Lock()
		token := s.bootstrap
		s.mu.Unlock()
		go func() {
			// The admin node comes up in a few seconds; only a PC on the
			// tailnet itself can open it.
			if s.ingress.adminExpected() && tailscaleSelfLogin() != "" {
				s.waitPublic(20 * time.Second)
			}
			if err := openPage(launchTarget{s.origin, token, s.publicOrigin()}, pwa); err != nil {
				noticef("cannot open the browser: %v", err)
			}
		}()
	}
	go func() { _ = h.Serve(s.ingress.adminLn) }()
	// The local ingress's hosts (<port>.<sandbox>.localhost) leave the
	// listener before the admin UI's Host check (internal/ingress/local).
	e = h.Serve(local.Split(ln, s.ingress.take, h.ReadHeaderTimeout, h.MaxHeaderBytes))
	if !errors.Is(e, http.ErrServerClosed) {
		return e
	}
	if exe := s.update.swappedExe(); exe != "" {
		<-shutdownDone
		ln.Close()
		fmt.Println("Restarting Iwato for the update…")
		return relaunch(exe, ln.Addr().String())
	}
	return nil
}

// loginBlock tells the owner how to log in. The single-use token is shown
// while it is unused and valid; an expired one is replaced (nobody logged
// in with it), but a used one is not: someone logged in already, and a new
// one comes from 別端末でログイン or a restart.
func (s *Server) loginBlock(header, reason string, startup bool) string {
	s.mu.Lock()
	if s.bootstrap != "" && time.Now().After(s.bootstrapUntil) {
		s.bootstrap, s.bootstrapUntil = randomHex(32), time.Now().Add(10*time.Minute)
	}
	token, until := s.bootstrap, s.bootstrapUntil
	s.mu.Unlock()
	var b strings.Builder
	fmt.Fprintf(&b, "\n== %s ==\n", header)
	if reason != "" {
		fmt.Fprintf(&b, "Reason: %s\n", strings.Join(strings.Fields(reason), " "))
		b.WriteString("Retry in the browser; its Log has the details.\n")
	}
	if token == "" {
		fmt.Fprintf(&b, "%s/\n", s.origin)
		if s.publicOrigin() != "" {
			fmt.Fprintf(&b, "Tailnet: %s/\n", s.publicOrigin())
		}
		b.WriteString("The startup URL has been used; browsers that logged in with it stay logged in.\n")
	} else {
		if startup {
			b.WriteString("Open this URL in a browser:\n")
		}
		fmt.Fprintf(&b, "%s/#token=%s\n", s.origin, token)
		if s.publicOrigin() != "" {
			fmt.Fprintf(&b, "Tailnet (tailscale serve): %s/#token=%s\n", s.publicOrigin(), token)
		}
		fmt.Fprintf(&b, "Single use, valid until %s (10 minutes). Do not share it.\n", until.Format("15:04"))
	}
	b.WriteString("A new URL: restart Iwato, or use \"Sign in on another device\" in a logged-in browser.\n")
	if startup {
		stop := "Ctrl+C stops Iwato"
		if runtime.GOOS == "windows" {
			stop = "Quit Iwato from its icon in the notification area"
		}
		b.WriteString(stop + "; the sandboxes and their main terminals keep running.\n")
	}
	b.WriteString("\n")
	return b.String()
}

// announcePreparation prints the login block again once the image and the
// gateway are ready or their preparation failed (and again after a retry),
// so the URL is not buried under the startup's output.
func (s *Server) announcePreparation(ctx context.Context, w io.Writer, every time.Duration) {
	announced := false
	for {
		st, _ := s.imagesReady()
		switch st.Phase {
		case "ready", "failed":
			if !announced {
				announced = true
				if st.Phase == "ready" {
					log.Println("preparation: ready")
					fmt.Fprint(w, s.loginBlock("Iwato is ready", "", false))
				} else {
					log.Println("preparation failed:", st.Message)
					fmt.Fprint(w, s.loginBlock("Iwato could not be prepared", st.Message, false))
				}
			}
		default:
			announced = false
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		}
	}
}

func writeExclusive(p string, data []byte) error {
	f, e := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return e
	}
	_, e = f.Write(data)
	ce := f.Close()
	if e != nil {
		return e
	}
	return ce
}

func usage() {
	fakeUsage := ""
	if fakeHooks != nil {
		fakeUsage = "\n  iwato serve -fake [-fake-engine-down]   (build tag uitest: the fake backend of the browser checks)"
	}
	fmt.Println(`Iwato ` + version + `
  iwato                (create iwato.json if missing, then serve)
  iwato serve [-config iwato.json] [-listen 127.0.0.1:PORT] [-browser=false] [-background] [-public-origin https://HOST.TAILNET.ts.net] [-session-hours N] [-verbose]` + fakeUsage + `
The page's port is taken at the first start (17600 or the next free one)
and kept in iwato.json ("port"). The page opens in the browser, or in the
installed app; starting Iwato again opens the running one.
The console shows the login URL, warnings and errors; the startup checks
and their fixes are in the UI (Settings). The rest of the log
(gateway, driver, image builds, requests) goes to iwato.log next to the
configuration (rotated at 10 MB to iwato.log.1) and to the UI's ログ.
-verbose also shows it on the console.
  iwato version
  iwato help
Sandboxes run on OpenShell in WSLc (Windows) or a Docker-compatible engine
such as Colima (macOS, Linux), and are created and run from the UI.`)
}
