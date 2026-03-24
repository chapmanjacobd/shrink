//go:build linux || darwin

package utils

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// killProcessGroupImpl kills a process and all its children on Unix systems.
func killProcessGroupImpl(pid int) {
	if pid <= 0 {
		return
	}

	// Kill the entire process group (negative PID)
	err := unix.Kill(-pid, unix.SIGKILL)
	if err != nil {
		// Try killing just the process
		_ = unix.Kill(pid, unix.SIGKILL)
	}
}

// setProcessNice sets the priority of a process on Unix systems.
func setProcessNice(pid, nice int) error {
	// Clamp nice to valid range
	if nice < -20 {
		nice = -20
	} else if nice > 19 {
		nice = 19
	}

	// Use setpriority to change process priority
	return unix.Setpriority(unix.PRIO_PROCESS, pid, nice)
}

// getChildMemoryUsage returns the total RSS memory usage of a process and all its children.
func getChildMemoryUsage(pid int) int64 {
	// Get memory for parent process
	total := getProcessRSS(pid)

	// Find and sum memory for all child processes
	children := getChildProcesses(pid)
	for _, childPid := range children {
		total += getProcessRSS(childPid)
	}

	return total
}

// getProcessRSS returns the RSS (Resident Set Size) memory of a process in bytes.
func getProcessRSS(pid int) int64 {
	// Try /proc/[pid]/statm first (Linux)
	if statm, err := readStatm(pid); err == nil {
		// statm[1] is RSS in pages
		pageSize := int64(os.Getpagesize())
		return statm[1] * pageSize
	}

	// Fallback to /proc/[pid]/status (Linux)
	if status, err := readStatusVmRSS(pid); err == nil {
		return status
	}

	// macOS: use sysctl
	if rss, err := getProcessRSSDarwin(pid); err == nil {
		return rss
	}

	return 0
}

// readStatm reads /proc/[pid]/statm on Linux.
func readStatm(pid int) ([]int64, error) {
	path := filepath.Join("/proc", strconv.Itoa(pid), "statm")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	fields := strings.Fields(string(data))
	if len(fields) < 2 {
		return nil, os.ErrNotExist
	}

	result := make([]int64, len(fields))
	for i, f := range fields {
		n, err := strconv.ParseInt(f, 10, 64)
		if err != nil {
			return nil, err
		}
		result[i] = n
	}
	return result, nil
}

// readStatusVmRSS reads VmRSS from /proc/[pid]/status on Linux.
func readStatusVmRSS(pid int) (int64, error) {
	path := filepath.Join("/proc", strconv.Itoa(pid), "status")
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}

	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "VmRSS:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				// VmRSS is in kB
				kb, err := strconv.ParseInt(fields[1], 10, 64)
				if err != nil {
					return 0, err
				}
				return kb * 1024, nil
			}
		}
	}

	return 0, os.ErrNotExist
}

// getChildProcesses returns a list of child process PIDs.
func getChildProcesses(parentPid int) []int {
	var children []int

	// Linux: read from /proc
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return children
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}

		if ppid := getProcessParent(pid); ppid == parentPid {
			children = append(children, pid)
		}
	}

	return children
}

// getProcessParent returns the parent PID of a process.
func getProcessParent(pid int) int {
	path := filepath.Join("/proc", strconv.Itoa(pid), "stat")
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}

	// Parse stat file - PPID is the 4th field
	// But first we need to handle the comm field which can contain spaces and parentheses
	content := string(data)

	// Find the last ')' which ends the comm field
	lastParen := strings.LastIndex(content, ")")
	if lastParen == -1 {
		return 0
	}

	// Split the rest
	fields := strings.Fields(content[lastParen+1:])
	if len(fields) < 2 {
		return 0
	}

	ppid, err := strconv.Atoi(fields[1]) // PPID is field 4 (index 1 after comm)
	if err != nil {
		return 0
	}

	return ppid
}
