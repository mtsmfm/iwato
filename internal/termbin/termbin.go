// Package termbin carries the iwato-entrypoint binary (cmd/iwato-entrypoint)
// built for Linux on this exe's architecture (the WSL VM's):
// scripts/build-exe.sh builds it and puts it in bin/ for the exe's build
// (through go build -overlay, per architecture). The sandbox image carries
// its own (built from the same commit); this one is for what is not that
// image: the WSLc driver places it, as iwato-term, in the sandboxes made
// before the image carried it, and a local build of the image
// (--local-images) puts it in. Without it (go test, a plain go build)
// Binary is nil: those sandboxes use the terminals without the daemon (the
// main process and execs).
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
