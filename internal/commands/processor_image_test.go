package commands

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/chapmanjacobd/shrink/internal/models"
)

func TestImageMagickErrors(t *testing.T) {
	tests := []struct {
		name     string
		logs     []string
		check    func([]string) bool
		expected bool
	}{
		{
			name:     "unsupported format",
			logs:     []string{"no decode delegate for this image format"},
			check:    isImageMagickUnsupportedError,
			expected: true,
		},
		{
			name:     "file error",
			logs:     []string{"unable to open image"},
			check:    isImageMagickFileError,
			expected: true,
		},
		{
			name:     "environment error",
			logs:     []string{"killed"},
			check:    isImageMagickEnvironmentError,
			expected: true,
		},
		{
			name:     "resource limit",
			logs:     []string{"magick: cache resources exhausted"},
			check:    isImageMagickResourceLimitError,
			expected: true,
		},
		{
			name:     "resource limit is not an environment error",
			logs:     []string{"magick: cache resources exhausted"},
			check:    isImageMagickEnvironmentError,
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.check(tt.logs); got != tt.expected {
				t.Errorf("check(%v) = %v, want %v", tt.logs, got, tt.expected)
			}
		})
	}
}

func TestImageMagickLimitArgs(t *testing.T) {
	tests := []struct {
		name          string
		cacheLimit    int64
		maxPixels     int64
		expected      []string
		expectedEmpty bool
	}{
		{
			name:          "limits disabled",
			expectedEmpty: true,
		},
		{
			name:       "cache limits",
			cacheLimit: 1 << 30,
			maxPixels:  100000000,
			expected: []string{
				"-limit", "memory", "1073741824",
				"-limit", "map", "1073741824",
				"-limit", "disk", "1073741824",
			},
		},
		{
			name:       "only cache limit",
			cacheLimit: 512 << 20,
			expected: []string{
				"-limit", "memory", "536870912",
				"-limit", "map", "536870912",
				"-limit", "disk", "536870912",
			},
		},
		{
			name:          "only pixel limit",
			maxPixels:     250000000,
			expectedEmpty: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &models.ProcessorConfig{Image: models.ImageConfig{
				ImageCacheLimit: tt.cacheLimit,
				MaxImagePixels:  tt.maxPixels,
			}}
			got := imageMagickLimitArgs(cfg)
			if tt.expectedEmpty {
				if len(got) != 0 {
					t.Errorf("imageMagickLimitArgs() = %v, want empty", got)
				}
				return
			}
			if !reflect.DeepEqual(got, tt.expected) {
				t.Errorf("imageMagickLimitArgs() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestEstimateGeospatial(t *testing.T) {
	tests := []struct {
		name             string
		mode             string
		hasGDAL          bool
		alreadyOptimized bool
		maxImageBytes    int64
		size             int64
		processable      bool
		expectedFuture   int64
	}{
		{
			name:           "cog with GDAL",
			mode:           "cog",
			hasGDAL:        true,
			size:           1000,
			processable:    true,
			expectedFuture: 500,
		},
		{
			name:           "cog without GDAL",
			mode:           "cog",
			hasGDAL:        false,
			size:           1000,
			processable:    false,
			expectedFuture: 0,
		},
		{
			name:             "already optimized",
			mode:             "cog",
			hasGDAL:          true,
			alreadyOptimized: true,
			size:             1000,
			processable:      false,
			expectedFuture:   0,
		},
		{
			name:           "skip mode",
			mode:           "skip",
			hasGDAL:        true,
			size:           1000,
			processable:    false,
			expectedFuture: 0,
		},
		{
			name:           "avif mode",
			mode:           "avif",
			size:           1000,
			processable:    true,
			expectedFuture: 30 << 10,
		},
		{
			name:           "avif mode skips oversized",
			mode:           "avif",
			maxImageBytes:  500,
			size:           1000,
			processable:    false,
			expectedFuture: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &models.ShrinkMedia{Path: "map.tif", Size: tt.size}
			cfg := &models.ProcessorConfig{Image: models.ImageConfig{
				TargetImageSize:      30 << 10,
				TranscodingImageTime: 1.5,
				MaxImageBytes:        tt.maxImageBytes,
				GeotiffMode:          tt.mode,
			}}
			info := estimateGeospatial(m, cfg, tt.alreadyOptimized, tt.hasGDAL)
			if info.IsProcessable != tt.processable {
				t.Errorf("IsProcessable = %v, want %v", info.IsProcessable, tt.processable)
			}
			if info.FutureSize != tt.expectedFuture {
				t.Errorf("FutureSize = %d, want %d", info.FutureSize, tt.expectedFuture)
			}
		})
	}
}

func TestEstimateSizeSkipsOversizedImage(t *testing.T) {
	cfg := &models.ProcessorConfig{Image: models.ImageConfig{
		TargetImageSize:      30 << 10,
		TranscodingImageTime: 1.5,
		MaxImageBytes:        200 << 20,
		MaxImagePixels:       100 * 1000 * 1000,
		GeotiffMode:          "cog",
	}}
	processor := NewImageProcessor()

	tests := []struct {
		name        string
		size        int64
		width       int
		height      int
		processable bool
	}{
		{name: "at size limit", size: 200 << 20, processable: true},
		{name: "over size limit", size: (200 << 20) + 1, processable: false},
		{name: "at pixel limit", size: 1000, width: 10000, height: 10000, processable: true},
		{name: "over pixel limit", size: 1000, width: 10001, height: 10000, processable: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &models.ShrinkMedia{
				Path:   "photo.jpg",
				Ext:    ".jpg",
				Size:   tt.size,
				Width:  tt.width,
				Height: tt.height,
			}
			info := processor.EstimateSize(m, cfg)
			if info.IsProcessable != tt.processable {
				t.Errorf("IsProcessable = %v, want %v", info.IsProcessable, tt.processable)
			}
		})
	}
}

func TestProcessImageSkipsTooManyPixels(t *testing.T) {
	cfg := &models.ProcessorConfig{Image: models.ImageConfig{
		TargetImageSize:      30 << 10,
		MaxImageWidth:        2400,
		MaxImageHeight:       2400,
		MaxImagePixels:       1000 * 1000,
		TranscodingImageTime: 1.5,
		GeotiffMode:          "cog",
	}}
	processor := NewImageProcessor()
	m := &models.ShrinkMedia{
		Path:   "photo.jpg",
		Ext:    ".jpg",
		Size:   1000,
		Width:  2000,
		Height: 2000,
	}

	result := processor.processImage(context.Background(), m, cfg)
	if !result.Success {
		t.Fatalf("expected keeping the original to succeed, got error: %v", result.Error)
	}
	if len(result.Outputs) != 1 || result.Outputs[0].Path != m.Path || result.Outputs[0].Size != m.Size {
		t.Errorf("expected original file as output, got %+v", result.Outputs)
	}
}

func TestReplaceFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "new.tif")
	dst := filepath.Join(dir, "old.tif")
	if err := os.WriteFile(src, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := replaceFile(src, dst, 0o600); err != nil {
		t.Fatalf("replaceFile: %v", err)
	}

	data, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "new" {
		t.Errorf("destination content = %q, want %q", data, "new")
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Errorf("source still exists after replacement")
	}
}
