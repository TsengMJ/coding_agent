//go:build unix

package backend

import (
	"errors"
	"os/exec"
	"syscall"
)

// setProcessGroup starts the command in its own process group and makes
// context cancellation kill the whole group, not just `sh`.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return killProcessGroup(cmd) }
}

// killProcessGroup sends SIGKILL to every process left in the command's group.
func killProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}
