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
		Enabled:       true,
		MemoryLimit:   1024 * 1024 * 1024, // 1GB
		MemorySwapMax: 512 * 1024 * 1024,  // 512MB
		Nice:          10,
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

func TestBuildSystemdCommandMemoryLimits(t *testing.T) {
	systemdRun := GetCommandPath("systemd-run")
	if systemdRun == "" {
		t.Skip("systemd-run not found, skipping memory limits test")
	}

	tests := []struct {
		name              string
		cfg               SystemdRunConfig
		wantMemoryMax     string
		wantMemorySwapMax string
		wantNoMemoryLimit bool
	}{
		{
			name: "2GB limit with 1GB swap",
			cfg: SystemdRunConfig{
				Enabled:       true,
				MemoryLimit:   2 * 1024 * 1024 * 1024,
				MemorySwapMax: 1024 * 1024 * 1024,
			},
			wantMemoryMax:     "MemoryMax=2147483648",
			wantMemorySwapMax: "MemorySwapMax=1073741824",
		},
		{
			name: "512MB limit with default swap (half)",
			cfg: SystemdRunConfig{
				Enabled:     true,
				MemoryLimit: 512 * 1024 * 1024,
			},
			wantMemoryMax:     "MemoryMax=536870912",
			wantMemorySwapMax: "MemorySwapMax=268435456",
		},
		{
			name: "swap explicitly disabled",
			cfg: SystemdRunConfig{
				Enabled:       true,
				MemoryLimit:   1024 * 1024 * 1024,
				MemorySwapMax: -1, // Use -1 to explicitly disable swap
			},
			wantMemoryMax:     "MemoryMax=1073741824",
			wantMemorySwapMax: "MemorySwapMax=0",
		},
		{
			name: "negative swap disables swap",
			cfg: SystemdRunConfig{
				Enabled:       true,
				MemoryLimit:   1024 * 1024 * 1024,
				MemorySwapMax: -1,
			},
			wantMemoryMax:     "MemoryMax=1073741824",
			wantMemorySwapMax: "MemorySwapMax=0",
		},
		{
			name: "no memory limit (direct execution)",
			cfg: SystemdRunConfig{
				Enabled:     true,
				MemoryLimit: 0,
			},
			wantNoMemoryLimit: true,
		},
		{
			name: "disabled systemd-run (direct execution)",
			cfg: SystemdRunConfig{
				Enabled:     false,
				MemoryLimit: 1024 * 1024 * 1024,
			},
			wantNoMemoryLimit: true,
		},
		{
			name: "large memory value",
			cfg: SystemdRunConfig{
				Enabled:       true,
				MemoryLimit:   8 * 1024 * 1024 * 1024,
				MemorySwapMax: 4 * 1024 * 1024 * 1024,
			},
			wantMemoryMax:     "MemoryMax=8589934592",
			wantMemorySwapMax: "MemorySwapMax=4294967296",
		},
		{
			name: "minimum memory value",
			cfg: SystemdRunConfig{
				Enabled:       true,
				MemoryLimit:   1,
				MemorySwapMax: 0,
			},
			wantMemoryMax:     "MemoryMax=1",
			wantMemorySwapMax: "MemorySwapMax=0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd, err := buildSystemdCommand(context.Background(), "true", nil, tt.cfg)
			if err != nil {
				t.Fatalf("buildSystemdCommand failed: %v", err)
			}

			argStr := strings.Join(cmd.Args, " ")

			if tt.wantNoMemoryLimit {
				// Should not contain MemoryMax
				if strings.Contains(argStr, "MemoryMax") {
					t.Errorf("expected no MemoryMax in command, but got: %s", argStr)
				}
				return
			}

			if !strings.Contains(argStr, tt.wantMemoryMax) {
				t.Errorf("expected %s in %s", tt.wantMemoryMax, argStr)
			}
			if tt.wantMemorySwapMax != "" && !strings.Contains(argStr, tt.wantMemorySwapMax) {
				t.Errorf("expected %s in %s", tt.wantMemorySwapMax, argStr)
			}
		})
	}
}

func TestBuildSystemdCommandSwapDefaultLogic(t *testing.T) {
	systemdRun := GetCommandPath("systemd-run")
	if systemdRun == "" {
		t.Skip("systemd-run not found, skipping swap default logic test")
	}

	tests := []struct {
		name          string
		memoryLimit   int64
		memorySwapMax int64
		wantSwap      int64
	}{
		{
			name:          "swap defaults to half of memory",
			memoryLimit:   1000,
			memorySwapMax: 0,
			wantSwap:      500,
		},
		{
			name:          "swap defaults to half (odd number)",
			memoryLimit:   1025,
			memorySwapMax: 0,
			wantSwap:      512,
		},
		{
			name:          "explicit swap value is used",
			memoryLimit:   1000,
			memorySwapMax: 200,
			wantSwap:      200,
		},
		{
			name:          "zero swap when MemorySwapMax is 0",
			memoryLimit:   1000,
			memorySwapMax: 0,
			wantSwap:      500, // default to half
		},
		{
			name:          "negative swap becomes 0",
			memoryLimit:   1000,
			memorySwapMax: -100,
			wantSwap:      0,
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
				t.Fatalf("buildSystemdCommand failed: %v", err)
			}

			argStr := strings.Join(cmd.Args, " ")
			expectedSwap := "MemorySwapMax=" + strconv.FormatInt(tt.wantSwap, 10)
			if !strings.Contains(argStr, expectedSwap) {
				t.Errorf("expected %s in %s", expectedSwap, argStr)
			}
		})
	}
}

