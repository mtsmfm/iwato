package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"
)

// The sandbox list, the ingress settings and the time zone are editable
// while serve runs; the other fields are fixed.

func (s *Server) sandboxList() []Sandbox {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Sandbox(nil), s.config.Sandboxes...)
}

func (s *Server) lookupSandbox(name string) (Sandbox, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.config.Sandbox(name)
}

// updateConfig changes the configuration as updateSandboxes does (the
// ingress settings, the time zone, the resource view and the sign-in
// limits).
func (s *Server) updateConfig(change func(*Config) error) error {
	s.configMu.Lock()
	defer s.configMu.Unlock()
	s.mu.Lock()
	next := s.config
	s.mu.Unlock()
	if err := change(&next); err != nil {
		return err
	}
	if err := next.Validate(); err != nil {
		return err
	}
	if s.configPath != "" {
		if err := writeConfig(s.configPath, next); err != nil {
			return fmt.Errorf("saving %s: %w", s.configPath, err)
		}
	}
	s.mu.Lock()
	s.config.Ingress = next.Ingress
	s.config.Timezone = next.Timezone
	s.config.Resources = next.Resources
	s.config.Sessions = next.Sessions
	s.mu.Unlock()
	return nil
}

// updateSandboxes validates the resulting configuration, persists it (when
// serve has a config file) and only then makes it visible.
func (s *Server) updateSandboxes(change func([]Sandbox) ([]Sandbox, error)) error {
	s.configMu.Lock()
	defer s.configMu.Unlock()
	s.mu.Lock()
	next := s.config
	next.Sandboxes = append([]Sandbox(nil), s.config.Sandboxes...)
	s.mu.Unlock()
	list, err := change(next.Sandboxes)
	if err != nil {
		return err
	}
	next.Sandboxes = list
	if err = next.Validate(); err != nil {
		return err
	}
	if s.configPath != "" {
		if err = writeConfig(s.configPath, next); err != nil {
			return fmt.Errorf("saving %s: %w", s.configPath, err)
		}
	}
	s.mu.Lock()
	s.config.Sandboxes = list
	s.mu.Unlock()
	return nil
}

// writeConfig replaces the file atomically (same-directory rename).
func writeConfig(path string, c Config) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".sandboxes-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(append(data, '\n')); err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// ensureConfig creates a configuration without sandboxes when none exists.
func ensureConfig(path string) (bool, error) {
	if _, err := os.Stat(path); err == nil || !os.IsNotExist(err) {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return false, err
	}
	data, _ := json.MarshalIndent(defaultConfig(), "", "  ")
	if err := writeExclusive(path, append(data, '\n')); err != nil {
		return false, err
	}
	return true, nil
}

// errSandboxExists refuses a second sandbox under a name.
var errSandboxExists = errors.New("同じ名前のサンドボックスがあります")

// engineCPUs is how many CPUs the sandboxes' engine has: as a running
// sandbox counted them, else as `docker info` said, else this PC's (WSL
// gives its VM all of them by default).
func (s *Server) engineCPUs() int {
	if n := s.resources.hostCPUs(); n > 0 {
		return n
	}
	if n := s.checks.engineCPUs(); n > 0 {
		return n
	}
	return runtime.NumCPU()
}

// checkCPUs refuses a CPU limit above the engine's CPUs, which Docker
// refuses only when it creates the container.
func (s *Server) checkCPUs(n int) error {
	limit := s.engineCPUs()
	if n < 0 || n > limit {
		return errCode("cpus_invalid", map[string]any{"max": limit}, fmt.Sprintf("CPU の上限は 1〜%d コアにしてください", limit))
	}
	return nil
}

