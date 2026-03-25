//go:build darwin

package utils

import (
	"os/exec"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// GetTotalRAM returns the total physical memory in bytes.
func GetTotalRAM() int64 {
	if s, err := unix.SysctlUint64("hw.memsize"); err == nil {
		return int64(s)
	}
	return 0
}

// killProcessGroupImpl kills a process and all its children on macOS.
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

// setProcessNice sets the priority of a process on macOS.
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
	// macOS: use ps
	if rss, err := getProcessRSSDarwin(pid); err == nil {
		return rss
	}
	return 0
}

// getProcessRSSDarwin gets RSS on macOS using ps command.
func getProcessRSSDarwin(pid int) (int64, error) {
	cmd := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(pid))
	out, err := cmd.Output()
	if err != nil {
		return 0, err
	}

	rssStr := strings.TrimSpace(string(out))
	if rssStr == "" {
		return 0, nil
	}

	rssKb, err := strconv.ParseInt(rssStr, 10, 64)
	if err != nil {
		return 0, err
	}

	// ps returns RSS in kilobytes
	return rssKb * 1024, nil
}

// getChildProcesses returns a list of child process PIDs.
func getChildProcesses(parentPid int) []int {
	// Use pgrep to find child processes
	cmd := exec.Command("pgrep", "-P", strconv.Itoa(parentPid))
	out, err := cmd.Output()
	if err != nil {
		return nil
	}

	var children []int
	lines := strings.Split(string(out), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if childPid, err := strconv.Atoi(line); err == nil {
			children = append(children, childPid)
		}
	}
	return children
}

// getProcessParent returns the parent PID of a process.
func getProcessParent(pid int) int {
	cmd := exec.Command("ps", "-o", "ppid=", "-p", strconv.Itoa(pid))
	out, err := cmd.Output()
	if err != nil {
		return 0
	}

	ppidStr := strings.TrimSpace(string(out))
	if ppidStr == "" {
		return 0
	}

	if ppid, err := strconv.Atoi(ppidStr); err == nil {
		return ppid
	}
	return 0
}
