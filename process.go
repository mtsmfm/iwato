package main

import (
	"os/exec"

	"iwato/internal/proc"
)

func configureProcess(c *exec.Cmd) { proc.Hide(c) }
