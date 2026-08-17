//go:build windows

package clibe

import "os/exec"

func isolateProcess(*exec.Cmd) {}

func killProcessTree(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
