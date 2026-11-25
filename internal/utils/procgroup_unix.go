//go:build unix

package utils

import (
	"os/exec"
	"syscall"
)

// SetupProcessGroup configures the command to run in a new process group and
// makes context cancellation kill the whole group. Without this, cancelling
// only kills the direct child and leaves helper processes running.
func SetupProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	// Unix: create new process group
	cmd.SysProcAttr.Setpgid = true
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
			// The process may have exited between cancellation and kill.
			return cmd.Process.Kill()
		}
		return nil
	}
}
