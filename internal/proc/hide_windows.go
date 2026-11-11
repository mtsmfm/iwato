// Package proc starts child processes without a console window of their
// own: Iwato is a GUI program on Windows, so a console program it starts
// (wslc.exe, tailscale.exe) would otherwise open one.
package proc

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// Hide starts c without a console window.
func Hide(c *exec.Cmd) {
	if c.SysProcAttr == nil {
		c.SysProcAttr = &syscall.SysProcAttr{}
	}
	c.SysProcAttr.HideWindow = true
	c.SysProcAttr.CreationFlags |= windows.CREATE_NO_WINDOW
}
