package commands

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chapmanjacobd/shrink/internal/models"
)

type MockProcessor struct {
	category string
	running  *int32
	maxSeen  *int32
	wg       *sync.WaitGroup
}

func (p *MockProcessor) Category() string                      { return p.category }
func (p *MockProcessor) RequiredTool() string                  { return "mock" }
func (p *MockProcessor) CanProcess(m *models.ShrinkMedia) bool { return m.Category == p.category }
func (p *MockProcessor) EstimateSize(m *models.ShrinkMedia, cfg *models.ProcessorConfig) models.ProcessableInfo {
	return models.ProcessableInfo{IsProcessable: true}
}

func (p *MockProcessor) Process(ctx context.Context, m *models.ShrinkMedia, cfg *models.ProcessorConfig, registry models.ProcessorRegistry) models.ProcessResult {
	atomic.AddInt32(p.running, 1)
	curr := atomic.LoadInt32(p.running)
	for {
		max := atomic.LoadInt32(p.maxSeen)
		if curr <= max || atomic.CompareAndSwapInt32(p.maxSeen, max, curr) {
			break
		}
	}
	p.wg.Done()
	// Wait for a bit to simulate work and allow parallelism to be observed
	time.Sleep(100 * time.Millisecond)
	atomic.AddInt32(p.running, -1)
	return models.ProcessResult{Success: true, Outputs: []models.ProcessOutputFile{{Path: m.Path, Size: m.Size}}}
}

func TestParallelLimits(t *testing.T) {
	cmd := &ShrinkCmd{}
	cmd.VideoThreads = 2
	cmd.AudioThreads = 3

	metrics := NewShrinkMetrics()
	// Disable TTY to avoid print noise
	metrics.isTTY = false

	var videoRunning, videoMax int32
	var audioRunning, audioMax int32

	// We use a wait group to make sure all goroutines have started and reached the process step
	var wg sync.WaitGroup
	wg.Add(10) // 5 video + 5 audio

	videoProc := &MockProcessor{category: "Video", running: &videoRunning, maxSeen: &videoMax, wg: &wg}
	audioProc := &MockProcessor{category: "Audio", running: &audioRunning, maxSeen: &audioMax, wg: &wg}

	registry := &MediaRegistry{
		processors: []models.MediaProcessor{videoProc, audioProc},
	}

	engine := NewEngine(cmd, &models.ProcessorConfig{}, registry, metrics)

	media := []models.ShrinkMedia{}
	for i := range 5 {
		vPath := filepath.Join(t.TempDir(), fmt.Sprintf("v%d.mp4", i))
		os.WriteFile(vPath, []byte("video"), 0o644)
		media = append(media, models.ShrinkMedia{Path: vPath, Category: "Video"})

		aPath := filepath.Join(t.TempDir(), fmt.Sprintf("a%d.mp3", i))
		os.WriteFile(aPath, []byte("audio"), 0o644)
		media = append(media, models.ShrinkMedia{Path: aPath, Category: "Audio"})
	}

	// Start processing in a goroutine
	done := make(chan bool)
	go func() {
		engine.processMedia(context.Background(), media)
		done <- true
	}()

	// Wait for all processors to at least start
	wg.Wait()

	// At this point, they should be at their limits
	vMax := atomic.LoadInt32(&videoMax)
	aMax := atomic.LoadInt32(&audioMax)

	if vMax > 2 {
		t.Errorf("expected max 2 video threads, got %d", vMax)
	}
	if aMax > 3 {
		t.Errorf("expected max 3 audio threads, got %d", aMax)
	}

	// Verify they ran in parallel (at least 2 videos and 3 audios should have started)
	if vMax < 2 {
		t.Errorf("expected at least 2 video threads running in parallel, got %d", vMax)
	}
	if aMax < 3 {
		t.Errorf("expected at least 3 audio threads running in parallel, got %d", aMax)
	}

	<-done
}
