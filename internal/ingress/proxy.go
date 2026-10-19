package ingress

import (
	"context"
	"errors"
	"fmt"
	"html"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// sandboxProxy is a sandbox's reverse proxy: one Transport, so the
// connections to the sandbox are kept and shared by every client.
type sandboxProxy struct {
	transport *http.Transport
	proxy     *httputil.ReverseProxy
}

// reqInfo travels with a request from the handler to the proxy's hooks.
type reqInfo struct {
	t          Target
	opts       PortOptions
	secure     bool
	publicHost string // with the port
	admin      string // the connection's AdminURL
}

type reqInfoKey struct{}

func info(r *http.Request) *reqInfo {
	i, _ := r.Context().Value(reqInfoKey{}).(*reqInfo)
	return i
}

func (s *Server) proxyFor(sandbox string) *sandboxProxy {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p := s.proxies[sandbox]; p != nil {
		return p
	}
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
			_, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			n, err := strconv.ParseUint(port, 10, 16)
			if err != nil {
				return nil, err
			}
			c, err := s.Dialer.DialContext(ctx, Target{Sandbox: sandbox, Port: uint16(n)})
			if err != nil {
				return nil, err
			}
			return upstreamConn{c}, nil
		},
		// The keep-alive pool takes at most 12 of the sandbox's places
		// per port; WebSockets and TCP take the others. Idle ones are
		// dropped when a dial finds no free place (Backend.OnBusy).
		MaxConnsPerHost:     12,
		MaxIdleConnsPerHost: 12,
		MaxIdleConns:        64,
		IdleConnTimeout:     60 * time.Second,
		DisableCompression:  true,
		ForceAttemptHTTP2:   false,
	}
	p := &sandboxProxy{transport: tr}
	p.proxy = &httputil.ReverseProxy{
		Transport:      tr,
		Rewrite:        rewrite,
		ModifyResponse: modifyResponse,
		ErrorHandler:   s.proxyError,
		// Dev servers stream (server-sent events, progress): no buffering.
		FlushInterval: -1,
		ErrorLog:      nil,
	}
	if s.proxies == nil {
		s.proxies = map[string]*sandboxProxy{}
	}
	s.proxies[sandbox] = p
	return p
}

// upstreamConn is the HTTP proxy's connection to the sandbox, without
// CloseWrite. The proxy half-closes an upgraded connection (WebSocket)
// whose client went away and waits for the sandbox's server to close its
// end, which it does only if the relay passes the half-close on: until
// then the connection holds one of the sandbox's places. Without
// CloseWrite the proxy closes it as soon as the client is gone.
type upstreamConn struct{ net.Conn }

func (s *Server) handler(c Conn, t Target, opts PortOptions) http.Handler {
	auth := s.auth(c)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c.Resolve != nil {
			// Host routing: a request on this connection for another
			// host (or one not exposed) is not this target's.
			if rt, ok := c.Resolve(r.Host); !ok || rt != t {
				w.Header().Set("Connection", "close")
				s.refusalPage(w, &reqInfo{t: t, admin: c.AdminURL}, ErrNotHere)
				return
			}
		}
		if !auth.AllowHTTP(w, r, c.Identity, t) {
			return
		}
		auth.Strip(r)
		host := r.Host
		if host == "" {
			host = c.PublicHost + ":" + strconv.Itoa(int(t.Port))
		}
		ri := &reqInfo{t: t, opts: opts, secure: c.Secure, publicHost: host, admin: c.AdminURL}
		r = r.WithContext(context.WithValue(r.Context(), reqInfoKey{}, ri))
		s.proxyFor(t.Sandbox).proxy.ServeHTTP(w, r)
	})
}

