//go:build unix

package clibe

import (
	"os/exec"
	"syscall"
)

func isolateProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killProcessTree(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	// Negative PID = the process group started by isolateProcess.
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
