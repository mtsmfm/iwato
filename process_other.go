//go:build !windows

package main

import "os/exec"

func configureProcess(c *exec.Cmd) {}
