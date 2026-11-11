//go:build !windows

package main

// newAutostart: only Windows starts Iwato at sign-in.
func newAutostart(string) autostarter { return nil }
