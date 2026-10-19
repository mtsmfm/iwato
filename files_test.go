package main

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type davReq struct {
	method, path, body string
	host               string
	user, pass         string
	header             map[string]string
}

func davDo(t *testing.T, s *Server, q davReq) (*http.Response, string) {
	t.Helper()
	r, err := http.NewRequest(q.method, s.origin+q.path, strings.NewReader(q.body))
	if err != nil {
		t.Fatal(err)
	}
	if q.host != "" {
		r.Host = q.host
	}
	if q.user != "" {
		r.SetBasicAuth(q.user, q.pass)
	}
	if q.method == "PROPFIND" {
		r.Header.Set("Depth", "1")
	}
	for k, v := range q.header {
		r.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	return res, string(b)
}

func davExpect(t *testing.T, s *Server, want int, q davReq) string {
	t.Helper()
	res, body := davDo(t, s, q)
	if res.StatusCode != want {
		t.Fatalf("%s %s (host %q): %d, want %d: %s", q.method, q.path, q.host, res.StatusCode, want, body)
	}
	return body
}

type madeKey struct {
	Key    davKey `json:"key"`
	Secret string `json:"secret"`
}

func makeKey(t *testing.T, s *Server, c *http.Cookie, csrf, body string) madeKey {
	t.Helper()
	res, b := request(t, s, "/api/files/keys", "POST", body, c, csrf)
	if res.StatusCode != 200 {
		t.Fatalf("make key: %d %s", res.StatusCode, b)
	}
	var k madeKey
	if err := json.Unmarshal(b, &k); err != nil {
		t.Fatal(err)
	}
	return k
}

func TestFilesKeysStoredHashed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dav-keys.json")
	k, err := loadDavKeys(path)
	if err != nil {
		t.Fatal(err)
	}
	key, secret, err := k.create("phone", davKindPassword, "", false)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if strings.Contains(string(b), secret) || !strings.Contains(string(b), davHash(secret)) {
		t.Fatalf("stored: %s", b)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm()&0077 != 0 {
		t.Fatalf("mode %v", fi.Mode())
	}
	again, err := loadDavKeys(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := again.find(davKindPassword, key.ID, secret); !ok {
		t.Fatal("reloaded key not found")
	}
	if _, ok := again.find(davKindPassword, key.ID, secret+"x"); ok {
		t.Fatal("wrong secret found")
	}
	if _, ok := again.find(davKindURL, "", secret); ok {
		t.Fatal("a password used as a secret URL")
	}
}

type slowBody struct{ parts int }

func (b *slowBody) Read(p []byte) (int, error) {
	if b.parts == 0 {
		return 0, io.EOF
	}
	b.parts--
	time.Sleep(250 * time.Millisecond)
	return copy(p, "chunk "), nil
}
