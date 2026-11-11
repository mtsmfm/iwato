//go:build !windows

package proc

import "os/exec"

// Hide does nothing outside Windows.
func Hide(*exec.Cmd) {}
