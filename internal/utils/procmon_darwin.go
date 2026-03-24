//go:build darwin

package utils

import (
	"unsafe"

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
	// macOS: use sysctl
	if rss, err := getProcessRSSDarwin(pid); err == nil {
		return rss
	}
	return 0
}

// getProcessRSSDarwin gets RSS on macOS using sysctl.
func getProcessRSSDarwin(pid int) (int64, error) {
	// Use sysctl to get process info on macOS
	// MIB: CTL_KERN, KERN_PROC, KERN_PROC_PID, pid
	mib := [4]int32{unix.CTL_KERN, unix.KERN_PROC, unix.KERN_PROC_PID, int32(pid)}

	// First call to get required buffer size
	size := uintptr(0)
	_, _, errno := unix.Syscall6(
		unix.SYS___SYSCTL,
		uintptr(unsafe.Pointer(&mib[0])),
		uintptr(len(mib)),
		0,
		uintptr(unsafe.Pointer(&size)),
		0,
		0,
	)
	if errno != 0 {
		return 0, errno
	}

	if size == 0 {
		return 0, unix.ENOENT
	}

	// Allocate buffer and get process info
	buf := make([]byte, size)
	_, _, errno = unix.Syscall6(
		unix.SYS___SYSCTL,
		uintptr(unsafe.Pointer(&mib[0])),
		uintptr(len(mib)),
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&size)),
		0,
		0,
	)
	if errno != 0 {
		return 0, errno
	}

	// Parse kinfo_proc structure (platform-specific)
	// The structure layout varies, but we can extract RSS from it
	// This is a simplified extraction - full implementation would need
	// proper struct definition
	return extractRSSFromKInfoProc(buf), nil
}

// extractRSSFromKInfoProc extracts RSS from kinfo_proc structure.
// This is a simplified implementation.
func extractRSSFromKInfoProc(buf []byte) int64 {
	// kinfo_proc structure on macOS is complex and varies by version
	// For now, return 0 and rely on fallback methods
	// A full implementation would parse the struct properly
	return 0
}

// getChildProcesses returns a list of child process PIDs.
func getChildProcesses(parentPid int) []int {
	// macOS doesn't have /proc. A full implementation would use
	// libproc or sysctl to enumerate processes.
	return nil
}

// getProcessParent returns the parent PID of a process.
func getProcessParent(pid int) int {
	// macOS doesn't have /proc.
	return 0
}
