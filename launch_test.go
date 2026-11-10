package main

import (
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTakePort(t *testing.T) {
	never := func(int, string) portChoice { t.Fatal("prompted"); return portQuit }
	config := func(t *testing.T, port int) (string, *Config) {
		c := testConfig()
		c.Port = port
		return filepath.Join(t.TempDir(), "iwato.json"), &c
	}
	held := func(t *testing.T) (net.Listener, int) {
		ln, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { ln.Close() })
		return ln, ln.Addr().(*net.TCPAddr).Port
	}
	iwato := func(t *testing.T, dir string) (*Server, int) {
		ln, port := held(t)
		s := newServer(testConfig(), &idleRuntime{}, false, ln.Addr().String())
		key, _, err := writeLaunchKey(dir)
		if err != nil {
			t.Fatal(err)
		}
		s.launchKey = key
		go http.Serve(ln, s.Handler())
		return s, port
	}

	t.Run("first start remembers the port", func(t *testing.T) {
		file, c := config(t, 0)
		ln, running, remembered, err := takePort(file, c, never)
		if err != nil || running != nil || !remembered {
			t.Fatal(err, running, remembered)
		}
		defer ln.Close()
		saved, err := readConfig(file)
		if err != nil || saved.Port != ln.Addr().(*net.TCPAddr).Port {
			t.Fatal(err, saved.Port, ln.Addr())
		}
	})
	t.Run("this configuration's Iwato answers", func(t *testing.T) {
		file, c := config(t, 0)
		s, port := iwato(t, filepath.Dir(file))
		s.setPublic("pc.example.ts.net", "https://pc.example.ts.net")
		c.Port = port
		_, running, _, err := takePort(file, c, never)
		if err != nil || running == nil || running.token != s.bootstrap || running.origin != s.origin || running.public != s.publicOrigin() {
			t.Fatal(err, running)
		}
	})
	t.Run("another configuration's Iwato is a conflict", func(t *testing.T) {
		file, c := config(t, 0)
		_, c.Port = iwato(t, t.TempDir())
		if err := os.WriteFile(filepath.Join(filepath.Dir(file), launchKeyFile), []byte(randomHex(32)), 0600); err != nil {
			t.Fatal(err)
		}
		asked := 0
		_, _, _, err := takePort(file, c, func(int, string) portChoice { asked++; return portQuit })
		if asked != 1 || err == nil || !strings.Contains(err.Error(), "in use") {
			t.Fatal(asked, err)
		}
	})
	t.Run("another port for this run only", func(t *testing.T) {
		file, c := config(t, 0)
		_, c.Port = held(t)
		choices := []portChoice{portRetry, portAnother}
		ln, running, remembered, err := takePort(file, c, func(int, string) portChoice {
			ch := choices[0]
			choices = choices[1:]
			return ch
		})
		if err != nil || running != nil || remembered || len(choices) != 0 {
			t.Fatal(err, running, remembered, choices)
		}
		ln.Close()
	})
}

func TestConsolePrompt(t *testing.T) {
	var out strings.Builder
	for in, want := range map[string]portChoice{"\n": portRetry, "x\nA\n": portAnother, "q\n": portQuit, "": portQuit} {
		if got := consolePrompt(strings.NewReader(in), &out)(17600, ""); got != want {
			t.Errorf("%q: %v, want %v", in, got, want)
		}
	}
}
