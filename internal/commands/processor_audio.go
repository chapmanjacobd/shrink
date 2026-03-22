package commands

import (
	"context"
	"math"
	"strings"

	"github.com/chapmanjacobd/shrink/internal/utils"
)

// AudioProcessor handles audio file processing
type AudioProcessor struct {
	BaseProcessor
	ffmpeg *FFmpegProcessor
}

func NewAudioProcessor(ffmpeg *FFmpegProcessor) *AudioProcessor {
	return &AudioProcessor{
		BaseProcessor: BaseProcessor{category: "Audio"},
		ffmpeg:        ffmpeg,
	}
}

func (p *AudioProcessor) CanProcess(m *ShrinkMedia) bool {
	filetype := strings.ToLower(m.MediaType)
	return (strings.HasPrefix(filetype, "audio/") || strings.Contains(filetype, " audio")) ||
		(utils.AudioExtensionMap[m.Ext] && m.VideoCount == 0)
}

func (p *AudioProcessor) EstimateSize(m *ShrinkMedia, cfg *ProcessorConfig) (int64, int) {
	duration := m.Duration
	if duration <= 0 {
		duration = float64(m.Size) / float64(cfg.SourceAudioBitrate) * 8
	}

	futureSize := int64(duration * float64(cfg.TargetAudioBitrate) / 8)
	processingTime := int(math.Ceil(duration / cfg.TranscodingAudioRate))

	return futureSize, processingTime
}

func (p *AudioProcessor) Process(ctx context.Context, m *ShrinkMedia, cfg *ProcessorConfig) ProcessResult {
	return p.ffmpeg.Process(ctx, m, cfg)
}
