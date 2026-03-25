//go:build !linux

package utils

import (
	"context"
	"os/exec"
)

// SystemdRunConfig holds configuration for running commands via systemd-run.
// On non-Linux platforms, memory limits are not enforced.
type SystemdRunConfig struct {
	MemoryLimit   int64
	MemorySwapMax int64
	Nice          int
	UseJournald   bool
	Enabled       bool
}

// RunCommandWithSystemd executes a command directly without systemd-run wrapper.
// Memory limits are not enforced on non-Linux platforms.
func RunCommandWithSystemd(ctx context.Context, exe string, args []string, cfg SystemdRunConfig) ([]byte, error) {
	cmd := exec.CommandContext(ctx, exe, args...)
	SetupProcessGroup(cmd)
	return cmd.CombinedOutput()
}
