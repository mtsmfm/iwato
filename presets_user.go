package main

import (
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// The owner's presets in the UI (the policy tab's YAML editor): files of
// the owner's folder (setUserPresetDir), so that editing them outside
// Iwato still works.
//
//	POST   /api/presets/user        {yaml}  a new file, ID.yaml
//	GET    /api/presets/user/FILE           {file, yaml}
//	PUT    /api/presets/user/FILE   {yaml}  replaces the file
//	DELETE /api/presets/user/FILE
//
// A file is saved only when it reads as a preset and passes Iwato's
// checks (parsePresetYAML: the parameters, no provider's host, no
// ambiguity with the providers' rules) and its ID is not another file's.

// userPresetFile is a file name the routes take: no directories.
var userPresetFile = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,79}(\.[A-Za-z0-9_-]{1,20})*\.yaml$`)

func userPresetPath(dir, file string) (string, error) {
	if !userPresetFile.MatchString(file) {
		return "", errCode("preset_file_invalid", nil, "ファイル名が正しくありません")
	}
	return filepath.Join(dir, file), nil
}

func userPresetDir() string {
	presetCatalog.RLock()
	defer presetCatalog.RUnlock()
	return presetCatalog.dir
}

// checkUserPreset reads b as the owner's preset to be saved as file: the
// errors as the list shows them.
func checkUserPreset(b []byte, file string) (*networkPreset, error) {
	if len(b) > 256<<10 {
		return nil, errCode("preset_too_large", nil, "256 KiB を超えています")
	}
	p, err := parsePresetYAML(b, "user")
	if err != nil {
		return nil, err
	}
	reloadUserPresets()
	if other := findPreset(p.ID); other != nil && other.File != file {
		return nil, errCode("preset_id_taken", map[string]any{"id": strings.TrimPrefix(p.ID, userPresetPrefix), "file": other.File}, fmt.Sprintf("ID %s は %s が使っています", strings.TrimPrefix(p.ID, userPresetPrefix), other.File))
	}
	return p, nil
}

// writeUserPreset writes the file through a temporary one beside it.
func writeUserPreset(dir, file string, b []byte) error {
	path, err := userPresetPath(dir, file)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".preset-*.tmp")
	if err != nil {
		return err
	}
	_, err = tmp.Write(b)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		os.Remove(tmp.Name())
	}
	return err
}

func (s *Server) handleUserPresets(w http.ResponseWriter, r *http.Request) {
	file, named := strings.CutPrefix(r.URL.Path, "/api/presets/user/")
	if !named && r.URL.Path != "/api/presets/user" {
		fail(w, 404, "unknown route")
		return
	}
	if !s.require(w, r, r.Method != http.MethodGet) {
		return
	}
	dir := userPresetDir()
	if dir == "" {
		failCode(w, 501, "preset_folder_missing", nil, "自分のプリセットを置くフォルダーがありません")
		return
	}
	path, err := userPresetPath(dir, file)
	if named && err != nil {
		failErr(w, 400, err)
		return
	}
	var body struct {
		YAML string `json:"yaml"`
	}
	switch {
	case !named && r.Method == http.MethodPost:
		if decodeLimit(w, r, &body, 300<<10) != nil {
			fail(w, 400, "invalid preset")
			return
		}
		p, err := parsePresetYAML([]byte(body.YAML), "user")
		if err != nil {
			failErr(w, 400, err)
			return
		}
		// The file is named after the ID (a-z, 0-9, -).
		file = strings.TrimPrefix(p.ID, userPresetPrefix) + ".yaml"
		if path, err = userPresetPath(dir, file); err != nil {
			failErr(w, 400, err)
			return
		}
		if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
			failCode(w, 409, "preset_file_exists", map[string]any{"file": file}, file+" がもうあります")
			return
		}
		if _, err := checkUserPreset([]byte(body.YAML), file); err != nil {
			failErr(w, 400, err)
			return
		}
		if err := writeUserPreset(dir, file, []byte(body.YAML)); err != nil {
			failErr(w, 500, err)
			return
		}
	case named && r.Method == http.MethodGet:
		b, err := os.ReadFile(path)
		if err != nil {
			failCode(w, 404, "preset_file_unreadable", map[string]any{"file": file}, file+" を読めません")
			return
		}
		jsonResponse(w, 200, map[string]any{"file": file, "yaml": string(b)})
		return
	case named && r.Method == http.MethodPut:
		if decodeLimit(w, r, &body, 300<<10) != nil {
			fail(w, 400, "invalid preset")
			return
		}
		if _, err := os.Stat(path); err != nil {
			failCode(w, 404, "preset_file_missing", map[string]any{"file": file}, file+" がありません")
			return
		}
		if _, err := checkUserPreset([]byte(body.YAML), file); err != nil {
			failErr(w, 400, err)
			return
		}
		if err := writeUserPreset(dir, file, []byte(body.YAML)); err != nil {
			failErr(w, 500, err)
			return
		}
	case named && r.Method == http.MethodDelete:
		if err := os.Remove(path); err != nil {
			failCode(w, 404, "preset_file_undeletable", map[string]any{"file": file}, file+" を消せません")
			return
		}
	default:
		fail(w, 405, "method not allowed")
		return
	}
	reloadUserPresets()
	jsonResponse(w, 200, map[string]any{"ok": true, "file": file})
}
