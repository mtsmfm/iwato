//go:build uitest

package main

import (
	"strings"
	"testing"
)

func TestTimezoneSetting(t *testing.T) {
	s, _ := newTestServer(t)
	cookie, csrf := login(t, s)
	for _, name := range []string{"Nowhere/City", "Local", "../x"} {
		if res, b := request(t, s, "/api/timezone", "POST", `{"timezone":"`+name+`"}`, cookie, csrf); res.StatusCode != 400 || !strings.Contains(string(b), "timezone_invalid") {
			t.Fatal(name, res.StatusCode, string(b))
		}
	}
	if res, b := request(t, s, "/api/timezone", "POST", `{"timezone":"Asia/Tokyo"}`, cookie, csrf); res.StatusCode != 200 {
		t.Fatal(res.StatusCode, string(b))
	}
	if _, b := request(t, s, "/api/timezone", "GET", "", cookie, ""); !strings.Contains(string(b), `"timezone":"Asia/Tokyo"`) || !strings.Contains(string(b), `"host":"`) {
		t.Fatal(string(b))
	}
}
