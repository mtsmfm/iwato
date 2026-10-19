package main

import (
	"context"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
)

// The labels of a persisted path's volume besides ownerKey: the sandbox
// and the path, which the volume's name (a hash of the path) does not tell.
const (
	sandboxKey = "iwato.sandbox"
	pathKey    = "iwato.path"
)

// Volume is a persisted path's volume. Path is empty when unknown (no
// labels, and no configured path matches its name). UsedBy is the
// configured sandbox that mounts it.
type Volume struct {
	Name      string `json:"name"`
	Sandbox   string `json:"sandbox"`
	Path      string `json:"path"`
	CreatedAt string `json:"createdAt,omitempty"`
	UsedBy    string `json:"usedBy,omitempty"`
}

// volumeRuntime lists and removes the persisted paths' volumes. OpenShell
// and Fake implement it.
type volumeRuntime interface {
	Volumes(context.Context) ([]Volume, error)
	RemoveVolume(context.Context, string) error
}

// volumeUser is the configured sandbox that mounts the volume, or "".
func volumeUser(c Config, sandboxes []Sandbox, name string) (sandbox, path string) {
	for _, sb := range sandboxes {
		for _, p := range sb.LegacyPersist {
			if persistVolume(c, sb, p) == name {
				return sb.Name, p
			}
		}
	}
	return "", ""
}

// describeVolumes fills in what the labels do not say: the user, the
// sandbox from the name, and the path when a configured one hashes to it.
func describeVolumes(c Config, sandboxes []Sandbox, list []Volume) []Volume {
	var paths []string
	for _, sb := range sandboxes {
		paths = append(paths, sb.LegacyPersist...)
	}
	for i := range list {
		v := &list[i]
		var p string
		v.UsedBy, p = volumeUser(c, sandboxes, v.Name)
		if v.Sandbox == "" {
			if v.UsedBy != "" {
				v.Sandbox = v.UsedBy
			} else if m := persistName(c).FindStringSubmatch(v.Name); m != nil {
				v.Sandbox = m[1]
			}
		}
		if v.Path == "" {
			v.Path = p
		}
		for _, q := range paths {
			if v.Path == "" && v.Sandbox != "" && persistVolume(c, Sandbox{Name: v.Sandbox}, q) == v.Name {
				v.Path = q
			}
		}
	}
	sort.Slice(list, func(i, j int) bool {
		a, b := list[i], list[j]
		if a.Sandbox != b.Sandbox {
			return a.Sandbox < b.Sandbox
		}
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		return a.Name < b.Name
	})
	return list
}

// persistName matches persistVolume's names; the group is the sandbox.
func persistName(c Config) *regexp.Regexp {
	return regexp.MustCompile(`^` + regexp.QuoteMeta(c.prefix()+"-") + `([a-z][a-z0-9-]{0,31})-persist-[0-9a-f]{8}$`)
}

var volumeNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,254}$`)

// handleVolumes lists the volumes (GET /api/volumes) and removes one that
// no configured sandbox uses (POST /api/volumes/NAME/delete).
func (s *Server) handleVolumes(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/api/volumes"), "/")
	rt, hasRT := s.rt.(volumeRuntime)
	if rest == "" {
		if r.Method != http.MethodGet {
			fail(w, 405, "method not allowed")
			return
		}
		if !s.require(w, r, false) {
			return
		}
		if !hasRT {
			jsonResponse(w, 200, map[string]any{"volumes": []Volume{}, "error": "volumes unavailable"})
			return
		}
		ctx, cancel := timeout(r.Context(), 20*time.Second)
		defer cancel()
		list, e := rt.Volumes(ctx)
		if e != nil {
			jsonResponse(w, 200, map[string]any{"volumes": []Volume{}, "error": safeCLIError(e)})
			return
		}
		s.mu.Lock()
		c := s.config
		s.mu.Unlock()
		jsonResponse(w, 200, map[string]any{"volumes": describeVolumes(c, c.Sandboxes, list)})
		return
	}
	name, ok := strings.CutSuffix(rest, "/delete")
	if r.Method != http.MethodPost || !ok {
		fail(w, 405, "method/operation not allowed")
		return
	}
	if !s.require(w, r, true) {
		return
	}
	if !volumeNamePattern.MatchString(name) {
		fail(w, 400, "invalid volume name")
		return
	}
	if !hasRT {
		fail(w, 501, "volumes unavailable")
		return
	}
	// No sandbox can be created (and mount it) while it is removed.
	s.configMu.Lock()
	defer s.configMu.Unlock()
	s.mu.Lock()
	c := s.config
	s.mu.Unlock()
	if user, _ := volumeUser(c, c.Sandboxes, name); user != "" {
		failCode(w, 409, "volume_in_use", map[string]any{"sandbox": user}, "サンドボックス "+user+" が使っています。先にサンドボックスを削除してください")
		return
	}
	ctx, cancel := timeout(s.ctx, time.Minute)
	defer cancel()
	if e := rt.RemoveVolume(ctx, name); e != nil {
		fail(w, 409, safeCLIError(e))
		return
	}
	jsonResponse(w, 200, map[string]bool{"ok": true})
}
