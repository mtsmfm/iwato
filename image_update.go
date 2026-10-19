package main

import (
	"context"
	stdlog "log"
	"net/http"
	"sync"
	"time"
)

// The standard image in the settings (設定): what is here and since when,
// and for a moving tag (edge) 「標準イメージを更新」, which pulls it again.
// Iwato pulls only a missing image by itself (EnsureImage).

// ImageInfo is the standard image as the settings show it.
type ImageInfo struct {
	Available bool   `json:"available"`
	Image     string `json:"image,omitempty"`
	// Source: pull (from ghcr.io) or build (from the exe's sandboximage/).
	Source string `json:"source,omitempty"`
	// Moves: the tag can move (edge), so that an update can bring a newer one.
	Moves   bool        `json:"moves"`
	Present bool        `json:"present"`
	Created string      `json:"created,omitempty"` // the image's own creation time
	At      string      `json:"at,omitempty"`      // when Iwato last pulled or built it
	Update  ImageUpdate `json:"update"`
}

// ImageUpdate is the last on-demand pull: pulling, updated, current
// (the tag had not moved) or failed.
type ImageUpdate struct {
	Phase   string `json:"phase,omitempty"`
	Message string `json:"message,omitempty"`
	// Detail is a failure's cause (the page words each phase itself).
	Detail string `json:"detail,omitempty"`
	At     string `json:"at,omitempty"`
}

type imageSource interface {
	ImageInfo(context.Context) ImageInfo
	UpdateImage(context.Context) (bool, error)
}

// engineImages is the standard image of WSLc, with images.json in dir.
type engineImages struct {
	e   *Engine
	dir string
}

func (s engineImages) ImageInfo(ctx context.Context) ImageInfo {
	return s.e.ImageInfo(ctx, s.dir)
}

func (s engineImages) UpdateImage(ctx context.Context) (bool, error) {
	return s.e.UpdateImage(ctx, s.dir)
}

// ImageInfo inspects the standard image and reads when Iwato got it.
func (e *Engine) ImageInfo(ctx context.Context, dir string) ImageInfo {
	info := ImageInfo{Available: true, Image: sandboxImage, Source: "build", Moves: imageMoves()}
	if imageRegistry != "" {
		info.Source = "pull"
	}
	d, err := e.inspect(ctx, "image", sandboxImage)
	if err != nil || d == nil {
		return info
	}
	info.Present = true
	info.Created = val(d["Created"])
	if rec, ok := readImageRecords(dir)[sandboxImage]; ok && rec.ID == val(d["Id"]) && !rec.At.IsZero() {
		info.At = rec.At.Format(time.RFC3339)
	}
	return info
}

// imageUpdates runs the on-demand pulls, one at a time, in the background
// (a pull takes minutes): the page polls GET /api/images for the result.
type imageUpdates struct {
	src   imageSource
	mu    sync.Mutex
	state ImageUpdate
}

func (u *imageUpdates) get() ImageUpdate {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.state
}

// start begins a pull unless one is running (then the caller waits for
// that one). updated runs after a pull that moved the tag.
func (u *imageUpdates) start(ctx context.Context, updated func()) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.state.Phase == "pulling" {
		return
	}
	u.state = ImageUpdate{Phase: "pulling", Message: "最新のイメージを取得しています…", At: time.Now().UTC().Format(time.RFC3339)}
	go func() {
		changed, err := u.src.UpdateImage(ctx)
		st := ImageUpdate{Phase: "current", Message: "最新です (取得したイメージは手元のものと同じでした)", At: time.Now().UTC().Format(time.RFC3339)}
		switch {
		case err != nil:
			stdlog.Println("standard image update:", safeText(english(err)))
			st.Phase, st.Message, st.Detail = "failed", "取得できませんでした。手元のイメージを使い続けます: "+safeCLIError(err), safeCLIError(err)
		case changed:
			st.Phase, st.Message = "updated", "新しいイメージを取得しました。これから作る・作り直すサンドボックスで使います"
		}
		u.mu.Lock()
		u.state = st
		u.mu.Unlock()
		if changed {
			updated()
		}
	}()
}

// handleImages answers GET /api/images and POST /api/images/update.
func (s *Server) handleImages(w http.ResponseWriter, r *http.Request) {
	update := r.URL.Path == "/api/images/update"
	switch {
	case update && r.Method != http.MethodPost, !update && r.Method != http.MethodGet:
		fail(w, 405, "method not allowed")
		return
	}
	if !s.require(w, r, update) {
		return
	}
	u := s.imageUpdates
	if u == nil {
		if update {
			fail(w, 501, "image update unavailable")
		} else {
			jsonResponse(w, 200, ImageInfo{})
		}
		return
	}
	if !update {
		info := u.src.ImageInfo(r.Context())
		info.Update = u.get()
		jsonResponse(w, 200, info)
		return
	}
	if !u.src.ImageInfo(r.Context()).Moves {
		failCode(w, 409, "image_fixed_tag", nil, "このイメージのタグは動かないので、更新はありません")
		return
	}
	if !s.requireImages(w) {
		return
	}
	u.start(s.ctx, s.cleanupImages)
	jsonResponse(w, 200, map[string]any{"ok": true, "update": u.get()})
}
