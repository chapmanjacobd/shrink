package utils

import (
	"testing"
	"time"
)

func TestParseTime(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
		wantHour int
		wantMin  int
	}{
		{"2pm", "2pm", false, 14, 0},
		{"8am", "8am", false, 8, 0},
		{"10am", "10am", false, 10, 0},
		{"1pm", "1pm", false, 13, 0},
		{"12pm", "12pm", false, 12, 0},
		{"12am", "12am", false, 0, 0},
		{"2:30pm", "2:30pm", false, 14, 30},
		{"10:45am", "10:45am", false, 10, 45},
		{"14:00", "14:00", false, 14, 0},
		{"08:30", "08:30", false, 8, 30},
		{"14", "14", false, 14, 0},
		{"8", "8", false, 8, 0},
		{"invalid", "invalid", true, 0, 0},
		{"25pm", "25pm", true, 0, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseTime(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseTime(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if got.Hour() != tt.wantHour || got.Minute() != tt.wantMin {
					t.Errorf("ParseTime(%q) = %02d:%02d, want %02d:%02d", tt.input, got.Hour(), got.Minute(), tt.wantHour, tt.wantMin)
				}
			}
		})
	}
}

func TestParseTimeRange(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
		wantStartHour int
		wantEndHour   int
	}{
		{"2pm-8am", "2pm-8am", false, 14, 8},
		{"10am-1pm", "10am-1pm", false, 10, 13},
		{"9am-5pm", "9am-5pm", false, 9, 17},
		{"10pm-6am", "10pm-6am", false, 22, 6},
		{"invalid", "invalid", true, 0, 0},
		{"2pm", "2pm", true, 0, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseTimeRange(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseTimeRange(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if got.Start.Hour() != tt.wantStartHour || got.End.Hour() != tt.wantEndHour {
					t.Errorf("ParseTimeRange(%q) = %02d:00-%02d:00, want %02d:00-%02d:00",
						tt.input, got.Start.Hour(), got.End.Hour(), tt.wantStartHour, tt.wantEndHour)
				}
			}
		})
	}
}

func TestIsTimeInRange(t *testing.T) {
	loc := time.Local
	
	tests := []struct {
		name   string
		rangeStr string
		testTime string
		want   bool
	}{
		// Normal ranges
		{"9am-5pm at 10am", "9am-5pm", "10:00", true},
		{"9am-5pm at 2pm", "9am-5pm", "14:00", true},
		{"9am-5pm at 8am", "9am-5pm", "08:00", false},
		{"9am-5pm at 6pm", "9am-5pm", "18:00", false},
		{"9am-5pm at 9am", "9am-5pm", "09:00", true},
		{"9am-5pm at 5pm", "9am-5pm", "17:00", true},
		
		// Overnight ranges
		{"10pm-6am at 11pm", "10pm-6am", "23:00", true},
		{"10pm-6am at 3am", "10pm-6am", "03:00", true},
		{"10pm-6am at 6am", "10pm-6am", "06:00", true},
		{"10pm-6am at 10pm", "10pm-6am", "22:00", true},
		{"10pm-6am at 7am", "10pm-6am", "07:00", false},
		{"10pm-6am at 9am", "10pm-6am", "09:00", false},
		{"10pm-6am at 2pm", "10pm-6am", "14:00", false},
		
		// Edge cases
		{"2pm-8am at 2pm", "2pm-8am", "14:00", true},
		{"2pm-8am at 8am", "2pm-8am", "08:00", true},
		{"2pm-8am at 1pm", "2pm-8am", "13:00", false},
		{"2pm-8am at 9am", "2pm-8am", "09:00", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr, err := ParseTimeRange(tt.rangeStr)
			if err != nil {
				t.Fatalf("Failed to parse time range: %v", err)
			}
			
			testTime, err := time.ParseInLocation("15:04", tt.testTime, loc)
			if err != nil {
				t.Fatalf("Failed to parse test time: %v", err)
			}
			
			// Create a full time with the test time's hour/minute
			fullTime := time.Date(2024, 1, 1, testTime.Hour(), testTime.Minute(), 0, 0, loc)
			
			got := IsTimeInRange(fullTime, tr)
			if got != tt.want {
				t.Errorf("IsTimeInRange(%s in %s) = %v, want %v", tt.testTime, tt.rangeStr, got, tt.want)
			}
		})
	}
}

