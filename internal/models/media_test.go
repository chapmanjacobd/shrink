package models

import (
	"testing"
)

func TestDisplayCategory(t *testing.T) {
	tests := []struct {
		m    ShrinkMedia
		want string
	}{
		{ShrinkMedia{Category: "Video", Ext: ".mp4"}, "Video: mp4"},
		{ShrinkMedia{Category: "Audio", Ext: ".mp3"}, "Audio: mp3"},
		{ShrinkMedia{Category: "Image", Ext: ".jpg"}, "Image: jpg"},
		{ShrinkMedia{Category: "Text", Ext: ".epub"}, "Text: epub"},
		{ShrinkMedia{Category: "Archived", Ext: ".zip"}, "Archived: zip"},
	}
	for _, tt := range tests {
		got := tt.m.DisplayCategory()
		if got != tt.want {
			t.Errorf("DisplayCategory() = %v, want %v", got, tt.want)
		}
	}
}

func TestProcessorConfigDefaults(t *testing.T) {
	// ProcessorConfig is mostly used for passing around, but we can ensure it exists
	cfg := &ProcessorConfig{}
	_ = cfg
}

func TestShouldShrink(t *testing.T) {
	tests := []struct {
		name        string
		size        int64
		futureSize  int64
		minSavings  float64
		forceShrink bool
		want        bool
	}{
		{
			name:       "below percentage threshold",
			size:       1000,
			futureSize: 950,
			minSavings: 0.10,
			want:       false,
		},
		{
			name:       "percentage is measured against original size",
			size:       1000,
			futureSize: 820,
			minSavings: 0.20,
			want:       false,
		},
		{
			name:       "exactly at percentage threshold",
			size:       1000,
			futureSize: 800,
			minSavings: 0.20,
			want:       true,
		},
		{
			name:       "zero percent threshold still requires savings",
			size:       1000,
			futureSize: 1000,
			minSavings: 0,
			want:       false,
		},
		{
			name:       "zero percent threshold accepts any savings",
			size:       1000,
			futureSize: 999,
			minSavings: 0,
			want:       true,
		},
		{
			name:       "absolute byte threshold",
			size:       1000,
			futureSize: 800,
			minSavings: 200,
			want:       true,
		},
		{
			name:       "below absolute byte threshold",
			size:       1000,
			futureSize: 850,
			minSavings: 200,
			want:       false,
		},
		{
			name:        "force shrink overrides threshold",
			size:        1000,
			futureSize:  1200,
			minSavings:  0.50,
			forceShrink: true,
			want:        true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &ProcessorConfig{
				Video:  VideoConfig{MinSavingsVideo: tt.minSavings},
				Common: CommonConfig{ForceShrink: tt.forceShrink},
			}
			m := &ShrinkMedia{Size: tt.size, Category: "Video"}
			if got := m.ShouldShrink(tt.futureSize, cfg); got != tt.want {
				t.Errorf("ShouldShrink(%d) = %v, want %v", tt.futureSize, got, tt.want)
			}
		})
	}
}
