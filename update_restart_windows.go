//go:build windows

package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"time"
)

// relaunch starts the swapped exe in this console and returns once it
// listens on addr; when it does not, the old exe is put back and started.
func relaunch(exe, addr string) error {
	err := startListening(exe, addr)
	if err == nil {
		return nil
	}
	var rerr error
	for range 10 {
		if rerr = restoreExe(exe); rerr == nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if rerr != nil {
		return fmt.Errorf("starting the updated %s: %v; restoring the old one: %v (it is kept as %s.old)", exe, err, rerr, exe)
	}
	noticef("the updated Iwato did not start (%v): starting the old one", err)
	return startListening(exe, addr)
}

func startListening(exe, addr string) error {
	c := exec.Command(exe, os.Args[1:]...)
	c.Env = restartEnv(addr)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := c.Start(); err != nil {
		return err
	}
	exited := make(chan error, 1)
	go func() { exited <- c.Wait() }()
	deadline := time.Now().Add(time.Minute)
	for time.Now().Before(deadline) {
		select {
		case err := <-exited:
			return fmt.Errorf("it exited: %v", err)
		case <-time.After(200 * time.Millisecond):
		}
		if conn, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
			conn.Close()
			return nil
		}
	}
	// Kill does not wait, and the exe cannot be replaced while it is mapped.
	_ = c.Process.Kill()
	<-exited
	return errors.New("it did not listen within a minute")
}

// rollBack is relaunch's job on Windows: the old process waits for this one.
func rollBack(_ string, cause error) error { return cause }
