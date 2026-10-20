//go:build !windows

package main

import (
	"fmt"
	"os"
	"syscall"
)

// relaunch replaces this process with the swapped exe (same PID, terminal
// and job), or with the old exe again when that fails.
func relaunch(exe, addr string) error {
	env := restartEnv(addr)
	err := syscall.Exec(exe, os.Args, env)
	if rerr := restoreExe(exe); rerr != nil {
		return fmt.Errorf("starting the updated %s: %v; restoring the old one: %v", exe, err, rerr)
	}
	noticef("the updated Iwato did not start (%v): starting the old one", err)
	return syscall.Exec(exe, os.Args, env)
}
