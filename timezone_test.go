package main

import (
	"testing"
	"time"
)

func TestWindowsZones(t *testing.T) {
	for key, want := range map[string]string{
		"Tokyo Standard Time":         "Asia/Tokyo",
		"Pacific Standard Time":       "America/Los_Angeles",
		"India Standard Time":         "Asia/Kolkata",
		"W. Europe Standard Time":     "Europe/Berlin",
		"UTC":                         utcZone,
		"Tokyo Standard Time\x00\x00": "Asia/Tokyo",
	} {
		if z, ok := windowsZone(key); !ok || z != want {
			t.Errorf("%q: %q %v", key, z, ok)
		}
	}
	if _, ok := windowsZone("Nowhere Standard Time"); ok {
		t.Error("unknown zone mapped")
	}
	// Every name is current: Ubuntu's tzdata lacks the old links.
	for key, z := range windowsZones {
		if _, err := time.LoadLocation(z); err != nil || tzAliases[z] != "" {
			t.Errorf("%s: %s %v", key, z, err)
		}
	}
}

func TestNormalTimezone(t *testing.T) {
	for name, want := range map[string]string{
		"Asia/Tokyo":    "Asia/Tokyo",
		"Asia/Calcutta": "Asia/Kolkata",
		"UTC":           utcZone,
		"Etc/GMT+12":    "Etc/GMT+12",
	} {
		if z, ok := normalTimezone(name); !ok || z != want {
			t.Errorf("%q: %q %v", name, z, ok)
		}
	}
	for _, name := range []string{"", "Local", "Nowhere/City", "../etc/passwd", "/etc/localtime", "Asia/Tokyo'; x", "Asia//Tokyo"} {
		if _, ok := normalTimezone(name); ok {
			t.Errorf("%q accepted", name)
		}
	}
	for p, want := range map[string]string{
		"/usr/share/zoneinfo/Asia/Tokyo":          "Asia/Tokyo",
		"../usr/share/zoneinfo/Europe/Paris":      "Europe/Paris",
		"/var/db/timezone/zoneinfo/Asia/Tokyo":    "Asia/Tokyo",
		"/usr/share/zoneinfo.default/Asia/Tokyo":  "Asia/Tokyo",
		"/usr/share/zoneinfo/posix/Europe/Berlin": "Europe/Berlin",
		"/etc/localtime":                          "",
	} {
		if z := zoneFromPath(p); z != want {
			t.Errorf("%s: %q", p, z)
		}
	}
}
