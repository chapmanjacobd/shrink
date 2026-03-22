//go:build darwin

package utils

import (
	"os"
	"syscall"
	"time"
)

// GetAccessTime returns the access time of a file
func GetAccessTime(info os.FileInfo) time.Time {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		return time.Unix(stat.Atimespec.Sec, stat.Atimespec.Nsec)
	}
	return info.ModTime()
}
