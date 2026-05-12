package process

import (
	"os"
	"testing"
)

func TestCgroupReader_Version(t *testing.T) {
	reader := NewCgroupReader()
	version := reader.Version()

	if version != 1 && version != 2 {
		t.Errorf("Expected cgroup version 1 or 2, got %d", version)
	}

	t.Logf("Detected cgroup version: %d", version)
}

func TestCgroupReader_ReadStatsByPID(t *testing.T) {
	reader := NewCgroupReader()

	// Read stats for current process
	stats, err := reader.ReadStatsByPID(os.Getpid())
	if err != nil {
		t.Skipf("Could not read cgroup stats (may not be in cgroup): %v", err)
	}

	if stats.MemoryUsage < 0 {
		t.Error("Expected non-negative memory usage")
	}

	t.Logf("Current process stats: Memory=%s, CPU=%dms",
		FormatBytes(stats.MemoryUsage), stats.CPUUsageUsec/1000)
}

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		bytes    int64
		expected string
	}{
		{0, "0 B"},
		{500, "500 B"},
		{1024, "1.0 KiB"},
		{1536, "1.5 KiB"},
		{1048576, "1.0 MiB"},
		{1073741824, "1.0 GiB"},
	}

	for _, tt := range tests {
		result := FormatBytes(tt.bytes)
		if result != tt.expected {
			t.Errorf("FormatBytes(%d) = %s, want %s", tt.bytes, result, tt.expected)
		}
	}
}

func TestSystemdAvailable(t *testing.T) {
	available := systemdAvailable()
	t.Logf("systemd available: %v", available)
}

func TestReadProcStats(t *testing.T) {
	stats, err := readProcStats(os.Getpid())
	if err != nil {
		t.Fatalf("Failed to read /proc stats: %v", err)
	}

	if stats.VmRSS <= 0 {
		t.Error("Expected positive VmRSS")
	}
	if stats.VmHWM <= 0 {
		t.Error("Expected positive VmHWM (peak RSS)")
	}
	if stats.VmHWM < stats.VmRSS {
		t.Error("VmHWM (peak) should be >= VmRSS (current)")
	}

	t.Logf("Process memory: RSS=%s, Peak RSS (VmHWM)=%s",
		FormatBytes(stats.VmRSS), FormatBytes(stats.VmHWM))
}

func TestReadStatsByPID_FallbackToProc(t *testing.T) {
	reader := NewCgroupReader()
	stats, err := reader.ReadStatsByPID(os.Getpid())
	if err != nil {
		t.Skipf("Could not read stats: %v", err)
	}

	// MemoryMax should be populated (either from cgroup or /proc VmHWM)
	if stats.MemoryMax <= 0 {
		t.Error("Expected positive MemoryMax (peak memory)")
	}

	t.Logf("Stats: Current=%s, Peak=%s, RSS=%s",
		FormatBytes(stats.MemoryUsage),
		FormatBytes(stats.MemoryMax),
		FormatBytes(stats.MemoryRSS))
}

func TestParseMemoryStat(t *testing.T) {
	content := `anon 12345678
file 87654321
kernel_stack 4096
`
	anon := parseMemoryStat(content, "anon")
	if anon != 12345678 {
		t.Errorf("Expected anon=12345678, got %d", anon)
	}

	file := parseMemoryStat(content, "file")
	if file != 87654321 {
		t.Errorf("Expected file=87654321, got %d", file)
	}

	missing := parseMemoryStat(content, "missing")
	if missing != 0 {
		t.Errorf("Expected missing=0, got %d", missing)
	}
}
