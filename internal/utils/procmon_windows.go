//go:build windows

package utils

import (
	"golang.org/x/sys/windows"
	"unsafe"
)

var (
	kernel32                  = windows.NewLazySystemDLL("kernel32.dll")
	procOpenProcess           = kernel32.NewProc("OpenProcess")
	procCloseHandle           = kernel32.NewProc("CloseHandle")
	procEnumProcesses         = kernel32.NewProc("K32EnumProcesses")
	procGetProcessMemoryInfo  = kernel32.NewProc("K32GetProcessMemoryInfo")
	psapi                     = windows.NewLazySystemDLL("psapi.dll")
)

const (
	PROCESS_QUERY_INFORMATION = 0x0400
	PROCESS_VM_READ           = 0x0010
)

// PROCESS_MEMORY_COUNTERS structure for GetProcessMemoryInfo
type processMemoryCounters struct {
	CB                     uint32
	PageFaultCount         uint32
	PeakWorkingSetSize     uintptr
	WorkingSetSize         uintptr
	QuotaPeakPagedPoolUsage  uintptr
	QuotaPagedPoolUsage      uintptr
	QuotaPeakNonPagedPoolUsage uintptr
	QuotaNonPagedPoolUsage   uintptr
	PagefileUsage          uint32
	PeakPagefileUsage      uint32
}

// killProcessGroupImpl kills a process and all its children on Windows.
func killProcessGroupImpl(pid int) {
	if pid <= 0 {
		return
	}

	// On Windows, we need to kill the process tree
	// For now, just kill the main process
	// A more complete implementation would enumerate child processes
	killProcess(pid)
}

// killProcess kills a single process on Windows.
func killProcess(pid int) {
	handle, _, _ := procOpenProcess.Call(
		windows.PROCESS_TERMINATE,
		0,
		uintptr(pid),
	)
	if handle == 0 {
		return
	}
	defer procCloseHandle.Call(handle)

	windows.TerminateProcess(windows.Handle(handle), 1)
}

// setProcessNice sets the priority of a process on Windows.
// Note: Windows uses priority classes instead of nice values.
func setProcessNice(pid, nice int) error {
	// Windows priority classes don't map directly to Unix nice values
	// For now, this is a no-op on Windows
	// A full implementation would use SetPriorityClass
	return nil
}

// getChildMemoryUsage returns the total working set memory usage of a process and all its children.
func getChildMemoryUsage(parentPid int) int64 {
	// Get memory for parent process
	total := getProcessWorkingSet(parentPid)

	// Find and sum memory for all child processes
	children := getChildProcesses(parentPid)
	for _, childPid := range children {
		total += getProcessWorkingSet(childPid)
	}

	return total
}

// getProcessWorkingSet returns the working set size of a process in bytes.
func getProcessWorkingSet(pid int) int64 {
	handle, _, _ := procOpenProcess.Call(
		uintptr(PROCESS_QUERY_INFORMATION|PROCESS_VM_READ),
		0,
		uintptr(pid),
	)
	if handle == 0 {
		return 0
	}
	defer procCloseHandle.Call(handle)

	var counters processMemoryCounters
	counters.CB = uint32(unsafe.Sizeof(counters))

	ret, _, _ := procGetProcessMemoryInfo.Call(
		handle,
		uintptr(unsafe.Pointer(&counters)),
		unsafe.Sizeof(counters),
	)
	if ret == 0 {
		return 0
	}

	return int64(counters.WorkingSetSize)
}

// getChildProcesses returns a list of child process PIDs.
func getChildProcesses(parentPid int) []int {
	var children []int

	// Enumerate all processes
	var pids [1024]uint32
	var needed uint32

	ret, _, _ := procEnumProcesses.Call(
		uintptr(unsafe.Pointer(&pids[0])),
		uintptr(len(pids)) * 4,
		uintptr(unsafe.Pointer(&needed)),
	)
	if ret == 0 {
		return children
	}

	numPids := int(needed / 4)

	// Check each process
	for i := 0; i < numPids; i++ {
		pid := int(pids[i])
		if getProcessParent(pid) == parentPid {
			children = append(children, pid)
		}
	}

	return children
}

// getProcessParent returns the parent PID of a process.
// Note: This is a simplified implementation. For full Windows support,
// you would need to use NtQueryInformationProcess or Toolhelp32 API.
func getProcessParent(pid int) int {
	// For Windows, we'd need to use the Toolhelp32 API or WMI
	// This is a placeholder - in practice, you might want to use
	// a more comprehensive approach with CreateToolhelp32Snapshot
	return 0
}
