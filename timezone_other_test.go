//go:build !windows

package main

import "testing"

func TestHostTimezoneFromTZ(t *testing.T) {
	for tz, want := range map[string]string{
		"Asia/Tokyo":                        "Asia/Tokyo",
		":/usr/share/zoneinfo/Europe/Paris": "Europe/Paris",
		"Asia/Calcutta":                     "Asia/Kolkata",
		"JST-9":                             utcZone,
	} {
		t.Setenv("TZ", tz)
		if z := hostTimezone(); z != want {
			t.Errorf("%s: %s", tz, z)
		}
	}
}
