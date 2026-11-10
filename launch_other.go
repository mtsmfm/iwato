//go:build !windows

package main

import (
	"os/exec"
	"runtime"
)

func openURL(u string) error {
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "open"
	}
	c := exec.Command(name, u)
	if err := c.Start(); err != nil {
		return err
	}
	go func() { _ = c.Wait() }()
	return nil
}

func protocolHandled(string) bool { return false }

func portOwner(int) string { return "" }
