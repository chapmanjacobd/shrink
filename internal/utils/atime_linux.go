//go:build linux

package utils

import (
	"os"
	"syscall"
	"time"
)

// GetAccessTime returns the access time of a file
func GetAccessTime(info os.FileInfo) time.Time {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		return time.Unix(stat.Atim.Sec, stat.Atim.Nsec)
	}
	return info.ModTime()
}

// GetDeviceID returns the device ID of a file
func GetDeviceID(info os.FileInfo) (uint64, bool) {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		return uint64(stat.Dev), true
	}
	return 0, false
}
