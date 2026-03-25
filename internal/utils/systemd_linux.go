//go:build linux

package utils

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

// SystemdRunConfig holds configuration for running commands via systemd-run.
type SystemdRunConfig struct {
	// MemoryLimit is the maximum memory allowed (in bytes).
	// If 0, no memory limit is set.
	MemoryLimit int64
	// MemorySwapMax is the maximum swap allowed (in bytes).
	// Default is MemoryLimit / 2. If 0 and MemoryLimit is set, swap is disabled.
	MemorySwapMax int64
	// Nice sets the process priority (-20 to 19).
	// Default: 0
	Nice int
	// UseJournald enables journald-compatible mode (--wait --pipe).
	// Default: false (uses --scope mode)
	UseJournald bool
	// Enabled controls whether systemd-run is used at all.
	// Default: true (use systemd-run if available)
	Enabled bool
	// Dir specifies the working directory for the command.
	// If empty, the current working directory is used.
	Dir string
}

// isTesting returns true if the code is running under `go test`.
func isTesting() bool {
	for _, arg := range os.Args {
		if strings.HasPrefix(arg, "-test.") {
			return true
		}
	}
	return testing.Testing()
}

// RunCommandWithSystemd executes a command, optionally wrapping it with systemd-run
// for resource control. Returns the command's exit error and output.
//
// If systemd-run is available and MemoryLimit > 0, the command is wrapped with:
//   - systemd-run --scope -p MemoryMax=<limit> -p MemorySwapMax=<swap> --nice=<nice> -- ...
//
// If systemd-run is not available or Enabled is false, runs the command directly.
// During tests, systemd-run is disabled to avoid D-Bus authentication prompts.
func RunCommandWithSystemd(ctx context.Context, exe string, args []string, cfg SystemdRunConfig) ([]byte, error) {
	// Disable systemd-run during tests to avoid D-Bus authentication
	if isTesting() {
		cmd := exec.CommandContext(ctx, exe, args...)
		SetupProcessGroup(cmd)
		if cfg.Dir != "" {
			cmd.Dir = cfg.Dir
		}
		return cmd.CombinedOutput()
	}

	cmd, err := buildSystemdCommand(ctx, exe, args, cfg)
	if err != nil {
		return nil, err
	}

	slog.Debug("Executing command", "cmd", cmd.String())
	return cmd.CombinedOutput()
}

// buildSystemdCommand builds an exec.Cmd, optionally wrapped with systemd-run.
func buildSystemdCommand(ctx context.Context, exe string, args []string, cfg SystemdRunConfig) (*exec.Cmd, error) {
	// Check if we should use systemd-run
	useSystemd := cfg.Enabled && cfg.MemoryLimit > 0

	if !useSystemd {
		// Direct execution
		cmd := exec.CommandContext(ctx, exe, args...)
		SetupProcessGroup(cmd)
		if cfg.Dir != "" {
			cmd.Dir = cfg.Dir
		}
		return cmd, nil
	}

	// Check if systemd-run is available
	systemdRun := GetCommandPath("systemd-run")
	if systemdRun == "" {
		slog.Debug("systemd-run not found, running command directly", "command", exe)
		cmd := exec.CommandContext(ctx, exe, args...)
		SetupProcessGroup(cmd)
		if cfg.Dir != "" {
			cmd.Dir = cfg.Dir
		}
		return cmd, nil
	}

	// Build systemd-run command
	systemdArgs := []string{"systemd-run"}

	// Check if running as user (not root)
	if os.Getenv("SUDO_UID") == "" {
		systemdArgs = append(systemdArgs, "--user")
	}

	// Add nice value if specified
	if cfg.Nice != 0 {
		systemdArgs = append(systemdArgs, "--nice="+strconv.Itoa(cfg.Nice))
	}

	// Add mode flags
	if cfg.UseJournald {
		systemdArgs = append(systemdArgs,
			"--service-type=exec",
			"--wait",
			"--pty",
			"--pipe",
		)
	} else {
		systemdArgs = append(systemdArgs, "--scope")
	}

	// Add working directory if specified
	if cfg.Dir != "" {
		systemdArgs = append(systemdArgs, "--working-directory="+cfg.Dir)
	}

	// Add memory limits
	if cfg.MemoryLimit > 0 {
		systemdArgs = append(systemdArgs,
			"-p", "MemoryMax="+FormatSize(cfg.MemoryLimit),
		)
		// Set swap limit (default to half of memory limit if not specified)
		swapMax := cfg.MemorySwapMax
		if swapMax == 0 {
			swapMax = cfg.MemoryLimit / 2
		}
		if swapMax > 0 {
			systemdArgs = append(systemdArgs,
				"-p", "MemorySwapMax="+FormatSize(swapMax),
			)
		} else {
			// Explicitly disable swap
			systemdArgs = append(systemdArgs,
				"-p", "MemorySwapMax=0",
			)
		}
	}

	// Add remaining systemd-run flags
	systemdArgs = append(systemdArgs,
		"--same-dir",
		"--collect",
		"--quiet",
		"--",
	)

	// Append the actual command
	systemdArgs = append(systemdArgs, exe)
	systemdArgs = append(systemdArgs, args...)

	cmd := exec.CommandContext(ctx, systemdRun, systemdArgs...)
	// Don't setup process group for systemd-run, systemd manages the scope
	return cmd, nil
}
