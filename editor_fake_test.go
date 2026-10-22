//go:build uitest

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strings"
	"testing"

	"github.com/pkg/sftp"
)

// The image preview: only images by their content (not their name), under
// a CSP that sandboxes them; the session, the path checks, the size cap.
func TestEditorRaw(t *testing.T) {
	s, c, _ := newFilesServer(t)
	svg := "\xef\xbb\xbf<?xml version=\"1.0\"?>\n<!-- a -->\n<!DOCTYPE svg PUBLIC \"-//W3C//DTD SVG 1.1//EN\" \"x.dtd\">\n<svg xmlns=\"http://www.w3.org/2000/svg\"><script>alert(1)</script></svg>"
	files := map[string]string{
		"i/a.png":    string(fakePNG()),
		"i/a.jpg":    "\xff\xd8\xff\xe0\x00\x10JFIF\x00",
		"i/a.gif":    "GIF89a\x01\x00\x01\x00",
		"i/a.webp":   "RIFF\x1a\x00\x00\x00WEBPVP8L\x0d\x00\x00\x00",
		"i/a.avif":   "\x00\x00\x00\x1cftypavif\x00\x00\x00\x00avifmif1miaf",
		"i/b.avif":   "\x00\x00\x00\x1cftypmif1\x00\x00\x00\x00mif1avifmiaf",
		"i/a.ico":    "\x00\x00\x01\x00\x01\x00\x10\x10",
		"i/a.svg":    svg,
		"i/link.png": "",
		"i/html.png": "<!doctype html><script>alert(1)</script>",
		"i/x.svg":    "<html><svg></svg></html>",
		"i/ent.svg":  "<!DOCTYPE svg [<!ENTITY x \"y\">]><svg/>",
		"i/a.txt":    "hello",
		"i/a.mp4":    "\x00\x00\x00\x18ftypisom\x00\x00\x00\x00isommp41",
		"i/a.pdf":    "%PDF-1.4\n",
		"i/big.png":  "",
	}
	err := s.files.Do(context.Background(), "dev", "test", func(sc *sftp.Client) error {
		if err := sc.MkdirAll("i/dir"); err != nil {
			return err
		}
		for p, data := range files {
			if p == "i/link.png" {
				continue
			}
			f, err := sc.Create(p)
			if err != nil {
				return err
			}
			f.Write([]byte(data))
			if p == "i/big.png" {
				f.Write(fakePNG())
				if err := f.Truncate(maxEditorImageBytes + 1); err != nil {
					return err
				}
			}
			f.Close()
		}
		return sc.Symlink("a.png", "i/link.png")
	})
	if err != nil {
		t.Fatal(err)
	}
	get := func(p string, cookie *http.Cookie) (*http.Response, string) {
		res, b := request(t, s, "/api/sandboxes/dev/editor/raw?path="+url.QueryEscape(p), "GET", "", cookie, "")
		return res, string(b)
	}
	if res, _ := get("i/a.png", nil); res.StatusCode != 401 {
		t.Fatalf("without a session: %d", res.StatusCode)
	}
	for _, p := range []string{"/etc/passwd", "../x", "i/../..", "~/i/a.png", "", "i/dir"} {
		if res, body := get(p, c); res.StatusCode != 400 {
			t.Errorf("%q: %d %s", p, res.StatusCode, body)
		}
	}
	if res, _ := get("i/nope.png", c); res.StatusCode != 404 {
		t.Errorf("missing: %d", res.StatusCode)
	}
	for p, want := range map[string]string{"i/a.png": "image/png", "i/link.png": "image/png", "i/a.jpg": "image/jpeg", "i/a.gif": "image/gif", "i/a.webp": "image/webp", "i/a.avif": "image/avif", "i/b.avif": "image/avif", "i/a.ico": "image/x-icon", "i/a.svg": "image/svg+xml"} {
		res, body := get(p, c)
		h := res.Header
		src := files[p]
		if p == "i/link.png" {
			src = files["i/a.png"]
		}
		if res.StatusCode != 200 || h.Get("Content-Type") != want || body != src {
			t.Errorf("%s: %d %q (%d bytes)", p, res.StatusCode, h.Get("Content-Type"), len(body))
			continue
		}
		if h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Content-Security-Policy") != "default-src 'none'; style-src 'unsafe-inline'; sandbox" || h.Get("Content-Disposition") != "inline" || h.Get("Cache-Control") != "no-store" {
			t.Errorf("%s: headers %v", p, h)
		}
	}
	for _, p := range []string{"i/html.png", "i/x.svg", "i/ent.svg", "i/a.txt", "i/a.mp4", "i/a.pdf"} {
		if res, body := get(p, c); res.StatusCode != 415 || strings.Contains(res.Header.Get("Content-Type"), "image") || strings.Contains(body, "alert") {
			t.Errorf("%s: %d %q %s", p, res.StatusCode, res.Header.Get("Content-Type"), body)
		}
	}
	if res, body := get("i/big.png", c); res.StatusCode != 413 {
		t.Errorf("too big: %d %s", res.StatusCode, body)
	}
	if res, _ := request(t, s, "/api/sandboxes/dev/editor/raw?path=i/a.png", "POST", "", c, ""); res.StatusCode != 405 {
		t.Errorf("POST: %d", res.StatusCode)
	}
}

