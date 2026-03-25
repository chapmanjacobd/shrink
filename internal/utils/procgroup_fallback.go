//go:build !unix

package utils

import "os/exec"

// SetupProcessGroup configures the command to run in a new process group.
// This is a no-op on platforms without process group support.
func SetupProcessGroup(cmd *exec.Cmd) {
	// No-op for platforms without process group support
}
