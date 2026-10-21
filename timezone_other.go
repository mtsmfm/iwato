//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"strings"
)

// hostZoneName is TZ, else the zone /etc/localtime links to, else
// /etc/timezone ("" when none says).
func hostZoneName() string {
	if tz := strings.TrimPrefix(os.Getenv("TZ"), ":"); tz != "" {
		if z := zoneFromPath(tz); z != "" {
			return z
		}
		return tz
	}
	if l, err := os.Readlink("/etc/localtime"); err == nil {
		if z := zoneFromPath(filepath.ToSlash(l)); z != "" {
			return z
		}
	}
	if b, err := os.ReadFile("/etc/timezone"); err == nil {
		return strings.TrimSpace(string(b))
	}
	return ""
}
