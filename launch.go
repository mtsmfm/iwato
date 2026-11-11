package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// Starting Iwato again (a double-click while it runs) opens the running one:
// the second process asks it for a sign-in token with the key it wrote next
// to the configuration, which only the owner's account can read.
const (
	launchKeyFile = "iwato.launch"
	// pwaProtocol is the installed app's protocol handler (manifest): when
	// the OS knows it, the page opens in the app rather than a browser tab.
	pwaProtocol = "web+iwato"
)

type portChoice int

const (
	portRetry portChoice = iota
	portAnother
	portQuit
)

// portPrompt decides what to do while the remembered port is taken by
// something other than this configuration's Iwato.
type portPrompt func(port int, owner string) portChoice

// launchTarget is the page to open: this PC's origin, a sign-in token and
// the tailnet origin (the page's node, or tailscale serve) when there is one.
type launchTarget struct {
	origin, token, public string
}

// takePort listens on the configuration's port, remembering the first one
// taken. When this configuration's Iwato already serves there it returns
// that one instead of a listener.
func takePort(configFile string, c *Config, prompt portPrompt) (ln net.Listener, running *launchTarget, err error) {
	if c.Port == 0 {
		if ln, err = listenLocal(""); err != nil {
			return nil, nil, err
		}
		c.Port = ln.Addr().(*net.TCPAddr).Port
		if e := writeConfig(configFile, *c); e != nil {
			noticef("cannot keep port %d in %s: %v", c.Port, configFile, e)
		}
		return ln, nil, nil
	}
	addr := "127.0.0.1:" + strconv.Itoa(c.Port)
	for {
		if ln, err = net.Listen("tcp4", addr); err == nil {
			return ln, nil, nil
		}
		if t, ok := askRunning(filepath.Dir(configFile), addr); ok {
			return nil, &t, nil
		}
		owner := portOwner(c.Port)
		switch prompt(c.Port, owner) {
		case portRetry:
			continue
		case portAnother:
			ln, err = listenLocal("")
			return ln, nil, err
		}
		if owner == "" {
			owner = "another program"
		}
		return nil, nil, fmt.Errorf("port %d is in use by %s (\"port\" in %s or -listen chooses another)", c.Port, owner, configFile)
	}
}

// askRunning asks the Iwato at addr for a sign-in token, with the key this
// configuration's Iwato wrote.
func askRunning(dir, addr string) (launchTarget, bool) {
	key, err := os.ReadFile(filepath.Join(dir, launchKeyFile))
	if err != nil {
		return launchTarget{}, false
	}
	origin := "http://" + addr
	body, _ := json.Marshal(map[string]string{"key": strings.TrimSpace(string(key))})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, origin+"/api/launch", bytes.NewReader(body))
	if err != nil {
		return launchTarget{}, false
	}
	req.Header.Set("Origin", origin)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return launchTarget{}, false
	}
	defer res.Body.Close()
	var got struct {
		Token  string `json:"token"`
		Public string `json:"public"`
	}
	if res.StatusCode != 200 || json.NewDecoder(io.LimitReader(res.Body, 4096)).Decode(&got) != nil || !launchToken.MatchString(got.Token) {
		return launchTarget{}, false
	}
	if _, public, err := parsePublicOrigin(got.Public); err == nil {
		got.Public = public
	} else {
		got.Public = ""
	}
	return launchTarget{origin, got.Token, got.Public}, true
}

// appActions are what the tray (app_windows.go) does while Iwato serves:
// open the page with a new sign-in token, open the log file, switch
// starting at sign-in, and show the subscriptions' usage.
type appActions struct {
	open      func()
	logFile   string
	autostart autostarter
	// usage is the subscriptions' usage for the tooltip (usage.go).
	usage func() string
}

var currentApp atomic.Pointer[appActions]

var launchToken = regexp.MustCompile(`^[0-9a-f]{64}$`)

// writeLaunchKey writes this process's key; the returned func removes it
// unless a later Iwato (an update's restart) has replaced it.
func writeLaunchKey(dir string) (string, func(), error) {
	key := randomHex(32)
	p := filepath.Join(dir, launchKeyFile)
	tmp, err := os.CreateTemp(dir, ".iwato-launch-*")
	if err != nil {
		return "", func() {}, err
	}
	defer os.Remove(tmp.Name())
	_, err = tmp.WriteString(key + "\n")
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), p)
	}
	if err != nil {
		return "", func() {}, err
	}
	return key, func() {
		if b, e := os.ReadFile(p); e == nil && strings.TrimSpace(string(b)) == key {
			_ = os.Remove(p)
		}
	}, nil
}

// openPage opens the page in the installed app when asked (pwa) and the OS
// knows its protocol (the app is on whichever origin it was installed
// from: the token works on both), otherwise in the default browser, on the
// tailnet origin when it answers.
func openPage(t launchTarget, pwa bool) error {
	if pwa && protocolHandled(pwaProtocol) {
		if err := openURL(pwaProtocol + ":" + t.token); err == nil {
			return nil
		}
	}
	if t.public != "" && answers(t.public) {
		t.origin = t.public
	}
	return openURL(t.url())
}

// answers: the page loads from origin (this PC reaches the tailnet address).
func answers(origin string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, origin+"/manifest.webmanifest", nil)
	if err != nil {
		return false
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	res.Body.Close()
	return res.StatusCode == 200
}

func (t launchTarget) url() string {
	if t.token == "" {
		return t.origin + "/"
	}
	return t.origin + "/#token=" + t.token
}

// browserByDefault: opening the page needs a desktop (not an SSH session
// on Linux).
func browserByDefault() bool {
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		return true
	}
	return os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != ""
}

// consolePrompt asks the owner on the console.
func consolePrompt(in io.Reader, out io.Writer) portPrompt {
	r := bufio.NewReader(in)
	return func(port int, owner string) portChoice {
		if owner == "" {
			owner = "another program"
		}
		fmt.Fprintf(out, "\nPort %d, which Iwato keeps for its page, is in use by %s.\n", port, owner)
		fmt.Fprintf(out, "Another port works this time, but the installed app (PWA) and the browsers' sign-ins belong to %d.\n", port)
		for {
			fmt.Fprint(out, "[R]etry, use [a]nother port this time, or [q]uit? ")
			line, err := r.ReadString('\n')
			switch strings.ToLower(strings.TrimSpace(line)) {
			case "", "r":
				if err != nil {
					return portQuit
				}
				return portRetry
			case "a":
				return portAnother
			case "q":
				return portQuit
			}
			if err != nil {
				return portQuit
			}
		}
	}
}

// waitPrompt retries for wait without asking (started at sign-in, or no
// console): the port may come free, as when the PC is starting.
func waitPrompt(wait, every time.Duration) portPrompt {
	var deadline time.Time
	return func(port int, owner string) portChoice {
		if deadline.IsZero() {
			deadline = time.Now().Add(wait)
			if owner == "" {
				owner = "another program"
			}
			noticef("port %d is in use by %s; waiting up to %s", port, owner, wait)
		}
		if time.Now().After(deadline) {
			return portQuit
		}
		time.Sleep(every)
		return portRetry
	}
}
