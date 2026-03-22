package commands

import (
	"context"
	"math"
	"strings"

	"github.com/chapmanjacobd/shrink/internal/utils"
)

// VideoProcessor handles video file processing
type VideoProcessor struct {
	BaseProcessor
	ffmpeg *FFmpegProcessor
}

func NewVideoProcessor(ffmpeg *FFmpegProcessor) *VideoProcessor {
	return &VideoProcessor{
		BaseProcessor: BaseProcessor{category: "Video"},
		ffmpeg:        ffmpeg,
	}
}

func (p *VideoProcessor) CanProcess(m *ShrinkMedia) bool {
	filetype := strings.ToLower(m.MediaType)
	return (strings.HasPrefix(filetype, "video/") || strings.Contains(filetype, " video")) ||
		(utils.VideoExtensionMap[m.Ext] && m.VideoCount >= 1)
}

func (p *VideoProcessor) EstimateSize(m *ShrinkMedia, cfg *ProcessorConfig) (int64, int) {
	duration := m.Duration
	if duration <= 0 {
		duration = float64(m.Size) / float64(cfg.SourceVideoBitrate) * 8
	}

	futureSize := int64(duration * float64(cfg.TargetVideoBitrate) / 8)
	processingTime := int(math.Ceil(duration / cfg.TranscodingVideoRate))

	return futureSize, processingTime
}

func (p *VideoProcessor) Process(ctx context.Context, m *ShrinkMedia, cfg *ProcessorConfig) ProcessResult {
	return p.ffmpeg.Process(ctx, m, cfg)
}
