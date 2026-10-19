//go:build uitest

package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"

	"iwato/internal/dav"
)

// DialFiles serves each fake sandbox's files from memory (kept while the
// fake runs, as a stopped sandbox keeps its files).
func (d *Fake) DialFiles(_ context.Context, w Sandbox) (dav.Transport, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.started[w.Name] {
		return nil, dav.ErrStopped
	}
	if d.files == nil {
		d.files = map[string]*dav.MemServer{}
	}
	m := d.files[w.Name]
	if m == nil {
		m = dav.NewMemServer()
		_ = m.WriteFile("README.md", []byte("# "+w.Name+"\n\nIwato の偽バックエンドのファイルです。\n"))
		_ = m.WriteFile("src/main.go", []byte("package main\n\nfunc main() {}\n"))
		// For the editor's quick open (editor.go): a project, a tree the
		// listing skips, and a file it does not open.
		for p, text := range fakeEditorFiles {
			_ = m.WriteFile(p, []byte(text))
		}
		_ = m.WriteFile("bin/tool", []byte("\x7fELF\x00\x00"))
		// For the image preview: a PNG, an SVG and a WebP, and HTML named
		// as a PNG (refused: not an image).
		_ = m.WriteFile("work/iwato/web/icon.png", fakePNG())
		_ = m.WriteFile("work/iwato/web/logo.svg", []byte(fakeSVG))
		webp, _ := base64.StdEncoding.DecodeString("UklGRhoAAABXRUJQVlA4TA0AAAAvAAAAEAcQERGIiP4HAA==")
		_ = m.WriteFile("work/iwato/web/dot.webp", webp)
		_ = m.WriteFile("work/iwato/web/fake.png", []byte("<!doctype html><script>alert(1)</script>"))
		// For the tree: links (to a file, a folder, nowhere).
		_ = m.Symlink("README.md", "work/iwato/LINK.md")
		_ = m.Symlink("work/iwato", "iwato")
		_ = m.Symlink("nowhere", "work/iwato/broken")
		d.files[w.Name] = m
	}
	return m.Transport(), nil
}

// FilesRoot is the home; the "experiment" sandbox stands for one without
// the daemon's SFTP server (the workspace; recreating gives the home).
func (d *Fake) FilesRoot(_ context.Context, w Sandbox) (FilesRoot, error) {
	if w.Name == "experiment" {
		return FilesRoot{Path: "/sandbox"}, nil
	}
	return FilesRoot{Path: homeDir, Home: true}, nil
}

var fakeEditorFiles = map[string]string{
	".bashrc":                       "# ~/.bashrc\nexport EDITOR=vi\n",
	"work/iwato/README.md":          "# Iwato\n\n偽バックエンドのプロジェクトです。\n",
	"work/iwato/main.go":            "package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"こんにちは\")\n}\n",
	"work/iwato/server.go":          "package main\n\n// Server serves the page.\ntype Server struct{}\n",
	"work/iwato/web/app.js":         "'use strict';\nconsole.log('app');\n",
	"work/iwato/web/styles.css":     "body { margin: 0; }\n",
	"work/iwato/docs/editor.md":     "# エディタ\n",
	"work/iwato/windows.txt":        "CRLF の行\r\n2 行目\r\n",
	"work/iwato/node_modules/x.js":  "module.exports = 1;\n",
	"work/iwato/.git/HEAD":          "ref: refs/heads/main\n",
	"work/iwato/.github/ci.yml":     "on: push\n",
	"work/iwato/web/editor.js":      "'use strict';\n",
	"work/iwato/docs/tree/a/b/c.md": "# 深い階層\n",
}

// fakePNG is a 48×32 gradient.
func fakePNG() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 48, 32))
	for y := range 32 {
		for x := range 48 {
			img.Set(x, y, color.RGBA{uint8(x * 5), uint8(y * 8), 160, 255})
		}
	}
	var b bytes.Buffer
	_ = png.Encode(&b, img)
	return b.Bytes()
}

// fakeSVG has a script, which an img (and the raw route's CSP) never runs.
const fakeSVG = `<?xml version="1.0" encoding="UTF-8"?>
<!-- Iwato -->
<svg xmlns="http://www.w3.org/2000/svg" width="120" height="80" viewBox="0 0 120 80">
  <rect width="120" height="80" rx="10" fill="#21382e"/>
  <circle cx="40" cy="40" r="22" fill="#8ddeba"/>
  <text x="70" y="47" font-size="20" fill="#e4f5ec">岩</text>
  <script>document.title = "ran"</script>
</svg>
`
