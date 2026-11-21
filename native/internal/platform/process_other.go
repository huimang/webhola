//go:build !windows

package platform

import (
	"os/exec"
)

func HideProcess(cmd *exec.Cmd) {}
