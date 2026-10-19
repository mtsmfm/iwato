//go:build !linux

// iwato-entrypoint runs in Linux sandboxes only.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "iwato-entrypoint runs in Linux sandboxes only")
	os.Exit(2)
}
