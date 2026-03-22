package models

// ShrinkMedia represents a media file to be processed
type ShrinkMedia struct {
	PartFiles      []string // For multi-part archives: list of all part files
	Path           string
	MediaType      string
	Ext            string
	Category       string
	Size           int64
	Duration       float64
	FutureSize     int64
	Savings        int64
	CompressedSize int64
	VideoCount     int
	AudioCount     int
	Width          int
	Height         int
	ProcessingTime int
	IsBroken       bool // For archives: lsar failed to read contents
}

// DisplayCategory returns the category with the extension suffix (e.g. "Video: mp4")
func (m *ShrinkMedia) DisplayCategory() string {
	ext := m.Ext
	if len(ext) > 0 && ext[0] == '.' {
		ext = ext[1:]
	}
	if ext == "" {
		ext = "unknown"
	}
	return m.Category + ": " + ext
}

// ShouldShrink determines if a file should be shrinked based on savings threshold
func (m *ShrinkMedia) ShouldShrink(futureSize int64, cfg *ProcessorConfig) bool {
	if cfg.Common.ForceShrink {
		return true
	}
	minSavings := cfg.GetMinSavings(m.Category)
	if minSavings < 1.0 {
		// Threshold is a percentage of future size
		shouldShrinkBuffer := int64(float64(futureSize) * minSavings)
		return m.Size > (futureSize + shouldShrinkBuffer)
	}
	// Threshold is absolute bytes
	return (m.Size - futureSize) >= int64(minSavings)
}
