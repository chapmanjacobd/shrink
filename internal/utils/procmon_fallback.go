//go:build !linux && !darwin && !windows

package utils

// GetTotalRAM returns the total physical memory in bytes.
func GetTotalRAM() int64 {
	return 0
}

func killProcessGroupImpl(pid int) {}

func setProcessNice(pid, nice int) error {
	return nil
}

func getChildMemoryUsage(pid int) int64 {
	return 0
}
