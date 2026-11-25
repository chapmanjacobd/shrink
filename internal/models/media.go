package models

import "math"

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
	if m.Category == "" {
		return ext
	}
	return m.Category + ": " + ext
}

// ShouldShrink determines if a file should be shrinked based on savings threshold.
//
// Percentage thresholds are measured against the original size, matching the
// user-facing definition of "at least N% space savings". For example, a 20%
// threshold requires the output to be no larger than 80% of the original file.
//
// For archives, futureSize must account for every file that will be extracted,
// including files that will be kept unchanged. Unarchiving only makes sense when
// the savings from the files that are processed outweigh the decompression of
// everything else.
func (m *ShrinkMedia) ShouldShrink(futureSize int64, cfg *ProcessorConfig) bool {
	if cfg.Common.ForceShrink {
		return true
	}
	minSavings := cfg.GetMinSavings(m.Category)
	savings := m.Size - futureSize
	if minSavings < 1.0 {
		if minSavings <= 0 {
			// A zero threshold still requires actual savings.
			return savings > 0
		}
		required := int64(math.Ceil(float64(m.Size) * minSavings))
		if required < 1 {
			required = 1
		}
		return savings >= required
	}
	// Threshold is absolute bytes
	return savings >= int64(minSavings)
}
