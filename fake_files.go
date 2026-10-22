//go:build uitest

package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"math"

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
		// For the media player: a tone, a clip (VP9 in MP4, which Chromium
		// builds without H.264 play too), and one no browser plays.
		_ = m.WriteFile("work/iwato/media/tone.wav", fakeWAV())
		clip, _ := base64.StdEncoding.DecodeString(fakeClip)
		_ = m.WriteFile("work/iwato/media/clip.mp4", clip)
		_ = m.WriteFile("work/iwato/media/broken.mp4", []byte("not a video"))
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
	// For the Markdown preview: GFM, a relative image, and raw HTML that
	// must not run.
	"work/iwato/docs/preview.md": "# プレビュー\n\n| 名前 | 値 |\n|---|---|\n| a | 1 |\n| b | **2** |\n\n" +
		"- [x] 済み\n- [ ] まだ\n\n~~取り消し~~ https://example.com/ と [リンク](https://example.com/docs)、[相対リンク](../README.md)\n\n" +
		"```go\nfunc main() {}\n```\n\n![アイコン](../web/icon.png) ![外部の画像](https://example.com/x.png)\n\n" +
		"<script>window.parent.__pwned = 1</script>\n\n<img src=x onerror=\"window.parent.__pwned = 2\">\n",
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

// fakeWAV is half a second of a 440 Hz tone (8 kHz, 8-bit mono PCM).
func fakeWAV() []byte {
	const rate, n = 8000, 4000
	var b bytes.Buffer
	b.WriteString("RIFF")
	_ = binary.Write(&b, binary.LittleEndian, uint32(36+n))
	b.WriteString("WAVEfmt ")
	for _, v := range []any{uint32(16), uint16(1), uint16(1), uint32(rate), uint32(rate), uint16(1), uint16(8)} {
		_ = binary.Write(&b, binary.LittleEndian, v)
	}
	b.WriteString("data")
	_ = binary.Write(&b, binary.LittleEndian, uint32(n))
	for i := range n {
		b.WriteByte(byte(128 + 60*math.Sin(2*math.Pi*440*float64(i)/rate)))
	}
	return b.Bytes()
}

// fakeClip is a 64×36, 0.4 s solid colour clip (ffmpeg, libvpx-vp9).
const fakeClip = "AAAAHGZ0eXBpc29tAAACAGlzb21pc28ybXA0MQAAAvZtb292AAAAbG12aGQAAAAAAAAAAAAAAAAAAAPoAAABkAABAAABAAAAAAAAAAAAAAAAAQAAAAAAAAAAAAAAAAAAAAEAAAAAAAAAAAAAAAAAAEAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAACAAACRXRyYWsAAABcdGtoZAAAAAMAAAAAAAAAAAAAAAEAAAAAAAABkAAAAAAAAAAAAAAAAAAAAAAAAQAAAAAAAAAAAAAAAAAAAAEAAAAAAAAAAAAAAAAAAEAAAAAAQAAAACQAAAAAACRlZHRzAAAAHGVsc3QAAAAAAAAAAQAAAZAAAAAAAAEAAAAAAb1tZGlhAAAAIG1kaGQAAAAAAAAAAAAAAAAAACgAAAAQAFXEAAAAAAAtaGRscgAAAAAAAAAAdmlkZQAAAAAAAAAAAAAAAFZpZGVvSGFuZGxlcgAAAAFobWluZgAAABR2bWhkAAAAAQAAAAAAAAAAAAAAJGRpbmYAAAAcZHJlZgAAAAAAAAABAAAADHVybCAAAAABAAABKHN0YmwAAACoc3RzZAAAAAAAAAABAAAAmHZwMDkAAAAAAAAAAQAAAAAAAAAAAAAAAAAAAAAAQAAkAEgAAABIAAAAAAAAAAEPTGF2YyBsaWJ2cHgtdnA5AAAAAAAAAAAAAAAAAAAAAAAY//8AAAAUdnBjQwEAAAAACoICAgIAAAAAAApmaWVsAQAAAAAQcGFzcAAAAAEAAAABAAAAFGJ0cnQAAAAAAAAEYAAABGAAAAAYc3R0cwAAAAAAAAABAAAAAgAACAAAAAAUc3RzcwAAAAAAAAABAAAAAQAAABxzdHNjAAAAAAAAAAEAAAABAAAAAgAAAAEAAAAcc3RzegAAAAAAAAAAAAAAAgAAACkAAAAPAAAAFHN0Y28AAAAAAAAAAQAAAyIAAAA9dWR0YQAAADVtZXRhAAAAAAAAACFoZGxyAAAAAAAAAABtZGlyYXBwbAAAAAAAAAAAAAAAAAhpbHN0AAAACGZyZWUAAABAbWRhdIJJg0IAA/ACNgA4JBwYSgAAMGAAABO///OvIABH////kgh/////V7AAhgBAkpwAUAAAAyAAAFRw"

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
