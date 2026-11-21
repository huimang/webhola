package platform

import (
	"os/exec"
	"syscall"
)

func HideProcess(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true} }
