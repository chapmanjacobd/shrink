//go:build !linux && !darwin && !windows

package utils

import (
	"os"
	"time"
)

// GetAccessTime returns the access time of a file
func GetAccessTime(info os.FileInfo) time.Time {
	return info.ModTime()
}

// GetDeviceID returns the device ID of a file
func GetDeviceID(info os.FileInfo) (uint64, bool) {
	return 0, false
}
