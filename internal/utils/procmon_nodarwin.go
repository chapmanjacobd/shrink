//go:build !darwin

package utils

import "os"

// getProcessRSSDarwin gets RSS on macOS using sysctl.
// Stub implementation for non-macOS systems.
func getProcessRSSDarwin(pid int) (int64, error) {
	return 0, os.ErrNotExist
}
