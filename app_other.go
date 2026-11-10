//go:build !windows

package main

import (
	"fmt"
	"os"
	"time"

	"golang.org/x/term"
)

func app(args []string) int {
	if err := run(args); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		return 1
	}
	return 0
}

// startPrompt asks on the console, or waits without one.
func startPrompt() portPrompt {
	if term.IsTerminal(int(os.Stdin.Fd())) {
		return consolePrompt(os.Stdin, os.Stdout)
	}
	return waitPrompt(time.Minute, 2*time.Second)
}
