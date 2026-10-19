// Package termbin carries the iwato-entrypoint binary (cmd/iwato-entrypoint)
// built for Linux on this exe's architecture. scripts/build-exe.sh puts it
// in bin/ for the exe's build (go build -overlay). The sandbox image
// carries its own; this one is what the WSLc driver places, as iwato-term,
// in sandboxes that run it from the legacy path, and what a local build of
// the image (--local-images) puts in. Without it (go test, a plain go
// build) Binary is nil.
package termbin

import (
	"embed"
	"runtime"
)

//go:embed all:bin
var files embed.FS

// Binary returns iwato-entrypoint, or nil when this build has none for its
// architecture.
func Binary() []byte {
	b, err := files.ReadFile("bin/iwato-entrypoint")
	if err != nil || !isELF(b, runtime.GOARCH) {
		return nil
	}
	return b
}

// isELF checks that b is a 64-bit little-endian ELF executable for goarch.
func isELF(b []byte, goarch string) bool {
	machine := map[string]uint16{"amd64": 62, "arm64": 183}[goarch]
	return len(b) > 20 && string(b[:4]) == "\x7fELF" && b[4] == 2 && b[5] == 1 && machine != 0 && uint16(b[18])|uint16(b[19])<<8 == machine
}
