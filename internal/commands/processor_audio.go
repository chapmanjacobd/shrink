package commands

import (
	"context"
	"math"
	"strings"

	"github.com/chapmanjacobd/shrink/internal/ffmpeg"
	"github.com/chapmanjacobd/shrink/internal/models"
	"github.com/chapmanjacobd/shrink/internal/utils"
)

// AudioProcessor handles audio file processing
type AudioProcessor struct {
	BaseProcessor
	ffmpeg *ffmpeg.FFmpegProcessor
}

func NewAudioProcessor(ffmpeg *ffmpeg.FFmpegProcessor) *AudioProcessor {
	return &AudioProcessor{
		BaseProcessor: BaseProcessor{category: "Audio", requiredTool: "ffmpeg"},
		ffmpeg:        ffmpeg,
	}
}

func (p *AudioProcessor) CanProcess(m *models.ShrinkMedia) bool {
	return (strings.HasPrefix(m.MediaType, "audio/") || strings.Contains(m.MediaType, " audio")) ||
		(utils.AudioExtensionMap[m.Ext] && m.VideoCount == 0)
}

func (p *AudioProcessor) EstimateSize(m *models.ShrinkMedia, cfg *models.ProcessorConfig) models.ProcessableInfo {
	duration := m.Duration
	if duration <= 0 {
		duration = float64(m.Size) / float64(cfg.Common.SourceAudioBitrate) * 8
	}

	futureSize := int64(duration * float64(cfg.Audio.TargetAudioBitrate) / 8)
	processingTime := int(math.Ceil(duration / cfg.Audio.TranscodingAudioRate))

	return models.ProcessableInfo{
		FutureSize:     futureSize,
		ProcessingTime: processingTime,
		IsProcessable:  true,
	}
}

func (p *AudioProcessor) Process(ctx context.Context, m *models.ShrinkMedia, cfg *models.ProcessorConfig, registry models.ProcessorRegistry) models.ProcessResult {
	return p.ffmpeg.Process(ctx, m, cfg, registry)
}
