package utils

// GetClearLineSequence returns the escape sequence to clear/overwrite a line.
// We use \x1b[K (Erase from cursor to end of line) which is standard for overwriting.
func GetClearLineSequence() string {
	return "\033[K"
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