// rewrite sends the request to the sandbox's loopback with the Host its
// port's policy asks for.
func rewrite(pr *httputil.ProxyRequest) {
	ri := info(pr.In)
	local := "localhost:" + strconv.Itoa(int(ri.t.Port))
	pr.Out.URL.Scheme = "http"
	pr.Out.URL.Host = "127.0.0.1:" + strconv.Itoa(int(ri.t.Port))
	proto := "http"
	if ri.secure {
		proto = "https"
	}
	pr.Out.Header.Del("Forwarded")
	pr.Out.Header.Del("X-Forwarded-Host")
	pr.Out.Header.Del("X-Forwarded-Proto")
	pr.Out.Header.Del("X-Forwarded-For")
	if ip, _, err := net.SplitHostPort(pr.In.RemoteAddr); err == nil {
		pr.Out.Header.Set("X-Forwarded-For", ip)
	}
	pr.Out.Header.Set("X-Forwarded-Proto", proto)
	switch ri.opts.Host {
	case HostPreserve:
		pr.Out.Host = ri.publicHost
	case HostForwarded:
		pr.Out.Host = local
		pr.Out.Header.Set("X-Forwarded-Host", ri.publicHost)
	default:
		pr.Out.Host = local
		// The page's own origin, as the dev server would see it locally.
		if o := pr.Out.Header.Get("Origin"); o == proto+"://"+ri.publicHost {
			pr.Out.Header.Set("Origin", "http://"+local)
		}
		if ref := pr.Out.Header.Get("Referer"); strings.HasPrefix(ref, proto+"://"+ri.publicHost+"/") {
			pr.Out.Header.Set("Referer", "http://"+local+strings.TrimPrefix(ref, proto+"://"+ri.publicHost))
		}
	}
}

// modifyResponse points the sandbox's own addresses in Location and
// cookies at the public name.
func modifyResponse(res *http.Response) error {
	ri := info(res.Request)
	if ri == nil {
		return nil
	}
	proto := "http"
	if ri.secure {
		proto = "https"
	}
	if loc := res.Header.Get("Location"); loc != "" {
		if u, err := url.Parse(loc); err == nil && u.IsAbs() && isLocalHost(u.Host, ri.t.Port) {
			u.Scheme, u.Host = proto, ri.publicHost
			res.Header.Set("Location", u.String())
		}
	}
	if cookies := res.Header.Values("Set-Cookie"); len(cookies) > 0 {
		res.Header.Del("Set-Cookie")
		for _, c := range cookies {
			res.Header.Add("Set-Cookie", stripLocalDomain(c))
		}
	}
	return nil
}

// isLocalHost: the sandbox's loopback names for the port (or without one).
func isLocalHost(hostport string, port uint16) bool {
	host, p, err := net.SplitHostPort(hostport)
	if err != nil {
		host, p = hostport, ""
	}
	if p != "" && p != strconv.Itoa(int(port)) {
		return false
	}
	switch strings.ToLower(strings.Trim(host, "[]")) {
	case "localhost", "127.0.0.1", "::1", "0.0.0.0":
		return true
	}
	return false
}

// stripLocalDomain drops Domain=localhost (or 127.0.0.1) from a cookie so
// that it belongs to the public host.
func stripLocalDomain(cookie string) string {
	parts := strings.Split(cookie, ";")
	out := parts[:1]
	for _, p := range parts[1:] {
		k, v, _ := strings.Cut(strings.TrimSpace(p), "=")
		if strings.EqualFold(k, "domain") {
			switch strings.ToLower(strings.TrimPrefix(strings.TrimSpace(v), ".")) {
			case "localhost", "127.0.0.1":
				continue
			}
		}
		out = append(out, p)
	}
	return strings.Join(out, ";")
}

