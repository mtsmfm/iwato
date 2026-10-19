package main

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	pb "github.com/NVIDIA/OpenShell/sdk/go/proto/openshellv1"
)

// Custom provider profiles: whole profiles the owner writes as YAML (in
// OpenShell's format) in the page's editor, or as files in
// <config folder>/profiles/ID.yaml, which Iwato loads when it starts (so
// they can be edited outside Iwato too) and imports with its own profiles.
// The ids starting with iwato- are Iwato's. A custom profile passes the
// same checks as a scoped provider's (checkProfile) before the gateway,
// which lints it again, imports it.

const customProfileGroup = "custom"

// maxCustomProfiles bounds the custom profiles.
const maxCustomProfiles = 40

// parseCustomProfile reads a custom profile and its entry for the page.
func parseCustomProfile(b []byte) (*pb.ProviderProfile, providerProfile, error) {
	if len(b) > maxProfileYAML {
		return nil, providerProfile{}, errCode("yaml_too_large", nil, "YAML が大きすぎます")
	}
	m, err := parseProfileYAML(b)
	if err != nil {
		return nil, providerProfile{}, errCode("profile_yaml_invalid", map[string]any{"detail": err.Error()}, "YAML を読めません: "+err.Error())
	}
	if m.Id == "iwato" || strings.HasPrefix(m.Id, "iwato-") {
		return nil, providerProfile{}, errCode("profile_id_reserved", nil, "iwato- で始まる id は Iwato のプロファイル用です。別の id にしてください")
	}
	if err := checkProfile(m); err != nil {
		return nil, providerProfile{}, err
	}
	if len(m.Credentials) == 0 || len(m.Credentials) > 8 {
		return nil, providerProfile{}, errCode("profile_credentials_count", nil, "credentials は 1 個から 8 個にしてください")
	}
	p := providerProfile{ID: m.Id, Label: truncate(m.DisplayName, 100), Group: customProfileGroup, Note: truncate(m.Description, 500), Custom: true,
		DefaultName: providerNameBase(m.Id), yaml: b}
	for i, c := range m.Credentials {
		if c.Refresh != nil || c.TokenGrant != nil {
			return nil, providerProfile{}, errCode("profile_credential_refresh", map[string]any{"index": i}, fmt.Sprintf("credentials[%d]: Iwato が値を入れるだけの認証情報にしてください (refresh や token_grant は OpenShell の CLI で)", i))
		}
		if len(c.EnvVars) == 0 {
			return nil, providerProfile{}, errCode("profile_credential_env", map[string]any{"index": i}, fmt.Sprintf("credentials[%d]: env_vars を書いてください (サンドボックスに渡る環境変数です)", i))
		}
		label := c.Description
		if label == "" {
			label = c.Name
		}
		p.Credentials = append(p.Credentials, profileCredential{Name: c.Name, Label: truncate(label, 100), Env: slices.Clone(c.EnvVars)})
	}
	return m, p, nil
}

// profileDir is <config folder>/profiles ("" without a config file).
func (s *Server) profileDir() string {
	if s.configPath == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(s.configPath), "profiles")
}

// customProfileFile is the file of a custom profile (the id is checked:
// lowercase letters, digits and hyphens).
func (s *Server) customProfileFile(id string) string {
	dir := s.profileDir()
	file := id + ".yaml"
	if dir == "" || !validProfileID(id) || !filepath.IsLocal(file) {
		return ""
	}
	return filepath.Join(dir, file)
}

// loadProfiles registers the scoped providers' profiles and the custom
// ones before Iwato prepares OpenShell (which imports them).
func (s *Server) loadProfiles() {
	s.mu.Lock()
	scopes := s.config.ProviderScopes
	s.mu.Unlock()
	registerScopes(scopes)
	dir := s.profileDir()
	if dir == "" {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			log.Printf("custom provider profiles: %v", english(err))
		}
		return
	}
	n := 0
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".yaml")
		if !ok || !e.Type().IsRegular() {
			continue
		}
		if n++; n > maxCustomProfiles {
			log.Printf("custom provider profiles: more than %d in %s; the rest are left out", maxCustomProfiles, dir)
			return
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err == nil && len(b) > maxProfileYAML {
			err = errors.New("too large")
		}
		var p providerProfile
		var m *pb.ProviderProfile
		if err == nil {
			m, p, err = parseCustomProfile(b)
		}
		if err == nil && m.Id != id {
			err = fmt.Errorf("its id is %s, not the file's name", m.Id)
		}
		if err != nil {
			log.Printf("custom provider profile %s: %v", e.Name(), english(err))
			continue
		}
		registerProfile(p)
	}
}

