//go:build uitest

package main

import (
	"encoding/json"
	"fmt"
	"testing"
)

func TestLogRouteNeedsASession(t *testing.T) {
	s := newServer(testConfig(), NewFake(), true, "127.0.0.1:29999")
	fmt.Fprintln(&s.logs, "gateway: up")
	if c := reviewRequest(s, "GET", "/api/log", "", "", "", s.host, nil, "").Code; c != 401 {
		t.Fatal(c)
	}
	cookie, _ := reviewLogin(t, s)
	w := reviewRequest(s, "GET", "/api/log?after=0", "", "", "", s.host, cookie, "")
	var v struct {
		Lines []string
		Next  int
	}
	if json.Unmarshal(w.Body.Bytes(), &v); w.Code != 200 || len(v.Lines) != 1 || v.Lines[0] != "gateway: up" || v.Next != 1 {
		t.Fatal(w.Code, w.Body.String())
	}
}
