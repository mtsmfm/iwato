package main

import (
	"bufio"
	"context"
	"encoding/json"
	"github.com/gorilla/websocket"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"
)

// This runs the actual compiled program in a child process, with its fake
// backend (--fake: build it with -tags uitest). It is executable/HTTP/WS
// coverage, not WSLc/Tailscale E2E.
func TestCompiledFakeProcessHTTPAndWebSocket(t *testing.T) {
	bin := os.Getenv("IWATO_SMOKE_BINARY")
	if bin == "" {
		t.Skip("set IWATO_SMOKE_BINARY to a freshly built native executable (go build -tags uitest)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "serve", "--fake", "--listen", "127.0.0.1:0")
	out, e := cmd.StdoutPipe()
	if e != nil {
		t.Fatal(e)
	}
	cmd.Stderr = io.Discard
	if e = cmd.Start(); e != nil {
		t.Fatal("compiled fake process failed to start", e)
	}
	defer func() { cancel(); _ = cmd.Wait() }()
	urls := make(chan string, 1)
	go func() {
		scan := bufio.NewScanner(out)
		pattern := regexp.MustCompile(`^http://127\.0\.0\.1:\d+/#token=[a-f0-9]{64}$`)
		for scan.Scan() {
			if pattern.MatchString(scan.Text()) {
				select {
				case urls <- scan.Text():
				default:
				}
			}
		}
	}()
	var launch string
	select {
	case launch = <-urls:
	case <-ctx.Done():
		t.Fatal("fake launch URL was not produced")
	}
	u, e := url.Parse(launch)
	if e != nil {
		t.Fatal("invalid launch URL")
	}
	origin := u.Scheme + "://" + u.Host
	token := strings.TrimPrefix(u.Fragment, "token=")
	client := &http.Client{Timeout: 5 * time.Second}
	call := func(method, path, body string, c *http.Cookie, csrf string) (int, []byte, *http.Response) {
		req, _ := http.NewRequestWithContext(ctx, method, origin+path, strings.NewReader(body))
		req.Header.Set("Origin", origin)
		req.Header.Set("Content-Type", "application/json")
		if c != nil {
			req.AddCookie(c)
		}
		if csrf != "" {
			req.Header.Set("X-CSRF-Token", csrf)
		}
		res, e := client.Do(req)
		if e != nil {
			t.Fatal("HTTP operation failed")
		}
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		return res.StatusCode, b, res
	}
	code, b, res := call("GET", "/", "", nil, "")
	if code != 200 || !strings.Contains(string(b), "<title>Iwato</title>") || res.Header.Get("Content-Security-Policy") == "" {
		t.Fatal("embedded UI/security headers missing")
	}
	code, b, res = call("POST", "/api/session", `{"token":"`+token+`"}`, nil, "")
	if code != 200 || len(res.Cookies()) != 1 {
		t.Fatal("real bootstrap failed")
	}
	cookie := res.Cookies()[0]
	var session struct {
		CSRF string `json:"csrf"`
	}
	if json.Unmarshal(b, &session) != nil || session.CSRF == "" {
		t.Fatal("missing csrf")
	}
	code, _, _ = call("POST", "/api/sandboxes/dev/start", "", cookie, session.CSRF)
	if code != 200 {
		t.Fatal("start failed")
	}
	connect := func() *websocket.Conn {
		ws, _, err := websocket.DefaultDialer.Dial("ws://"+u.Host+"/ws/dev?csrf="+session.CSRF, http.Header{"Origin": {origin}, "Cookie": {cookie.String()}})
		if err != nil {
			t.Fatal("real binary websocket handshake failed")
		}
		return ws
	}
	ws := connect()
	v := viewWS(t, ws, 80, 24)
	waitText(t, v, "dev $")
	if e = v.Input([]byte("echo native-process-smoke\r")); e != nil {
		t.Fatal(e)
	}
	waitText(t, v, "\nnative-process-smoke\ndev $")
	ws.Close()
	ws = connect()
	waitText(t, viewWS(t, ws, 80, 24), "native-process-smoke")
	ws.Close()
	code, b, _ = call("GET", "/api/sandboxes/dev/proposals", "", cookie, "")
	if code != 200 || !strings.Contains(string(b), `"pending":[{`) {
		t.Fatal("fake approvals", code, string(b))
	}
	t.Log("Actual compiled child process: embedded UI, bootstrap/session, CSRF start, real WebSocket input/reconnect and the fake approvals passed; WSLc and OpenShell are simulated")
}