// handleCreateSandbox adds a sandbox: its image, presets and providers.
// Only its home persists (the image convention): a request naming paths to
// persist is refused unless empty. Nothing is started; 起動 creates it in
// OpenShell.
func (s *Server) handleCreateSandbox(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, true) {
		return
	}
	var body struct {
		Name    string   `json:"name"`
		Image   string   `json:"image"`
		Persist []string `json:"persist"`
		// Presets are network presets (presets.go) the first policy has.
		Presets []PresetRef `json:"presets"`
		// Providers are attached when OpenShell creates the sandbox.
		Providers []string `json:"providers"`
		// GPU requests the machine's GPU (resources.gpu).
		GPU bool `json:"gpu"`
		// CPUs is the CPU limit; 0 is none.
		CPUs int `json:"cpus"`
	}
	if decode(w, r, &body) != nil {
		fail(w, 400, "invalid request")
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	if !safeID.MatchString(body.Name) {
		failCode(w, 400, "name_invalid", map[string]any{"max": 32}, "名前は英小文字で始まり、英小文字・数字・ハイフンの 32 文字以内にしてください")
		return
	}
	body.Image = strings.TrimSpace(body.Image)
	if body.Image != "" && !validImage(body.Image) {
		failCode(w, 400, "image_invalid", nil, "イメージの指定が正しくありません")
		return
	}
	if len(body.Persist) > 0 {
		failCode(w, 400, "persist_unsupported", nil, "永続化するパスは選べません。ホーム (/home/dev) の下だけが残ります")
		return
	}
	if body.GPU && !s.gpuAvailable(r.Context()) {
		failCode(w, 400, "gpu_unavailable", nil, "この環境では GPU を使えません")
		return
	}
	if err := s.checkCPUs(body.CPUs); err != nil {
		failErr(w, 400, err)
		return
	}
	body.Presets = withRequiredPresets(body.Presets)
	if err := validPresetList(body.Presets); err != nil {
		failErr(w, 400, err)
		return
	}
	if err := validProviderList(body.Providers); err != nil {
		fail(w, 400, "プロバイダー: "+err.Error())
		return
	}
	var chosen []ProviderInfo
	if len(body.Providers) > 0 {
		rt, ok := s.rt.(providerRuntime)
		if !ok {
			fail(w, 501, "providers unavailable")
			return
		}
		// Held until the sandbox is in the configuration, so that none of
		// its providers is deleted meanwhile.
		s.providerMu.Lock()
		defer s.providerMu.Unlock()
		ctx, cancel := timeout(r.Context(), 20*time.Second)
		defer cancel()
		list, _, err := rt.ListProviders(ctx)
		if err != nil {
			fail(w, 409, safeCLIError(err))
			return
		}
		if err := checkProviders(list, body.Providers); err != nil {
			failErr(w, 409, err)
			return
		}
		// Providers whose rules disagree on an endpoint (OpenShell would
		// refuse to compose them).
		for _, name := range body.Providers {
			chosen = append(chosen, list[slices.IndexFunc(list, func(p ProviderInfo) bool { return p.Name == name })])
		}
		if vs := s.providersAmbiguities(ctx, chosen); len(vs) > 0 {
			failCheck(w, errRefused("ambiguous_providers", nil, ambiguityMessage("この組み合わせのプロバイダーは OpenShell に拒否されます", vs, "どちらかだけを選んでください。"), vs, nil))
			return
		}
		// A policy OpenShell already has under this name (normally none:
		// a new sandbox starts without network rules).
		for _, name := range body.Providers {
			i := slices.IndexFunc(list, func(p ProviderInfo) bool { return p.Name == name })
			// The presets' rules are checked below, so only the
			// gateway's policy is checked here.
			conflicts, err := s.conflictsForAttach(ctx, newSandbox(body.Name), list[i])
			if err != nil {
				failCode(w, 409, "policy_unverifiable", map[string]any{"detail": safeCLIError(err)}, "ポリシーを確かめられません: "+safeCLIError(err))
				return
			}
			ambiguities, err := s.attachAmbiguities(ctx, newSandbox(body.Name), list[i])
			if err != nil {
				failCode(w, 409, "policy_unverifiable", map[string]any{"detail": safeCLIError(err)}, "ポリシーを確かめられません: "+safeCLIError(err))
				return
			}
			if len(conflicts) > 0 || len(ambiguities) > 0 {
				failCheck(w, attachRefusal(name, conflicts, ambiguities))
				return
			}
		}
	} else {
		body.Providers = nil
	}
	// The presets with each other and with the providers.
	if err := s.presetsAmbiguities(r.Context(), body.Presets, chosen); err != nil {
		failCheck(w, err)
		return
	}
	err := s.updateSandboxes(func(list []Sandbox) ([]Sandbox, error) {
		for _, sb := range list {
			if sb.Name == body.Name {
				return nil, errSandboxExists
			}
		}
		sb := newSandbox(body.Name)
		sb.Image, sb.Providers, sb.Presets, sb.GPU, sb.CPUs = body.Image, body.Providers, body.Presets, body.GPU, body.CPUs
		return append(list, sb), nil
	})
	if errors.Is(err, errSandboxExists) {
		failCode(w, 409, "sandbox_exists", nil, err.Error())
		return
	}
	if err != nil {
		failErr(w, 409, err)
		return
	}
	jsonResponse(w, 200, map[string]any{"ok": true, "name": body.Name, "createdAt": time.Now().UTC()})
}

func (s *Server) forgetSandbox(name string) error {
	return s.updateSandboxes(func(list []Sandbox) ([]Sandbox, error) {
		out := list[:0]
		for _, sb := range list {
			if sb.Name != name {
				out = append(out, sb)
			}
		}
		return out, nil
	})
}

// defaultConfigPath is where iwato.json is without -config (ADR 0027): in
// the folder Iwato starts in when one is there (where it always was before),
// else in the user's config folder, as OpenShell keeps its own: Windows'
// %LOCALAPPDATA% (not the roaming %APPDATA%: the sandboxes are this PC's),
// $XDG_CONFIG_HOME or ~/.config elsewhere, macOS included.
func defaultConfigPath() (string, error) {
	if _, err := os.Stat("iwato.json"); err == nil {
		return "iwato.json", nil
	}
	dir, err := userConfigDir(runtime.GOOS, os.Getenv, os.UserHomeDir)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "iwato", "iwato.json"), nil
}

func userConfigDir(goos string, getenv func(string) string, home func() (string, error)) (string, error) {
	if goos == "windows" {
		if d := getenv("LOCALAPPDATA"); d != "" {
			return d, nil
		}
		return "", errors.New("LOCALAPPDATA is not set: start Iwato with -config")
	}
	// The XDG spec ignores a relative value.
	if d := getenv("XDG_CONFIG_HOME"); filepath.IsAbs(d) {
		return d, nil
	}
	h, err := home()
	if err != nil {
		return "", fmt.Errorf("no home folder (%v): start Iwato with -config", err)
	}
	return filepath.Join(h, ".config"), nil
}
