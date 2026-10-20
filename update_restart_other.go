//go:build !windows

package main

import (
	"fmt"
	"os"
	"syscall"
)

var execExe = syscall.Exec

// relaunch replaces this process with the swapped exe (same PID, terminal
// and job), or with the old exe again when that fails.
func relaunch(exe, addr string) error {
	err := execExe(exe, os.Args, restartEnv(addr))
	return restoreAndExec(exe, addr, err)
}

// rollBack runs when the swapped exe, started by relaunch, fails before
// serving.
func rollBack(addr string, cause error) error {
	exe, err := selfExe()
	if err != nil {
		return fmt.Errorf("%v; finding the old exe: %v", cause, err)
	}
	return restoreAndExec(exe, addr, cause)
}

func restoreAndExec(exe, addr string, cause error) error {
	if err := restoreExe(exe); err != nil {
		return fmt.Errorf("starting the updated %s: %v; restoring the old one: %v", exe, cause, err)
	}
	noticef("the updated Iwato did not start (%v): starting the old one", cause)
	return execExe(exe, os.Args, append(restartEnv(addr), restartRestoredEnv+"=1"))
}
