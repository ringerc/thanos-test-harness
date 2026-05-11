package process

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// CgroupStats holds resource usage statistics from cgroup.
type CgroupStats struct {
	Timestamp time.Time

	// Memory stats (bytes)
	MemoryUsage    int64 // current memory usage
	MemoryMax      int64 // peak memory usage
	MemoryLimit    int64 // memory limit (if set)
	MemoryCache    int64 // page cache
	MemoryRSS      int64 // resident set size
	MemorySwap     int64 // swap usage
	MemoryOOMKills int64 // OOM kill count

	// CPU stats (microseconds)
	CPUUsageUsec int64 // total CPU time
	CPUUserUsec  int64 // user CPU time
	CPUSysUsec   int64 // system CPU time
}

// CgroupReader reads stats from cgroup filesystem.
type CgroupReader struct {
	version int // 1 or 2
}

// NewCgroupReader creates a reader for the detected cgroup version.
func NewCgroupReader() *CgroupReader {
	version := detectCgroupVersion()
	return &CgroupReader{version: version}
}

// ReadStats reads cgroup stats for a given path.
func (r *CgroupReader) ReadStats(cgroupPath string) (*CgroupStats, error) {
	if cgroupPath == "" {
		return nil, fmt.Errorf("empty cgroup path")
	}

	stats := &CgroupStats{Timestamp: time.Now()}

	if r.version == 2 {
		return r.readCgroupV2(cgroupPath, stats)
	}
	return r.readCgroupV1(cgroupPath, stats)
}

// ReadStatsByPID reads cgroup stats for a process by PID.
func (r *CgroupReader) ReadStatsByPID(pid int) (*CgroupStats, error) {
	cgroupPath, err := r.getCgroupPathForPID(pid)
	if err != nil {
		return nil, err
	}
	return r.ReadStats(cgroupPath)
}

func (r *CgroupReader) readCgroupV2(path string, stats *CgroupStats) (*CgroupStats, error) {
	// memory.current
	if data, err := os.ReadFile(filepath.Join(path, "memory.current")); err == nil {
		stats.MemoryUsage = parseIntFile(string(data))
	}

	// memory.peak (if available)
	if data, err := os.ReadFile(filepath.Join(path, "memory.peak")); err == nil {
		stats.MemoryMax = parseIntFile(string(data))
	}

	// memory.max
	if data, err := os.ReadFile(filepath.Join(path, "memory.max")); err == nil {
		s := strings.TrimSpace(string(data))
		if s != "max" {
			stats.MemoryLimit = parseIntFile(s)
		}
	}

	// memory.stat for detailed breakdown
	if data, err := os.ReadFile(filepath.Join(path, "memory.stat")); err == nil {
		content := string(data)
		stats.MemoryCache = parseMemoryStat(content, "file")
		stats.MemoryRSS = parseMemoryStat(content, "anon")
	}

	// memory.swap.current
	if data, err := os.ReadFile(filepath.Join(path, "memory.swap.current")); err == nil {
		stats.MemorySwap = parseIntFile(string(data))
	}

	// memory.events for OOM
	if data, err := os.ReadFile(filepath.Join(path, "memory.events")); err == nil {
		content := string(data)
		stats.MemoryOOMKills = parseMemoryStat(content, "oom_kill")
	}

	// cpu.stat
	if data, err := os.ReadFile(filepath.Join(path, "cpu.stat")); err == nil {
		content := string(data)
		stats.CPUUsageUsec = parseMemoryStat(content, "usage_usec")
		stats.CPUUserUsec = parseMemoryStat(content, "user_usec")
		stats.CPUSysUsec = parseMemoryStat(content, "system_usec")
	}

	return stats, nil
}

func (r *CgroupReader) readCgroupV1(path string, stats *CgroupStats) (*CgroupStats, error) {
	// cgroup v1 has separate hierarchies for memory and cpu
	memPath := filepath.Join("/sys/fs/cgroup/memory", filepath.Base(path))
	cpuPath := filepath.Join("/sys/fs/cgroup/cpu,cpuacct", filepath.Base(path))

	// memory.usage_in_bytes
	if data, err := os.ReadFile(filepath.Join(memPath, "memory.usage_in_bytes")); err == nil {
		stats.MemoryUsage = parseIntFile(string(data))
	}

	// memory.max_usage_in_bytes
	if data, err := os.ReadFile(filepath.Join(memPath, "memory.max_usage_in_bytes")); err == nil {
		stats.MemoryMax = parseIntFile(string(data))
	}

	// memory.limit_in_bytes
	if data, err := os.ReadFile(filepath.Join(memPath, "memory.limit_in_bytes")); err == nil {
		stats.MemoryLimit = parseIntFile(string(data))
	}

	// memory.stat
	if data, err := os.ReadFile(filepath.Join(memPath, "memory.stat")); err == nil {
		content := string(data)
		stats.MemoryCache = parseMemoryStat(content, "cache")
		stats.MemoryRSS = parseMemoryStat(content, "rss")
	}

	// cpuacct.usage (nanoseconds -> convert to microseconds)
	if data, err := os.ReadFile(filepath.Join(cpuPath, "cpuacct.usage")); err == nil {
		stats.CPUUsageUsec = parseIntFile(string(data)) / 1000
	}

	return stats, nil
}

func (r *CgroupReader) getCgroupPathForPID(pid int) (string, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", pid))
	if err != nil {
		return "", fmt.Errorf("read cgroup for pid %d: %w", pid, err)
	}

	if r.version == 2 {
		// cgroup v2: single line "0::/path"
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "0::") {
				return "/sys/fs/cgroup" + strings.TrimPrefix(line, "0::"), nil
			}
		}
	} else {
		// cgroup v1: multiple lines, use memory hierarchy
		for _, line := range strings.Split(string(data), "\n") {
			parts := strings.SplitN(line, ":", 3)
			if len(parts) == 3 && strings.Contains(parts[1], "memory") {
				return parts[2], nil
			}
		}
	}
	return "", fmt.Errorf("cgroup path not found for pid %d", pid)
}

// detectCgroupVersion returns 1 or 2 based on the system's cgroup setup.
func detectCgroupVersion() int {
	// Check for cgroup v2 unified hierarchy
	if _, err := os.Stat("/sys/fs/cgroup/cgroup.controllers"); err == nil {
		return 2
	}
	return 1
}

// Version returns the detected cgroup version.
func (r *CgroupReader) Version() int {
	return r.version
}

func parseIntFile(content string) int64 {
	v, _ := strconv.ParseInt(strings.TrimSpace(content), 10, 64)
	return v
}

// FormatBytes formats bytes as human-readable string.
func FormatBytes(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(bytes)/float64(div), "KMGTPE"[exp])
}
