//go:build uitest

package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// 「標準イメージを更新」 through the API: a CSRF POST pulls in the background,
// the replaced image is listed as stale and cleaned up once unused, and GET
// reports the result.
func TestImageUpdateOnDemand(t *testing.T) {
	pullEdge(t)
	idA, idB := "sha256:"+strings.Repeat("a", 64), "sha256:"+strings.Repeat("b", 64)
	id := idA
	e, pulls := fakeImageEngine(t, &id, idB)
	dir := t.TempDir()
	s := newServer(testConfig(), NewFake(), true, "127.0.0.1:29999")
	s.imageUpdates = &imageUpdates{src: engineImages{e, dir}}
	cleaned := make(chan bool, 1)
	s.imageCleanup = func() { cleaned <- true }
	cookie, csrf := reviewLogin(t, s)
	if c := reviewRequest(s, "POST", "/api/images/update", "", s.origin, "same-origin", s.host, cookie, "").Code; c != 403 || pulls() != 0 {
		t.Fatal("update without CSRF:", c)
	}
	if w := reviewRequest(s, "POST", "/api/images/update", "", s.origin, "same-origin", s.host, cookie, csrf); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var info ImageInfo
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		w := reviewRequest(s, "GET", "/api/images", "", "", "", s.host, cookie, "")
		_ = json.Unmarshal(w.Body.Bytes(), &info)
		if info.Update.Phase != "pulling" || time.Now().After(deadline) {
			break
		}
	}
	if info.Update.Phase != "updated" || !info.Moves || pulls() != 1 {
		t.Fatalf("%+v (pulls %d)", info, pulls())
	}
	select {
	case <-cleaned:
	case <-time.After(5 * time.Second):
		t.Fatal("no cleanup after the update")
	}
	if stale := readStaleImages(dir); len(stale) != 1 || stale[0] != idA {
		t.Fatal("replaced image not stale:", stale)
	}
}
