package commands

import (
	"context"
	"math"

	"github.com/chapmanjacobd/shrink/internal/ffmpeg"
	"github.com/chapmanjacobd/shrink/internal/models"
	"github.com/chapmanjacobd/shrink/internal/utils"
)

// VideoProcessor handles video file processing
type VideoProcessor struct {
	BaseProcessor
	ffmpeg *ffmpeg.FFmpegProcessor
}

func NewVideoProcessor(ffmpeg *ffmpeg.FFmpegProcessor) *VideoProcessor {
	return &VideoProcessor{
		BaseProcessor: BaseProcessor{category: "Video", requiredTool: "ffmpeg"},
		ffmpeg:        ffmpeg,
	}
}

func (p *VideoProcessor) CanProcess(m *models.ShrinkMedia) bool {
	return utils.VideoExtensionMap[m.Ext]
}

func (p *VideoProcessor) EstimateSize(m *models.ShrinkMedia, cfg *models.ProcessorConfig) models.ProcessableInfo {
	duration := m.Duration
	if duration <= 0 {
		duration = float64(m.Size) / float64(cfg.Common.SourceVideoBitrate) * 8
	}

	futureSize := int64(duration * float64(cfg.Video.TargetVideoBitrate) / 8)
	processingTime := int(math.Ceil(duration / cfg.Video.TranscodingVideoRate))

	return models.ProcessableInfo{
		FutureSize:     futureSize,
		ProcessingTime: processingTime,
		IsProcessable:  true,
	}
}

func (p *VideoProcessor) Process(ctx context.Context, m *models.ShrinkMedia, cfg *models.ProcessorConfig, registry models.ProcessorRegistry) models.ProcessResult {
	return p.ffmpeg.Process(ctx, m, cfg, registry)
}