// proxyError answers what reaching the sandbox failed with.
func (s *Server) proxyError(w http.ResponseWriter, r *http.Request, err error) {
	ri := info(r)
	if isClientGone(r, err) {
		return
	}
	switch {
	case errors.Is(err, ErrStopped):
		s.errorPage(w, ri, http.StatusServiceUnavailable, "停止中", fmt.Sprintf("サンドボックス %s は停止中です。", ri.t.Sandbox), "Iwato の画面で起動すると、同じ URL で開けます。")
	case errors.Is(err, ErrRefused):
		s.errorPage(w, ri, http.StatusBadGateway, "サーバーがありません", fmt.Sprintf(":%d で待ち受けているサーバーがありません。", ri.t.Port),
			fmt.Sprintf("サンドボックス %s の中で、ポート %d (127.0.0.1 か 0.0.0.0、または ::1) で待ち受けるサーバーを起動してください。", ri.t.Sandbox, ri.t.Port))
	case errors.Is(err, ErrBusy):
		w.Header().Set("Retry-After", "5")
		s.errorPage(w, ri, http.StatusServiceUnavailable, "混み合っています", fmt.Sprintf("サンドボックス %s への接続が上限 (%d) に達しています。", ri.t.Sandbox, DefaultMaxConns), "少し待ってから読み込み直してください。WebSocket や TCP の接続を閉じると空きます。")
	default:
		s.logf("ingress %s:%d: %s %s: %v", ri.t.Sandbox, ri.t.Port, r.Method, r.URL.Path, err)
		s.errorPage(w, ri, http.StatusBadGateway, "接続できません", fmt.Sprintf("サンドボックス %s の :%d に接続できませんでした。", ri.t.Sandbox, ri.t.Port), err.Error())
	}
}

// refusalPage answers the core's refusals of HTTP-only connections.
func (s *Server) refusalPage(w http.ResponseWriter, ri *reqInfo, err error) {
	switch {
	case errors.Is(err, ErrDisabled):
		s.errorPage(w, ri, http.StatusNotFound, "公開していません", fmt.Sprintf("サンドボックス %s の :%d は公開しない設定です。", ri.t.Sandbox, ri.t.Port), "iwato.json のこのサンドボックスの ingress の設定で変えられます。")
	case errors.Is(err, ErrTCPOnly):
		s.errorPage(w, ri, http.StatusNotImplemented, "TCP のポートです", fmt.Sprintf(":%d は TCP (mode tcp) の設定です。", ri.t.Port), "この PC の公開 (*.localhost) は HTTP と WebSocket だけを通します。TCP のまま使うには Tailscale で公開してください。")
	case errors.Is(err, ErrNotHere):
		s.errorPage(w, ri, http.StatusMisdirectedRequest, "別のホストへの要求です", "この接続は別のホストのものです。", "読み込み直してください。")
	default:
		r := (&http.Request{URL: &url.URL{}}).WithContext(context.WithValue(context.Background(), reqInfoKey{}, ri))
		s.proxyError(w, r, err)
	}
}

// errorPage is a small page in the page's language; nothing from the
// sandbox is shown unescaped.
func (s *Server) errorPage(w http.ResponseWriter, ri *reqInfo, code int, title, lead, detail string) {
	admin := ""
	if ri != nil && ri.admin != "" {
		admin = ri.admin
	} else if s.AdminURL != nil {
		admin = s.AdminURL()
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
	w.WriteHeader(code)
	var b strings.Builder
	fmt.Fprintf(&b, `<!doctype html><html lang="ja"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>%s - Iwato</title>`, html.EscapeString(title))
	b.WriteString(`<style>body{font:15px/1.6 system-ui,sans-serif;background:#101216;color:#e6ece9;margin:0;padding:12vh 20px}main{max-width:560px;margin:auto}h1{font-size:20px;margin:0 0 8px}p{margin:6px 0;color:#c4d0ca}code{color:#8ddeba}a{color:#8ddeba}</style></head><body><main>`)
	fmt.Fprintf(&b, "<h1>%s</h1><p>%s</p><p>%s</p>", html.EscapeString(title), html.EscapeString(lead), html.EscapeString(detail))
	if ri != nil {
		fmt.Fprintf(&b, "<p><code>%s:%d</code></p>", html.EscapeString(ri.t.Sandbox), ri.t.Port)
	}
	if admin != "" {
		fmt.Fprintf(&b, `<p><a href="%s">Iwato の画面を開く</a></p>`, html.EscapeString(admin))
	}
	b.WriteString("</main></body></html>\n")
	_, _ = w.Write([]byte(b.String()))
}
