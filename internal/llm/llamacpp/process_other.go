//go:build !windows && !darwin && !linux

package llamacpp

import (
	"os"
	"os/exec"
)

func configureProcess(cmd *exec.Cmd)       {}
func interruptProcess(cmd *exec.Cmd) error { return cmd.Process.Signal(os.Interrupt) }
func killProcess(cmd *exec.Cmd) error      { return cmd.Process.Kill() }
