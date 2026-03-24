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
		sourceBitrate := float64(utils.GetEstimatedBitrate(m.Ext))
		if sourceBitrate <= 0 {
			sourceBitrate = float64(cfg.Common.SourceVideoBitrate)
		}
		duration = float64(m.Size) / sourceBitrate * 8
	}

	targetBitrate := float64(cfg.Video.TargetVideoBitrate)
	transcodeRate := cfg.Video.TranscodingVideoRate

	if m.Width > 0 && m.Height > 0 {
		maxW := float64(cfg.Video.MaxVideoWidth)
		maxH := float64(cfg.Video.MaxVideoHeight)
		actualW := float64(m.Width)
		actualH := float64(m.Height)

		outW, outH := actualW, actualH
		if outW > maxW || outH > maxH {
			scale := math.Min(maxW/outW, maxH/outH)
			outW *= scale
			outH *= scale
		}

		baselinePixels := maxW * maxH
		outPixels := outW * outH

		if baselinePixels > 0 {
			pixelRatio := outPixels / baselinePixels
			// Don't reduce target bitrate too drastically for small videos
			if pixelRatio < 0.25 {
				pixelRatio = 0.25
			}
			if pixelRatio > 1.0 {
				pixelRatio = 1.0
			}
			targetBitrate *= pixelRatio

			sourcePixels := actualW * actualH
			if sourcePixels > outPixels && outPixels > 0 {
				// Large resolution videos (e.g. VR) take much longer to decode and resize
				complexityRatio := outPixels / sourcePixels
				if complexityRatio < 0.2 {
					complexityRatio = 0.2
				}
				transcodeRate *= complexityRatio
			}
		}
	}

	futureSize := int64(duration * targetBitrate / 8)
	processingTime := int(math.Ceil(duration / transcodeRate))

	return models.ProcessableInfo{
		FutureSize:     futureSize,
		ProcessingTime: processingTime,
		IsProcessable:  true,
	}
}

func (p *VideoProcessor) Process(ctx context.Context, m *models.ShrinkMedia, cfg *models.ProcessorConfig, registry models.ProcessorRegistry) models.ProcessResult {
	return p.ffmpeg.Process(ctx, m, cfg, registry)
}