// TestParseSizeToSystemdCommandE2E tests the full end-to-end flow from CLI string
// parsing to systemd-run command building, including the default 8GB value.
func TestParseSizeToSystemdCommandE2E(t *testing.T) {
	systemdRun := GetCommandPath("systemd-run")
	if systemdRun == "" {
		t.Skip("systemd-run not found, skipping e2e test")
	}

	tests := []struct {
		name              string
		cliMemoryLimit    string
		cliMemorySwapMax  string
		wantMemoryMax     string
		wantMemorySwapMax string
		wantNoSystemd     bool // true if systemd-run should not be used
	}{
		{
			name:              "default empty (8GB default)",
			cliMemoryLimit:    "",
			cliMemorySwapMax:  "",
			wantMemoryMax:     "MemoryMax=8589934592",     // 8 * 1024 * 1024 * 1024
			wantMemorySwapMax: "MemorySwapMax=4294967296", // half of 8GB
		},
		{
			name:              "4G memory limit",
			cliMemoryLimit:    "4G",
			cliMemorySwapMax:  "",
			wantMemoryMax:     "MemoryMax=4294967296",
			wantMemorySwapMax: "MemorySwapMax=2147483648", // half of 4G
		},
		{
			name:              "512M memory limit",
			cliMemoryLimit:    "512M",
			cliMemorySwapMax:  "",
			wantMemoryMax:     "MemoryMax=536870912",
			wantMemorySwapMax: "MemorySwapMax=268435456", // half of 512M
		},
		{
			name:              "2G with 1G swap",
			cliMemoryLimit:    "2G",
			cliMemorySwapMax:  "1G",
			wantMemoryMax:     "MemoryMax=2147483648",
			wantMemorySwapMax: "MemorySwapMax=1073741824",
		},
		{
			name:              "swap disabled with 0",
			cliMemoryLimit:    "2G",
			cliMemorySwapMax:  "0",
			wantMemoryMax:     "MemoryMax=2147483648",
			wantMemorySwapMax: "MemorySwapMax=0",
		},
		{
			name:             "0 memory limit (no systemd)",
			cliMemoryLimit:   "0",
			cliMemorySwapMax: "",
			wantNoSystemd:    true,
		},
		{
			name:              "1GiB explicit",
			cliMemoryLimit:    "1GiB",
			cliMemorySwapMax:  "",
			wantMemoryMax:     "MemoryMax=1073741824",
			wantMemorySwapMax: "MemorySwapMax=536870912",
		},
		{
			name:              "1024MiB explicit",
			cliMemoryLimit:    "1024MiB",
			cliMemorySwapMax:  "",
			wantMemoryMax:     "MemoryMax=1073741824",
			wantMemorySwapMax: "MemorySwapMax=536870912",
		},
		{
			name:              "1048576KiB explicit",
			cliMemoryLimit:    "1048576KiB",
			cliMemorySwapMax:  "",
			wantMemoryMax:     "MemoryMax=1073741824",
			wantMemorySwapMax: "MemorySwapMax=536870912",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Parse CLI strings to int64 (simulating config.go logic)
			memoryLimit := ParseSize(tt.cliMemoryLimit)
			if memoryLimit == 0 && tt.cliMemoryLimit == "" {
				// Default to 8GB when empty
				memoryLimit = 8 * 1024 * 1024 * 1024
			}

			var memorySwapMax int64
			if tt.cliMemorySwapMax == "0" {
				memorySwapMax = -1 // Explicitly disable swap
			} else {
				memorySwapMax = ParseSize(tt.cliMemorySwapMax)
			}

			cfg := SystemdRunConfig{
				Enabled:       true,
				MemoryLimit:   memoryLimit,
				MemorySwapMax: memorySwapMax,
			}

			cmd, err := buildSystemdCommand(context.Background(), "ffmpeg", []string{"-i", "input.mkv"}, cfg)
			if err != nil {
				t.Fatalf("buildSystemdCommand failed: %v", err)
			}

			argStr := strings.Join(cmd.Args, " ")

			if tt.wantNoSystemd {
				// Should not contain systemd-run memory limits
				if strings.Contains(argStr, "MemoryMax") {
					t.Errorf("expected no MemoryMax in command, but got: %s", argStr)
				}
				return
			}

			if !strings.Contains(argStr, tt.wantMemoryMax) {
				t.Errorf("expected %s in %s", tt.wantMemoryMax, argStr)
			}
			if tt.wantMemorySwapMax != "" && !strings.Contains(argStr, tt.wantMemorySwapMax) {
				t.Errorf("expected %s in %s", tt.wantMemorySwapMax, argStr)
			}
		})
	}
}