// writeProfileFile writes the file through a temporary one.
func writeProfileFile(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".profile-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// handleCustomProfile adds or replaces a custom profile (POST
// /api/provider-profiles with {yaml}) or deletes one (POST
// /api/provider-profiles/ID/delete), refused while a provider uses it.
func (s *Server) handleCustomProfile(w http.ResponseWriter, r *http.Request, rest string) {
	if !s.require(w, r, true) {
		return
	}
	store, ok := s.rt.(profileStore)
	rt, hasRT := s.rt.(providerRuntime)
	if !ok || !hasRT {
		fail(w, 501, "providers unavailable")
		return
	}
	var id string
	if rest != "" {
		var op string
		var cut bool
		if id, op, cut = strings.Cut(rest, "/"); !cut || op != "delete" || !validProfileID(id) {
			fail(w, 404, "unknown profile route")
			return
		}
		if p, ok := profileByID(id); !ok || !p.Custom {
			failCode(w, 404, "profile_custom_missing", map[string]any{"id": id}, "カスタムのプロファイル "+id+" はありません")
			return
		}
	}
	var body struct {
		YAML string `json:"yaml"`
	}
	if decodeLimit(w, r, &body, 2*maxProfileYAML) != nil {
		fail(w, 400, "invalid request")
		return
	}
	s.providerMu.Lock()
	defer s.providerMu.Unlock()
	ctx, cancel := timeout(s.ctx, 60*time.Second)
	defer cancel()
	if id != "" {
		list, _, err := rt.ListProviders(ctx)
		if err != nil {
			fail(w, 409, safeCLIError(err))
			return
		}
		var users []string
		for _, p := range list {
			if p.Profile == id {
				users = append(users, p.Name)
			}
		}
		if len(users) > 0 {
			slices.Sort(users)
			failCode(w, 409, "profile_in_use", map[string]any{"providers": users}, "プロバイダー "+strings.Join(users, ", ")+" が使っています。先に削除してください")
			return
		}
		if err := store.DeleteProviderProfile(ctx, id); err != nil {
			failCode(w, 409, "openshell_refused", map[string]any{"detail": safeCLIError(err)}, "OpenShell が受け付けませんでした: "+safeCLIError(err))
			return
		}
		if f := s.customProfileFile(id); f != "" {
			if err := os.Remove(f); err != nil && !errors.Is(err, os.ErrNotExist) {
				failCode(w, 500, "profile_file_undeletable", map[string]any{"detail": err.Error()}, "ファイルを消せませんでした: "+err.Error())
				return
			}
		}
		unregisterProfile(id)
		jsonResponse(w, 200, map[string]bool{"ok": true})
		return
	}
	b := []byte(body.YAML)
	m, p, err := parseCustomProfile(b)
	if err != nil {
		failErr(w, 400, err)
		return
	}
	// A profile of the id the gateway has and Iwato did not add (one
	// imported with OpenShell's CLI, which providers may use) is not
	// overwritten.
	if p, ok := profileByID(m.Id); !ok || !p.Custom {
		reader, ok := s.rt.(providerProfileReader)
		if !ok {
			fail(w, 501, "providers unavailable")
			return
		}
		existing, err := reader.ProviderProfile(ctx, m.Id)
		if err != nil {
			failCode(w, 409, "profile_unverifiable", map[string]any{"detail": safeCLIError(err)}, "ゲートウェイのプロファイルを確かめられません: "+safeCLIError(err))
			return
		}
		if existing != nil {
			failCode(w, 409, "profile_id_taken", map[string]any{"id": m.Id}, "ゲートウェイにはもう id "+m.Id+" のプロファイルがあります (OpenShell の CLI で取り込んだものなど)。上書きしないので、別の id にしてください")
			return
		}
	}
	// An updated profile changes where its providers' credentials go: the
	// policies of the sandboxes they are attached to are not to change
	// meanwhile (policy_lock.go; providerMu is held already).
	if prt, ok := s.rt.(providerRuntime); ok {
		list, attached, err := s.providerState(ctx, prt)
		if err != nil {
			fail(w, 409, safeCLIError(err))
			return
		}
		var users []string
		for _, sb := range s.sandboxList() {
			if slices.ContainsFunc(list, func(x ProviderInfo) bool { return x.Profile == m.Id && slices.Contains(attached[sb.Name], x.Name) }) {
				users = append(users, sb.Name)
			}
		}
		defer s.lockPolicy(users...)()
	}
	if _, ok := profileByID(m.Id); !ok && len(customProfiles()) >= maxCustomProfiles {
		failCode(w, 409, "profile_custom_too_many", map[string]any{"max": maxCustomProfiles}, fmt.Sprintf("カスタムのプロファイルは %d 個までです", maxCustomProfiles))
		return
	}
	if err := store.PutProviderProfile(ctx, m); err != nil {
		failCode(w, 409, "openshell_refused", map[string]any{"detail": safeCLIError(err)}, "OpenShell が受け付けませんでした: "+safeCLIError(err))
		return
	}
	file := s.customProfileFile(m.Id)
	if file != "" {
		if err := writeProfileFile(file, b); err != nil {
			failCode(w, 500, "profile_file_unsaved", map[string]any{"detail": err.Error()}, "OpenShell には取り込みましたが、ファイルに保存できませんでした: "+err.Error())
			return
		}
	}
	registerProfile(p)
	jsonResponse(w, 200, map[string]any{"ok": true, "id": m.Id, "file": file})
}
