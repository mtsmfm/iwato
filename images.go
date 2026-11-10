package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	stdlog "log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"iwato/internal/termd"
)

// sandboxImage is Iwato's image for sandboxes (sandboximage/, published by
// image.yml), pulled.
var sandboxImage = imageRef("iwato-sandbox")

// daemonLabel is the image convention's label (docs/adr/0005): an image
// that carries Iwato's daemon names its path (termd.Path) in it. Sandboxes
// of such an image run the daemon as their main process.
const daemonLabel = "iwato.entrypoint"

// imageHasDaemon tells whether the local image ref carries the daemon (its
// daemonLabel). An image that is not here yet (the driver pulls it) or that
// cannot be inspected counts as one without it: its sandboxes use the
// terminals without the daemon (the main process and execs).
// It inspects the image each time (no cache, unlike imageID), so a
// sandbox created right after an update (UpdateImage, the recreate that
// pulls first) sees the new image's label.
func (e *Engine) imageHasDaemon(ctx context.Context, ref string) bool {
	if e == nil {
		return false
	}
	d, err := e.inspect(ctx, "image", ref)
	if err != nil || d == nil {
		return false
	}
	labels := obj(obj(d["Config"])["Labels"])
	if labels == nil {
		labels = obj(d["Labels"])
	}
	return val(labels[daemonLabel]) == termd.Path
}

// ImageState is the image preparation progress shown in the UI.
type ImageState struct {
	Phase   string `json:"phase"` // checking | building | ready | failed (| engine: imagesReady)
	Current string `json:"current,omitempty"`
	// Message is the step (Japanese) or the failure (English: the console
	// prints it); Code and Params name it for the page (web/i18n.js
	// images.state.<code>). A failure's detail can have a code too.
	Message      string         `json:"message,omitempty"`
	Code         string         `json:"code,omitempty"`
	Params       map[string]any `json:"params,omitempty"`
	DetailCode   string         `json:"detailCode,omitempty"`
	DetailParams map[string]any `json:"detailParams,omitempty"`
	Log          string         `json:"log,omitempty"`
}

// prepState is an ImageState with a code (TestErrorCodesHaveStrings).
func prepState(phase, current, code string, params map[string]any, message string) ImageState {
	return ImageState{Phase: phase, Current: current, Code: code, Params: params, Message: message}
}

// prepFailed is the failed state for err: the message English, the
// detail's code when err has one (as it is).
func prepFailed(current, code, what string, err error) ImageState {
	detail := safeCLIError(err)
	st := prepState("failed", current, code, map[string]any{"detail": detail}, what+": "+safeText(english(err)))
	if c := (*codedError)(nil); errors.As(err, &c) && c.msg == err.Error() {
		st.DetailCode, st.DetailParams = c.code, c.params
	}
	return st
}

type imageRecord struct {
	ID string `json:"id"`
	// At is when Iwato last pulled the image.
	At time.Time `json:"at,omitzero"`
}

type imageManager struct {
	mu    sync.Mutex
	state ImageState
}

func (m *imageManager) get() ImageState {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state.Phase == "" {
		return prepState("checking", "", "images_checking", nil, "イメージを確認しています…")
	}
	return m.state
}

func (m *imageManager) set(s ImageState) {
	m.mu.Lock()
	m.state = s
	m.mu.Unlock()
}

