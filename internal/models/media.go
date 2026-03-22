package models

// ShrinkMedia represents a media file to be processed
type ShrinkMedia struct {
	Path           string
	Size           int64
	Duration       float64
	VideoCount     int
	AudioCount     int
	SubtitleCount  int
	Width          int
	Height         int
	VideoCodecs    string
	AudioCodecs    string
	SubtitleCodecs string
	MediaType      string
	Ext            string
	Category       string
	FutureSize     int64
	Savings        int64
	ProcessingTime int
	CompressedSize int64
	ArchivePath    string
	NewPath        string
	NewSize        int64
	TimeDeleted    int64
	Invalid        bool
	IsBroken       bool     // For archives: lsar failed to read contents
	PartFiles      []string // For multi-part archives: list of all part files
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
