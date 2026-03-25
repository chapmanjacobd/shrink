// Package utils provides cross-platform process monitoring utilities.
package utils

import (
	"context"
	"log/slog"
	"os/exec"
	"sync"
	"time"
)

// ProcessMonitor tracks and enforces memory limits on child processes.
type ProcessMonitor struct {
	mu            sync.RWMutex
	pid           int
	cmd           *exec.Cmd
	limitBytes    int64
	checkInterval time.Duration
	stopChan      chan struct{}
	doneChan      chan struct{}
	exceeded      bool
	nice          int
}

// ProcessMonitorConfig holds configuration for process monitoring.
type ProcessMonitorConfig struct {
	// MemoryLimit is the maximum memory allowed (in bytes).
	// If 0, no memory monitoring is performed.
	MemoryLimit int64
	// CheckInterval is how often to check memory usage.
	// Default: 500ms
	CheckInterval time.Duration
	// Nice sets the process priority (Unix only, -20 to 19).
	// Default: 0
	Nice int
}

// NewProcessMonitor creates a new process monitor.
func NewProcessMonitor(cmd *exec.Cmd, cfg ProcessMonitorConfig) *ProcessMonitor {
	if cfg.CheckInterval <= 0 {
		cfg.CheckInterval = 500 * time.Millisecond
	}

	return &ProcessMonitor{
		cmd:           cmd,
		limitBytes:    cfg.MemoryLimit,
		checkInterval: cfg.CheckInterval,
		stopChan:      make(chan struct{}),
		doneChan:      make(chan struct{}),
		nice:          cfg.Nice,
	}
}

// Start begins monitoring the process.
// Call this after starting the command.
func (pm *ProcessMonitor) Start(ctx context.Context) {
	if pm.cmd.Process == nil {
		close(pm.doneChan)
		return
	}

	pm.mu.Lock()
	pm.pid = pm.cmd.Process.Pid
	pm.mu.Unlock()

	// Set process priority if requested (Unix only)
	if pm.nice != 0 {
		_ = setProcessNice(pm.pid, pm.nice)
	}

	// Start memory monitoring if limit is set
	if pm.limitBytes > 0 {
		go pm.monitorLoop(ctx)
	} else {
		close(pm.doneChan)
	}
}

// Stop stops the monitoring goroutine.
func (pm *ProcessMonitor) Stop() {
	close(pm.stopChan)
	<-pm.doneChan
}

// Exceeded returns true if the process exceeded the memory limit.
func (pm *ProcessMonitor) Exceeded() bool {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	return pm.exceeded
}

// GetMemoryUsage returns the current memory usage of the process and its children.
// Returns 0 if unable to determine.
func (pm *ProcessMonitor) GetMemoryUsage() int64 {
	pm.mu.RLock()
	pid := pm.pid
	pm.mu.RUnlock()

	if pid <= 0 {
		return 0
	}

	return getChildMemoryUsage(pid)
}

// monitorLoop periodically checks memory usage.
func (pm *ProcessMonitor) monitorLoop(ctx context.Context) {
	defer close(pm.doneChan)

	ticker := time.NewTicker(pm.checkInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-pm.stopChan:
			return
		case <-ticker.C:
			usage := pm.GetMemoryUsage()
			if usage > pm.limitBytes {
				pm.mu.Lock()
				pm.exceeded = true
				pm.mu.Unlock()

				slog.Warn("Process exceeded memory limit",
					"pid", pm.pid,
					"usage", FormatSize(usage),
					"limit", FormatSize(pm.limitBytes))

				// Kill the process group
				killProcessGroup(pm.pid)
				return
			}
		}
	}
}

// RunCommandWithMonitoring runs a command with memory monitoring.
// This is a convenience function that combines exec.CommandContext with monitoring.
func RunCommandWithMonitoring(ctx context.Context, cfg ProcessMonitorConfig, name string, args ...string) (*exec.Cmd, *ProcessMonitor, error) {
	cmd := exec.CommandContext(ctx, name, args...)

	// Set up process group for proper cleanup
	setupProcessGroup(cmd)

	monitor := NewProcessMonitor(cmd, cfg)

	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}

	monitor.Start(ctx)

	return cmd, monitor, nil
}

// killProcessGroup kills a process and all its children.
func killProcessGroup(pid int) {
	if pid <= 0 {
		return
	}

	// Platform-specific process group killing
	killProcessGroupImpl(pid)
}
