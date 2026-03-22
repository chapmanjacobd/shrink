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
