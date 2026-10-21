package main

import (
	"context"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"
	_ "time/tzdata" // validation on hosts without a zoneinfo (Windows)
)

// The sandboxes' time zone (Config.Timezone): "" follows the host's, an
// IANA name sets that zone. The terminals get it as TZ (termEnvContent).

const utcZone = "Etc/UTC"

var tzName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_+-]*(/[A-Za-z0-9][A-Za-z0-9_+-]*)*$`)

// normalTimezone is the current IANA name of a zone name, false for one
// that is not a zone.
func normalTimezone(name string) (string, bool) {
	if len(name) > 64 || !tzName.MatchString(name) || name == "Local" {
		return "", false
	}
	if _, err := time.LoadLocation(name); err != nil {
		return "", false
	}
	if n, ok := tzAliases[name]; ok {
		return n, true
	}
	return name, true
}

// hostTimezone is the host's zone as an IANA name (UTC when unknown).
func hostTimezone() string {
	raw := hostZoneName()
	if raw == "" {
		return utcZone
	}
	if z, ok := normalTimezone(raw); ok {
		return z
	}
	log.Printf("time zone: unknown host zone %q, using UTC", raw)
	return utcZone
}

// sandboxTimezone is the zone the sandboxes get for the setting.
func sandboxTimezone(setting string) string {
	if setting == "" {
		return hostTimezone()
	}
	if z, ok := normalTimezone(setting); ok {
		return z
	}
	return utcZone
}

// zoneFromPath is the zone name in a zoneinfo path
// (/usr/share/zoneinfo/Asia/Tokyo, /var/db/timezone/zoneinfo/Asia/Tokyo).
func zoneFromPath(p string) string {
	i := strings.LastIndex(p, "zoneinfo")
	if i < 0 {
		return ""
	}
	rest := p[i+len("zoneinfo"):]
	if j := strings.IndexByte(rest, '/'); j >= 0 {
		rest = rest[j+1:]
	} else {
		return ""
	}
	for _, d := range []string{"posix/", "right/"} {
		rest = strings.TrimPrefix(rest, d)
	}
	return rest
}

// windowsZone is the IANA name of a Windows time zone key name.
func windowsZone(key string) (string, bool) {
	z, ok := windowsZones[strings.TrimSpace(strings.TrimRight(key, "\x00"))]
	return z, ok
}

// timezoneRuntime is a runtime that applies the setting (OpenShell).
type timezoneRuntime interface {
	SetTimezone(setting string)
	RewriteTermEnv(ctx context.Context, names []string)
}

// handleTimezone reads (GET) and sets (POST) the setting; the host's zone
// is shown for "".
func (s *Server) handleTimezone(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if !s.require(w, r, false) {
			return
		}
		s.mu.Lock()
		tz := s.config.Timezone
		s.mu.Unlock()
		jsonResponse(w, 200, map[string]string{"timezone": tz, "host": hostTimezone()})
	case http.MethodPost:
		if !s.require(w, r, true) {
			return
		}
		var body struct {
			Timezone string `json:"timezone"`
		}
		if decode(w, r, &body) != nil {
			fail(w, 400, "invalid request")
			return
		}
		if _, ok := normalTimezone(body.Timezone); body.Timezone != "" && !ok {
			failCode(w, 400, "timezone_invalid", map[string]any{"name": body.Timezone}, "タイムゾーンの名前が正しくありません: "+body.Timezone)
			return
		}
		if err := s.updateConfig(func(c *Config) error { c.Timezone = body.Timezone; return nil }); err != nil {
			failErr(w, 400, err)
			return
		}
		s.applyTimezone(body.Timezone)
		jsonResponse(w, 200, map[string]bool{"ok": true})
	default:
		w.Header().Set("Allow", "GET, POST")
		fail(w, 405, "method not allowed")
	}
}

// applyTimezone hands the setting to the runtime, which rewrites the
// terminals' environment of the running sandboxes in the background.
func (s *Server) applyTimezone(setting string) {
	rt, ok := s.rt.(timezoneRuntime)
	if !ok {
		return
	}
	rt.SetTimezone(setting)
	var names []string
	for _, w := range s.sandboxList() {
		names = append(names, w.Name)
	}
	if len(names) == 0 {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(s.ctx), 2*time.Minute)
		defer cancel()
		rt.RewriteTermEnv(ctx, names)
	}()
}
