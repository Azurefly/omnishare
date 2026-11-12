//go:build !windows

package api

import "os/exec"

func configureTailCommand(_ *exec.Cmd) {}
