package main

import "golang.org/x/sys/unix"

// clearQuarantine drops the attribute that makes Gatekeeper ask before the
// first run (best effort: a download by Go does not set it).
func clearQuarantine(path string) { _ = unix.Removexattr(path, "com.apple.quarantine") }
