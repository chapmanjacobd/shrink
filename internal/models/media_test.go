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