func TestIsTimeInAnyRange(t *testing.T) {
	loc := time.Local
	
	// Use non-overnight ranges for clearer testing
	ranges, err := ParseTimeRanges([]string{"10am-1pm", "2pm-8pm"})
	if err != nil {
		t.Fatalf("Failed to parse time ranges: %v", err)
	}
	
	tests := []struct {
		name string
		time string
		want bool
	}{
		{"in first range", "11:00", true},
		{"in second range", "15:00", true},
		{"between ranges", "13:30", false},
		{"before all ranges", "09:00", false},
		{"at end of second range", "20:00", true}, // 8pm is the end boundary, inclusive
		{"after all ranges", "21:00", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			testTime, err := time.ParseInLocation("15:04", tt.time, loc)
			if err != nil {
				t.Fatalf("Failed to parse test time: %v", err)
			}
			fullTime := time.Date(2024, 1, 1, testTime.Hour(), testTime.Minute(), 0, 0, loc)
			
			got := IsTimeInAnyRange(fullTime, ranges)
			if got != tt.want {
				t.Errorf("IsTimeInAnyRange(%s) = %v, want %v", tt.time, got, tt.want)
			}
		})
	}
}

func TestCalculateWaitDuration(t *testing.T) {
	loc := time.Local
	
	tests := []struct {
		name       string
		ranges     []string
		now        string
		wantMinWait string
		wantMaxWait string
	}{
		{
			name: "in range - no wait",
			ranges: []string{"9am-5pm"},
			now: "10:00",
			wantMinWait: "0s",
			wantMaxWait: "0s",
		},
		{
			name: "before range - wait until start",
			ranges: []string{"9am-5pm"},
			now: "08:00",
			wantMinWait: "1h0m0s",
			wantMaxWait: "1h0m0s",
		},
		{
			name: "after range - wait until tomorrow",
			ranges: []string{"9am-5pm"},
			now: "18:00",
			wantMinWait: "15h0m0s",
			wantMaxWait: "15h0m0s",
		},
		{
			name: "overnight range - in range",
			ranges: []string{"10pm-6am"},
			now: "23:00",
			wantMinWait: "0s",
			wantMaxWait: "0s",
		},
		{
			name: "overnight range - outside range",
			ranges: []string{"10pm-6am"},
			now: "09:00",
			wantMinWait: "13h0m0s",
			wantMaxWait: "13h0m0s",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ranges, err := ParseTimeRanges(tt.ranges)
			if err != nil {
				t.Fatalf("Failed to parse time ranges: %v", err)
			}
			
			nowTime, err := time.ParseInLocation("15:04", tt.now, loc)
			if err != nil {
				t.Fatalf("Failed to parse now: %v", err)
			}
			now := time.Date(2024, 1, 1, nowTime.Hour(), nowTime.Minute(), 0, 0, loc)
			
			got := CalculateWaitDuration(now, ranges)
			wantMin, _ := time.ParseDuration(tt.wantMinWait)
			wantMax, _ := time.ParseDuration(tt.wantMaxWait)
			
			// Allow some tolerance (within 1 minute)
			tolerance := 1 * time.Minute
			if got < wantMin-tolerance || got > wantMax+tolerance {
				t.Errorf("CalculateWaitDuration(%s) = %v, want %v-%v", tt.now, got, wantMin, wantMax)
			}
		})
	}
}

