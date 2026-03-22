//go:build windows

package utils

import (
	"os"
	"syscall"
	"time"
)

// GetAccessTime returns the access time of a file
func GetAccessTime(info os.FileInfo) time.Time {
	if stat, ok := info.Sys().(*syscall.Win32FileAttributeData); ok {
		return time.Unix(0, stat.LastAccessTime.Nanoseconds())
	}
	return info.ModTime()
}