// The media route: the type from the name's allowlist, Range, the raw
// route's headers; other names and non-files refused.
func TestEditorMedia(t *testing.T) {
	s, c, _ := newFilesServer(t)
	data := make([]byte, 300000)
	for i := range data {
		data[i] = byte(i * 7)
	}
	err := s.files.Do(context.Background(), "dev", "test", func(sc *sftp.Client) error {
		if err := sc.MkdirAll("m/dir.mp4"); err != nil {
			return err
		}
		for _, p := range []string{"m/a.MP4", "m/a.wav", "m/a.html", "m/a"} {
			f, err := sc.Create(p)
			if err != nil {
				return err
			}
			f.Write(data)
			f.Close()
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	get := func(p, rng string, cookie *http.Cookie) (*http.Response, []byte) {
		r, _ := http.NewRequest("GET", s.origin+"/api/sandboxes/dev/editor/media?path="+url.QueryEscape(p), nil)
		if cookie != nil {
			r.AddCookie(cookie)
		}
		if rng != "" {
			r.Header.Set("Range", rng)
		}
		res, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res, b
	}
	if res, _ := get("m/a.wav", "", nil); res.StatusCode != 401 {
		t.Fatalf("without a session: %d", res.StatusCode)
	}
	res, b := get("m/a.MP4", "bytes=200000-200099", c)
	h := res.Header
	if res.StatusCode != 206 || !bytes.Equal(b, data[200000:200100]) || h.Get("Content-Range") != "bytes 200000-200099/300000" || h.Get("Content-Type") != "video/mp4" {
		t.Errorf("range: %d %v (%d bytes)", res.StatusCode, h, len(b))
	}
	if h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Content-Security-Policy") != "default-src 'none'; style-src 'unsafe-inline'; sandbox" || h.Get("Content-Disposition") != "inline" {
		t.Errorf("headers: %v", h)
	}
	if res, b := get("m/a.wav", "", c); res.StatusCode != 200 || !bytes.Equal(b, data) || res.Header.Get("Content-Type") != "audio/wav" {
		t.Errorf("whole: %d %q (%d bytes)", res.StatusCode, res.Header.Get("Content-Type"), len(b))
	}
	for p, want := range map[string]int{"m/a.html": 415, "m/a": 415, "m/dir.mp4": 400, "m/nope.mp4": 404, "../x.mp4": 400, "/etc/x.mp4": 400} {
		if res, b := get(p, "", c); res.StatusCode != want || strings.HasPrefix(res.Header.Get("Content-Type"), "video") || bytes.Contains(b, data[:50]) {
			t.Errorf("%s: %d %q", p, res.StatusCode, res.Header.Get("Content-Type"))
		}
	}
}

// The tree's directory listing: the session, the same path checks as the
// other routes, directories first then by name (any case), links said for
// what they lead to, heavy folders marked, the cap, a stopped sandbox.
func TestEditorDir(t *testing.T) {
	s, c, csrf := newFilesServer(t)
	err := s.files.Do(context.Background(), "dev", "test", func(sc *sftp.Client) error {
		for _, p := range []string{"t/b.txt", "t/A.md", "t/zdir/x", "t/Cdir/y", "t/node_modules/z.js"} {
			if err := sc.MkdirAll(path.Dir(p)); err != nil {
				return err
			}
			f, err := sc.Create(p)
			if err != nil {
				return err
			}
			f.Write([]byte("hello"))
			f.Close()
		}
		if err := sc.Symlink("b.txt", "t/link"); err != nil {
			return err
		}
		return sc.Symlink("zdir", "t/dlink")
	})
	if err != nil {
		t.Fatal(err)
	}
	get := func(q string, cookie *http.Cookie) (int, string) {
		res, b := request(t, s, "/api/sandboxes/dev/editor/dir?path="+url.QueryEscape(q), "GET", "", cookie, "")
		return res.StatusCode, string(b)
	}
	if code, _ := get("t", nil); code != 401 {
		t.Fatalf("without a session: %d", code)
	}
	for _, q := range []string{"/etc", "../x", "t/../..", "~/t", "t/b.txt", "nope"} {
		if code, body := get(q, c); code != 400 {
			t.Errorf("%q: %d %s", q, code, body)
		}
	}
	code, body := get("t/", c)
	var d editorDir
	if code != 200 || json.Unmarshal([]byte(body), &d) != nil {
		t.Fatalf("list: %d %s", code, body)
	}
	var got []string
	for _, e := range d.Entries {
		got = append(got, fmt.Sprintf("%s:%s:%s:%d:%v", e.Name, e.Kind, e.To, e.Size, e.Heavy))
	}
	want := []string{"Cdir:dir::0:false", "node_modules:dir::0:true", "zdir:dir::0:false", "A.md:file::5:false", "b.txt:file::5:false", "dlink:symlink:dir:0:false", "link:symlink:file:5:false"}
	if d.Path != "t" || d.Total != 7 || d.Truncated || !slices.Equal(got, want) {
		t.Fatalf("list: %+v\n got %q\nwant %q", d, got, want)
	}
	if code, body := get("", c); code != 200 || !strings.Contains(body, `"name":"t","kind":"dir"`) {
		t.Fatalf("the root: %d %s", code, body)
	}
	err = s.files.Do(context.Background(), "dev", "test", func(sc *sftp.Client) error {
		v, err := readEditorDir(sc, "t", 2)
		if err == nil && (!v.Truncated || v.Total != 7 || len(v.Entries) != 2 || v.Entries[1].Name != "node_modules") {
			err = fmt.Errorf("capped: %+v", v)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if res, _ := request(t, s, "/api/sandboxes/nope/editor/dir?path=t", "GET", "", c, ""); res.StatusCode != 404 {
		t.Fatalf("unknown sandbox: %d", res.StatusCode)
	}
	if res, b := request(t, s, "/api/sandboxes/dev/stop", "POST", "", c, csrf); res.StatusCode != 200 {
		t.Fatal(res.StatusCode, string(b))
	}
	if code, body := get("t", c); code != 409 {
		t.Fatalf("stopped: %d %s", code, body)
	}
}