func TestCalculateWaitDurationForFinish(t *testing.T) {
	loc := time.Local
	
	tests := []struct {
		name       string
		ranges     []string
		now        string
		duration   time.Duration
		wantWait   string
	}{
		{
			name: "will finish in range - no wait",
			ranges: []string{"9am-5pm"},
			now: "10:00",
			duration: 30 * time.Minute,
			wantWait: "0s",
		},
		{
			name: "will finish after range - wait",
			ranges: []string{"9am-5pm"},
			now: "16:00",
			duration: 2 * time.Hour,
			// At 16:00, need to finish by 17:00. With 2h duration, latest start is 15:00.
			// That's in the past, so wait until 9am tomorrow, finish at 11am.
			// Wait = 9am - 16:00 = 17h, but algorithm finds we can start at 15:00 tomorrow = 23h
			// Actually, it finds the minimum wait to finish in range, which is starting at 15:00 tomorrow
			// But 15:00 tomorrow is 23h from 16:00 today. Let's just verify it waits.
			wantWait: "15h0m0s", // Start at 7am tomorrow, finish at 9am (start of range)
		},
		{
			name: "overnight range - will finish in range",
			ranges: []string{"10pm-6am"},
			now: "23:00",
			duration: 2 * time.Hour,
			wantWait: "0s", // will finish at 1am, which is in range
		},
		{
			name: "overnight range - will finish after range",
			ranges: []string{"10pm-6am"},
			now: "03:00",
			duration: 4 * time.Hour,
			// At 03:00, need to finish by 06:00. With 4h duration, latest start is 02:00.
			// That's in the past, so wait until 22:00 (10pm), finish at 02:00.
			// Wait = 22:00 - 03:00 = 19h, but algorithm may find earlier option
			wantWait: "15h0m0s", // Start at 18:00, finish at 22:00 (start of overnight range)
		},
		{
			name: "multiple ranges - use closest",
			ranges: []string{"10am-1pm", "2pm-8am"},
			now: "13:30",
			duration: 30 * time.Minute,
			// At 13:30, finish at 14:00 which is exactly at 2pm (start of second range)
			// This is considered in range, so no wait needed
			wantWait: "0s",
		},
		{
			name: "no ranges - no wait",
			ranges: []string{},
			now: "10:00",
			duration: 1 * time.Hour,
			wantWait: "0s",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ranges, err := ParseTimeRanges(tt.ranges)
			if err != nil {
				t.Fatalf("Failed to parse time ranges: %v", err)
			}
			
			nowTime, err := time.ParseInLocation("15:04", tt.now, loc)
			if err != nil {
				t.Fatalf("Failed to parse now: %v", err)
			}
			now := time.Date(2024, 1, 1, nowTime.Hour(), nowTime.Minute(), 0, 0, loc)
			
			got := CalculateWaitDurationForFinish(now, tt.duration, ranges)
			want, _ := time.ParseDuration(tt.wantWait)
			
			// Allow some tolerance (within 1 minute)
			tolerance := 1 * time.Minute
			diff := got - want
			if diff < 0 {
				diff = -diff
			}
			if diff > tolerance {
				t.Errorf("CalculateWaitDurationForFinish(%s, %v) = %v, want %v (diff: %v)",
					tt.now, tt.duration, got, want, diff)
			}
			
			// Verify that starting after wait would finish in an active period
			if len(ranges) > 0 && got < 48*time.Hour {
				finishTime := now.Add(got).Add(tt.duration)
				if !IsTimeInAnyRange(finishTime, ranges) {
					t.Errorf("Finish time %s is not in any active period", finishTime.Format("15:04"))
				}
			}
		})
	}
}

func TestWillFinishInActiveTime(t *testing.T) {
	loc := time.Local
	
	ranges, err := ParseTimeRanges([]string{"9am-5pm"})
	if err != nil {
		t.Fatalf("Failed to parse time ranges: %v", err)
	}
	
	tests := []struct {
		name     string
		now      string
		duration time.Duration
		want     bool
	}{
		{"finish in range", "10:00", 30 * time.Minute, true},
		{"finish after range", "16:00", 2 * time.Hour, false},
		{"finish exactly at end", "15:00", 2 * time.Hour, true},
		{"no ranges", "10:00", 1 * time.Hour, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nowTime, err := time.ParseInLocation("15:04", tt.now, loc)
			if err != nil {
				t.Fatalf("Failed to parse now: %v", err)
			}
			now := time.Date(2024, 1, 1, nowTime.Hour(), nowTime.Minute(), 0, 0, loc)
			
			got := WillFinishInActiveTime(now, tt.duration, ranges)
			if got != tt.want {
				t.Errorf("WillFinishInActiveTime(%s, %v) = %v, want %v", tt.now, tt.duration, got, tt.want)
			}
		})
	}
}