// EnsureImage pulls the sandbox image when it is missing (blocking). One
// here is used as it is: a moving tag (edge) is pulled again only on
// request (UpdateImage). dir holds images.json and the pull log.
func (e *Engine) EnsureImage(ctx context.Context, dir string, report func(ImageState)) error {
	logPath := filepath.Join(dir, "image-build.log")
	st := prepState("checking", "sandbox", "images_checking", nil, "イメージを確認しています…")
	st.Log = logPath
	report(st)
	e.forgetImage(sandboxImage)
	if e.imageID(ctx, sandboxImage) != "" {
		return nil
	}
	st = prepState("building", "sandbox", "images_pulling", nil, "イメージを取得しています… 初回は数分かかります")
	st.Log = logPath
	report(st)
	pctx, cancel := context.WithTimeout(ctx, firstPullTimeout)
	defer cancel()
	if err := e.stream(pctx, logPath, "pull "+sandboxImage, "image", "pull", sandboxImage); err != nil {
		return fmt.Errorf("pulling %s failed (see %s): %w", sandboxImage, logPath, err)
	}
	e.forgetImage(sandboxImage)
	return recordImage(dir, readImageRecords(dir), imageRecord{ID: e.imageID(ctx, sandboxImage)})
}

// recordImage notes in images.json that the sandbox image was just pulled.
func recordImage(dir string, records map[string]imageRecord, rec imageRecord) error {
	rec.At = time.Now().UTC()
	records[sandboxImage] = rec
	data, _ := json.MarshalIndent(records, "", "  ")
	return os.WriteFile(filepath.Join(dir, "images.json"), append(data, '\n'), 0600)
}

func readImageRecords(dir string) map[string]imageRecord {
	records := map[string]imageRecord{}
	if data, err := os.ReadFile(filepath.Join(dir, "images.json")); err == nil {
		_ = json.Unmarshal(data, &records)
	}
	if records == nil { // the file held null
		records = map[string]imageRecord{}
	}
	return records
}

// A first pull (minutes on a slow line) and an update end after these, so
// a stalled network does not keep Iwato preparing or updating forever.
const (
	firstPullTimeout  = time.Hour
	updatePullTimeout = 30 * time.Minute
)

// UpdateImage pulls the moving tag (edge) again, on the owner's request
// (the settings' 「標準イメージを更新」 or a recreate that asks for it), and
// reports whether the tag moved. A failure leaves the local image in use.
// The image the tag left is listed as stale, under cleanupMu so that it
// cannot race with CleanupImages, which removes it once no container uses
// it. Sandboxes already created, and those being created or recreated
// during the pull, keep the image they got: the tag moves only when the
// pull is complete. The caller runs one at a time (imageUpdates).
func (e *Engine) UpdateImage(ctx context.Context, dir string) (bool, error) {
	logPath := filepath.Join(dir, "image-build.log")
	e.forgetImage(sandboxImage)
	old := e.imageID(ctx, sandboxImage)
	pctx, cancel := context.WithTimeout(ctx, updatePullTimeout)
	defer cancel()
	if err := e.stream(pctx, logPath, "pull "+sandboxImage+" (update)", "image", "pull", sandboxImage); err != nil {
		return false, fmt.Errorf("pulling %s failed (see %s): %w", sandboxImage, logPath, err)
	}
	e.forgetImage(sandboxImage)
	id := e.imageID(ctx, sandboxImage)
	// The tag has moved once the pull ends: failing to keep the records
	// only loses the pull time or a later cleanup, so it is logged, not
	// reported as a failed update.
	if err := recordImage(dir, readImageRecords(dir), imageRecord{ID: id}); err != nil {
		stdlog.Printf("recording the pull of %s: %v", sandboxImage, english(err))
	}
	if id == "" || id == old {
		return false, nil
	}
	stdlog.Printf("%s updated (%s -> %s)", sandboxImage, old, id)
	if err := e.addStaleImage(dir, old); err != nil {
		stdlog.Printf("registering %s for cleanup: %v", old, english(err))
	}
	return true, nil
}

// stream runs an engine command whose output goes to logPath and Iwato's
// log.
func (e *Engine) stream(ctx context.Context, logPath, title string, args ...string) error {
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	defer log.Close()
	fmt.Fprintf(log, "\n=== %s %s ===\n", time.Now().Format(time.RFC3339), title)
	c := exec.CommandContext(ctx, e.exe, e.args(args)...)
	configureProcess(c)
	out := io.MultiWriter(log, stdlog.Writer())
	c.Stdout, c.Stderr = out, out
	return c.Run()
}

