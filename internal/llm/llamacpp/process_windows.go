//go:build windows

package llamacpp

import (
	"os/exec"
	"syscall"
)

const createNewProcessGroup = 0x00000200

func configureProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewProcessGroup}
}

// Windows does not provide a reliable os.Interrupt equivalent for an arbitrary
// detached child. Termination is the safe fallback and the reaper still owns
// the only Wait call.
func interruptProcess(cmd *exec.Cmd) error { return cmd.Process.Kill() }
func killProcess(cmd *exec.Cmd) error      { return cmd.Process.Kill() }
