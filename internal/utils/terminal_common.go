package utils

import "runtime"

// GetClearLineSequence returns the escape sequence to clear/overwrite a line.
// For OSes that support it, we use \x1b[1K (Erase from beginning of line to cursor).
func GetClearLineSequence() string {
	if runtime.GOOS == "windows" {
		// Older Windows versions might only support \x1b[K
		// But modern Windows 10+ with VT mode enabled supports both.
		// The user specifically asked for \x1b[1K where supported.
		return "\033[1K"
	}
	return "\033[1K"
}

// TruncateMiddle truncates a string in the middle with ellipsis
func TruncateMiddle(s string, max int) string {
	if s == "" {
		return ""
	}
	if len(s) <= max {
		return s
	}
	half := max / 2
	if half < 2 {
		half = 2
	}
	return s[:half-1] + "…" + s[len(s)-half+1:]
}
