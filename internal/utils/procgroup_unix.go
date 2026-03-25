//go:build unix

package utils

import (
	"os/exec"
	"syscall"
)

// SetupProcessGroup configures the command to run in a new process group.
// This allows us to kill all child processes together.
func SetupProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	// Unix: create new process group
	cmd.SysProcAttr.Setpgid = true
}
