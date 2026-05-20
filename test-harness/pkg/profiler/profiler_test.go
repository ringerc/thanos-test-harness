package profiler

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestProfilerRSSSampling(t *testing.T) {
	p := New(10 * time.Millisecond)

	// Profile current process
	pid := os.Getpid()
	p.AddProcess("test", pid, 0) // No pprof endpoint

	ctx := context.Background()

	result, err := p.Profile(ctx, func() error {
		// Allocate some memory to see if profiler catches it
		data := make([]byte, 10*1024*1024) // 10 MB
		_ = data[0]                         // Prevent optimization
		time.Sleep(50 * time.Millisecond)
		return nil
	})

	if err != nil {
		t.Fatalf("Profile returned error: %v", err)
	}

	if result == nil {
		t.Fatal("Profile returned nil result")
	}

	if len(result.Processes) != 1 {
		t.Fatalf("Expected 1 process, got %d", len(result.Processes))
	}

	proc, ok := result.Processes["test"]
	if !ok {
		t.Fatal("Process 'test' not found in results")
	}

	if len(proc.RSSSamples) < 2 {
		t.Errorf("Expected at least 2 RSS samples, got %d", len(proc.RSSSamples))
	}

	if proc.RSSMax <= 0 {
		t.Errorf("Expected positive RSSMax, got %d", proc.RSSMax)
	}

	t.Logf("RSS samples: %d", len(proc.RSSSamples))
	t.Logf("RSS min: %s, max: %s, avg: %s, delta: %s",
		FormatBytes(proc.RSSMin),
		FormatBytes(proc.RSSMax),
		FormatBytes(proc.RSSAvg),
		FormatBytes(proc.RSSDelta))
}

func TestProfilerSummary(t *testing.T) {
	p := New(10 * time.Millisecond)
	p.AddProcess("test", os.Getpid(), 0)

	ctx := context.Background()
	result, err := p.Profile(ctx, func() error {
		time.Sleep(30 * time.Millisecond)
		return nil
	})

	if err != nil {
		t.Fatalf("Profile returned error: %v", err)
	}

	summary := result.Summary()
	if len(summary) == 0 {
		t.Error("Summary returned empty string")
	}

	t.Logf("Summary:\n%s", summary)
}

func TestReadProcMemory(t *testing.T) {
	sample := readProcMemory(os.Getpid())

	if sample.RSS <= 0 {
		t.Errorf("Expected positive RSS, got %d", sample.RSS)
	}

	if sample.VmHWM <= 0 {
		t.Errorf("Expected positive VmHWM, got %d", sample.VmHWM)
	}

	t.Logf("Current process: RSS=%s, VmHWM=%s",
		FormatBytes(sample.RSS),
		FormatBytes(sample.VmHWM))
}

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		bytes    int64
		expected string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1024, "1.0 KiB"},
		{1536, "1.5 KiB"},
		{1048576, "1.0 MiB"},
		{1073741824, "1.0 GiB"},
	}

	for _, tt := range tests {
		result := FormatBytes(tt.bytes)
		if result != tt.expected {
			t.Errorf("FormatBytes(%d) = %q, expected %q", tt.bytes, result, tt.expected)
		}
	}
}
