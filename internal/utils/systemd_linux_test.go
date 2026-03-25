//go:build linux

package utils

import (
	"context"
	"strconv"
	"strings"
	"testing"
)

func TestBuildSystemdCommand(t *testing.T) {
	// Ensure systemd-run is seen as available for the test if it's in PATH,
	// or skip if we can't find it to test the full logic.
	// But we can test the formatting logic even if it's not in PATH if we could mock GetCommandPath.
	// Since we can't mock it easily without changing the code, let's just check if it's there.
	systemdRun := GetCommandPath("systemd-run")
	if systemdRun == "" {
		t.Skip("systemd-run not found, skipping full command test")
	}

	ctx := context.Background()
	exe := "echo"
	args := []string{"hello"}
	cfg := SystemdRunConfig{
		Enabled:      true,
		MemoryLimit:  1024 * 1024 * 1024, // 1GB
		MemorySwapMax: 512 * 1024 * 1024,  // 512MB
		Nice:         10,
	}

	cmd, err := buildSystemdCommand(ctx, exe, args, cfg)
	if err != nil {
		t.Fatalf("buildSystemdCommand failed: %v", err)
	}

	cmdArgs := cmd.Args
	argStr := strings.Join(cmdArgs, " ")

	// Check for MemoryMax and MemorySwapMax formatting
	expectedMemoryMax := "MemoryMax=" + strconv.FormatInt(cfg.MemoryLimit, 10)
	expectedSwapMax := "MemorySwapMax=" + strconv.FormatInt(cfg.MemorySwapMax, 10)

	foundMemoryMax := false
	foundSwapMax := false
	for _, arg := range cmdArgs {
		if arg == expectedMemoryMax {
			foundMemoryMax = true
		}
		if arg == expectedSwapMax {
			foundSwapMax = true
		}
	}

	if !foundMemoryMax {
		t.Errorf("expected %s in command args, but not found. Args: %v", expectedMemoryMax, argStr)
	}
	if !foundSwapMax {
		t.Errorf("expected %s in command args, but not found. Args: %v", expectedSwapMax, argStr)
	}

	// Check for nice value
	expectedNice := "--nice=10"
	foundNice := false
	for _, arg := range cmdArgs {
		if arg == expectedNice {
			foundNice = true
		}
	}
	if !foundNice {
		t.Errorf("expected %s in command args, but not found", expectedNice)
	}
}

func TestBuildSystemdCommandFormatting(t *testing.T) {
	// This test specifically checks the formatting logic by calling buildSystemdCommand
	// and inspecting the arguments if systemd-run is available.
	
	systemdRun := GetCommandPath("systemd-run")
	if systemdRun == "" {
		t.Skip("systemd-run not found, skipping formatting test")
	}

	tests := []struct {
		name          string
		memoryLimit   int64
		memorySwapMax int64
		wantMax       string
		wantSwap      string
	}{
		{
			name:          "1GB Limit",
			memoryLimit:   1024 * 1024 * 1024,
			memorySwapMax: 512 * 1024 * 1024,
			wantMax:       "MemoryMax=1073741824",
			wantSwap:      "MemorySwapMax=536870912",
		},
		{
			name:          "Small Limit",
			memoryLimit:   1024,
			memorySwapMax: 0, // Should default to half
			wantMax:       "MemoryMax=1024",
			wantSwap:      "MemorySwapMax=512",
		},
		{
			name:          "No Swap",
			memoryLimit:   1024,
			memorySwapMax: -1, 
			wantMax:       "MemoryMax=1024",
			wantSwap:      "MemorySwapMax=0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := SystemdRunConfig{
				Enabled:       true,
				MemoryLimit:   tt.memoryLimit,
				MemorySwapMax: tt.memorySwapMax,
			}
			cmd, err := buildSystemdCommand(context.Background(), "true", nil, cfg)
			if err != nil {
				t.Fatalf("failed: %v", err)
			}

			argStr := strings.Join(cmd.Args, " ")
			if !strings.Contains(argStr, tt.wantMax) {
				t.Errorf("expected %s in %s", tt.wantMax, argStr)
			}
			if !strings.Contains(argStr, tt.wantSwap) {
				t.Errorf("expected %s in %s", tt.wantSwap, argStr)
			}
		})
	}
}
