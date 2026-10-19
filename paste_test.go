//go:build uitest

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/pkg/sftp"
)

// TestPasteImage goes through the endpoint to the fake's SFTP server: the
// image is written under the home's pasteDir and its absolute path
// answered; wrong types, sizes, a missing CSRF token and a stopped sandbox
// are refused; old images are pruned.
func TestPasteImage(t *testing.T) {
	s, c, csrf := newFilesServer(t)
	post := func(name, ctype, token string, body []byte) (int, string) {
		t.Helper()
		r, _ := http.NewRequest("POST", s.origin+"/api/sandboxes/"+name+"/paste-image", bytes.NewReader(body))
		r.Header.Set("Origin", s.origin)
		r.Header.Set("Content-Type", ctype)
		r.Header.Set("X-CSRF-Token", token)
		r.AddCookie(c)
		res, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	read := func(rel string) ([]byte, error) {
		var data []byte
		err := s.files.Do(context.Background(), "dev", "test", func(c *sftp.Client) error {
			f, err := c.Open(rel)
			if err != nil {
				return err
			}
			defer f.Close()
			data, err = io.ReadAll(f)
			return err
		})
		return data, err
	}
	png := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{7}, 100)...)

	// Older pastes: 60 recent ones (the newest 50 stay with the new one,
	// so 49 of them), one 8 days old, and a file of the owner's.
	now := time.Now().UTC()
	var recent []string
	for i := range 60 {
		name := fmt.Sprintf("%s-%08x.png", now.Add(-time.Duration(60-i)*time.Minute).Format(pasteTimeLayout), i)
		recent = append(recent, name)
	}
	old := now.Add(-8*24*time.Hour).Format(pasteTimeLayout) + "-0000abcd.jpg"
	err := s.files.Do(context.Background(), "dev", "test", func(c *sftp.Client) error {
		if err := c.MkdirAll(pasteDir); err != nil {
			return err
		}
		for _, name := range append(recent, old, "mine.txt") {
			f, err := c.Create(pasteDir + "/" + name)
			if err != nil {
				return err
			}
			f.Close()
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	code, body := post("dev", "image/png", csrf, png)
	if code != 200 {
		t.Fatalf("paste: %d %s", code, body)
	}
	var got struct{ Path string }
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err)
	}
	rel, ok := strings.CutPrefix(got.Path, homeDir+"/")
	if !ok || !strings.HasPrefix(rel, pasteDir+"/") || !pasteName.MatchString(strings.TrimPrefix(rel, pasteDir+"/")) || !strings.HasSuffix(rel, ".png") {
		t.Fatalf("path: %q", got.Path)
	}
	if data, err := read(rel); err != nil || !bytes.Equal(data, png) {
		t.Fatalf("written: %v %q", err, data)
	}
	for i, name := range recent {
		_, err := read(pasteDir + "/" + name)
		if kept := i >= len(recent)-(pasteKeep-1); kept != (err == nil) {
			t.Fatalf("%s (%d): kept %v, err %v", name, i, kept, err)
		}
	}
	if _, err := read(pasteDir + "/" + old); !os.IsNotExist(err) {
		t.Fatalf("8 days old: %v", err)
	}
	if _, err := read(pasteDir + "/mine.txt"); err != nil {
		t.Fatalf("the owner's file: %v", err)
	}

	// A JPEG declared as a PNG is named for what it is.
	jpeg := append([]byte("\xff\xd8\xff\xe0"), bytes.Repeat([]byte{1}, 20)...)
	if code, body := post("dev", "image/png", csrf, jpeg); code != 200 || !strings.Contains(body, `.jpg"`) {
		t.Fatalf("jpeg: %d %s", code, body)
	}
	for _, tc := range []struct {
		name, ctype, token string
		body               []byte
		want               int
	}{
		{"dev", "image/svg+xml", csrf, []byte("<svg/>"), 415},
		{"dev", "image/png", csrf, []byte("<html>not an image"), 415},
		{"dev", "image/png", csrf, append(png, make([]byte, maxPasteBytes)...), 413},
		{"dev", "image/png", "", png, 403},
		{"nope", "image/png", csrf, png, 404},
	} {
		if code, body := post(tc.name, tc.ctype, tc.token, tc.body); code != tc.want {
			t.Errorf("%s %s: %d, want %d: %s", tc.name, tc.ctype, code, tc.want, body)
		}
	}
	if res, b := request(t, s, "/api/sandboxes/dev/stop", "POST", "", c, csrf); res.StatusCode != 200 {
		t.Fatal(res.StatusCode, string(b))
	}
	if code, body := post("dev", "image/png", csrf, png); code != 409 {
		t.Fatalf("stopped: %d %s", code, body)
	}
}
