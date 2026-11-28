//go:build !windows

package aiquota

import "os/exec"

func hideQuotaWindow(*exec.Cmd) {}