// Images replaced by a rebuild or a pull lose their tag but stay while a
// sandbox still uses them (until the sandbox is deleted). Their IDs are
// kept in stale-images.json and removed once nothing uses them.
const staleImagesFile = "stale-images.json"

var imageIDPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

func readStaleImages(dir string) []string {
	var ids []string
	if data, err := os.ReadFile(filepath.Join(dir, staleImagesFile)); err == nil {
		_ = json.Unmarshal(data, &ids)
	}
	return ids
}

func writeStaleImages(dir string, ids []string) error {
	if ids == nil {
		ids = []string{}
	}
	data, _ := json.MarshalIndent(ids, "", "  ")
	return os.WriteFile(filepath.Join(dir, staleImagesFile), append(data, '\n'), 0600)
}

// addStaleImage lists id in stale-images.json, under the lock CleanupImages
// holds while it rewrites the file.
func (e *Engine) addStaleImage(dir, id string) error {
	e.cleanupMu.Lock()
	defer e.cleanupMu.Unlock()
	return addStaleImage(dir, id)
}

func addStaleImage(dir, id string) error {
	if !imageIDPattern.MatchString(id) {
		return nil
	}
	ids := readStaleImages(dir)
	for _, have := range ids {
		if have == id {
			return nil
		}
	}
	return writeStaleImages(dir, append(ids, id))
}

// CleanupImages removes replaced images that no container uses any more.
// Without --force the engine refuses an image any container, running or
// stopped, was created from; those stay listed for the next pass. An image
// that got a tag again is not Iwato's to remove.
func (e *Engine) CleanupImages(ctx context.Context, dir string) error {
	e.cleanupMu.Lock()
	defer e.cleanupMu.Unlock()
	ids := readStaleImages(dir)
	if len(ids) == 0 {
		return nil
	}
	var keep []string
	for _, id := range ids {
		if !imageIDPattern.MatchString(id) {
			continue
		}
		d, err := e.inspect(ctx, "image", id)
		if err != nil {
			keep = append(keep, id)
			continue
		}
		if d == nil {
			continue
		}
		if tags, _ := d["RepoTags"].([]any); len(tags) > 0 {
			continue
		}
		if _, err = e.run(ctx, "image", "remove", id); err != nil {
			keep = append(keep, id)
		}
	}
	return writeStaleImages(dir, keep)
}

// preparer reports the runtime's own preparation (OpenShell's gateway).
type preparer interface {
	Preparation() ImageState
	RetryPreparation(context.Context) bool
}

// imagesReady is checked before operations that need the image and the
// gateway: the page shows the image's state, then the gateway's.
func (s *Server) imagesReady() (ImageState, bool) {
	// Waiting for the engine (checks.go): the page shows the failed check.
	if s.checks.engine() != nil {
		return ImageState{Phase: "engine"}, false
	}
	st := ImageState{Phase: "ready"}
	if s.images != nil {
		st = s.images.get()
	}
	if p, ok := s.rt.(preparer); ok && st.Phase == "ready" {
		st = p.Preparation()
	}
	return st, st.Phase == "ready"
}

// prepareImages runs at serve startup (and on retry).
func (s *Server) prepareImages(ctx context.Context, e *Engine, dir string) {
	s.images.set(prepState("checking", "", "images_checking", nil, "イメージを確認しています…"))
	if err := e.EnsureImage(ctx, dir, s.images.set); err != nil {
		st := prepFailed("", "images_failed", "the sandbox image could not be prepared", err)
		st.Log = filepath.Join(dir, "image-build.log")
		s.images.set(st)
		stdlog.Println("image preparation failed:", safeText(english(err)))
		return
	}
	s.images.set(ImageState{Phase: "ready"})
	s.cleanupImages()
}

func (s *Server) cleanupImages() {
	if s.imageCleanup != nil {
		go s.imageCleanup()
	}
}
