//go:build !linux && !darwin && !windows

package utils

import "os/exec"

// GetTotalRAM returns the total physical memory in bytes.
func GetTotalRAM() int64 {
	return 0
}

// SetupProcessGroup configures the command to run in a new process group.
// This is a no-op on platforms without process group support.
func SetupProcessGroup(cmd *exec.Cmd) {
	// No-op for platforms without process group support
}

// setupProcessGroup configures the command to run in a new process group.
func setupProcessGroup(cmd *exec.Cmd) {
	SetupProcessGroup(cmd)
}

func killProcessGroupImpl(pid int) {}

func setProcessNice(pid, nice int) error {
	return nil
}

func getChildMemoryUsage(pid int) int64 {
	return 0
}
