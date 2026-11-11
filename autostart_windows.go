package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// runKey starts Iwato at sign-in from the current user's Run key, as most
// Windows apps do (no administrator; listed and switchable in Task
// Manager's Startup apps). The command names the configuration by its full
// path: Windows starts it in another folder.
type runKey struct{ command string }

const (
	runKeyPath   = `Software\Microsoft\Windows\CurrentVersion\Run`
	approvedPath = `Software\Microsoft\Windows\CurrentVersion\Explorer\StartupApproved\Run`
	runValue     = "Iwato"
)

func newAutostart(configPath string) autostarter {
	exe, err := os.Executable()
	if err != nil {
		return nil
	}
	config, err := filepath.Abs(configPath)
	if err != nil {
		return nil
	}
	return runKey{command: fmt.Sprintf(`"%s" serve -background -config "%s"`, exe, config)}
}

// Enabled: the Run key starts this exe with this configuration, and Task
// Manager has not turned it off.
func (r runKey) Enabled() (bool, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.QUERY_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer k.Close()
	v, _, err := k.GetStringValue(runValue)
	if errors.Is(err, registry.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return strings.EqualFold(v, r.command) && !turnedOff(), nil
}

// turnedOff: Task Manager's Startup apps disabled it (an odd first byte in
// StartupApproved; the Run value stays).
func turnedOff() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, approvedPath, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	b, _, err := k.GetBinaryValue(runValue)
	return err == nil && len(b) > 0 && b[0]&1 == 1
}

// Set writes or removes the Run value; either way Task Manager's own
// switch is cleared, so that turning it on here starts it.
func (r runKey) Set(on bool) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKeyPath, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if on {
		err = k.SetStringValue(runValue, r.command)
	} else if err = k.DeleteValue(runValue); errors.Is(err, registry.ErrNotExist) {
		err = nil
	}
	if err != nil {
		return err
	}
	if a, err := registry.OpenKey(registry.CURRENT_USER, approvedPath, registry.SET_VALUE); err == nil {
		_ = a.DeleteValue(runValue)
		a.Close()
	}
	return nil
}
