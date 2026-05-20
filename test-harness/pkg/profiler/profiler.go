package profiler

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime/pprof"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/pprof/profile"
)

// MemorySample represents a single memory measurement.
type MemorySample struct {
	Timestamp time.Time
	RSS       int64 // Resident Set Size in bytes
	VmHWM     int64 // Peak RSS (high water mark)
}

// PprofHeapStats holds parsed heap profile statistics.
type PprofHeapStats struct {
	Timestamp    time.Time
	AllocBytes   int64 // Total allocated bytes
	AllocObjects int64 // Total allocated objects
	InuseBytes   int64 // Currently in-use bytes
	InuseObjects int64 // Currently in-use objects
	// Top allocations by size
	TopAllocations []Allocation
}

// Allocation represents a single allocation site.
type Allocation struct {
	Function string
	File     string
	Line     int64
	Bytes    int64
	Objects  int64
}

// ProcessProfile holds profiling data for a single process.
type ProcessProfile struct {
	Name       string
	PID        int
	HTTPPort   int // Port for pprof HTTP endpoint (0 if not available)

	// RSS samples collected during profiling
	RSSSamples []MemorySample

	// Pprof heap profiles
	HeapProfiles []PprofHeapStats

	// Computed statistics
	RSSMin     int64
	RSSMax     int64
	RSSAvg     int64
	RSSDelta   int64 // End - Start
	HeapPeak   int64 // Peak InuseBytes from pprof
}

// ProfileResult holds profiling results for all processes.
type ProfileResult struct {
	mu             sync.Mutex
	StartTime      time.Time
	EndTime        time.Time
	Duration       time.Duration
	SampleInterval time.Duration
	SampleCount    int
	Processes      map[string]*ProcessProfile
}

// Profiler collects memory profiles from multiple processes.
type Profiler struct {
	mu        sync.Mutex
	processes map[string]*processConfig
	interval  time.Duration
}

type processConfig struct {
	name     string
	pid      int
	httpPort int
}

// New creates a new profiler with the given sample interval.
func New(interval time.Duration) *Profiler {
	if interval < 10*time.Millisecond {
		interval = 10 * time.Millisecond
	}
	return &Profiler{
		processes: make(map[string]*processConfig),
		interval:  interval,
	}
}

// AddProcess adds a process to be profiled.
// httpPort is the port where the Go process exposes pprof (0 if not available).
func (p *Profiler) AddProcess(name string, pid int, httpPort int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.processes[name] = &processConfig{
		name:     name,
		pid:      pid,
		httpPort: httpPort,
	}
}

// Profile runs profiling for the duration of the provided function.
// It starts sampling before calling fn and stops after fn returns.
func (p *Profiler) Profile(ctx context.Context, fn func() error) (*ProfileResult, error) {
	result := &ProfileResult{
		StartTime:      time.Now(),
		SampleInterval: p.interval,
		Processes:      make(map[string]*ProcessProfile),
	}

	p.mu.Lock()
	processes := make(map[string]*processConfig, len(p.processes))
	for k, v := range p.processes {
		processes[k] = v
		result.Processes[k] = &ProcessProfile{
			Name:     v.name,
			PID:      v.pid,
			HTTPPort: v.httpPort,
		}
	}
	p.mu.Unlock()

	// Channel to stop sampling
	stopCh := make(chan struct{})
	var wg sync.WaitGroup

	// Start RSS sampling goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()
		p.sampleRSSLoop(ctx, processes, result, stopCh)
	}()

	// Start pprof sampling goroutine (less frequent)
	wg.Add(1)
	go func() {
		defer wg.Done()
		p.samplePprofLoop(ctx, processes, result, stopCh)
	}()

	// Run the actual function
	fnErr := fn()

	// Stop sampling
	close(stopCh)
	wg.Wait()

	result.EndTime = time.Now()
	result.Duration = result.EndTime.Sub(result.StartTime)

	// Compute statistics
	for _, proc := range result.Processes {
		computeStats(proc)
	}

	return result, fnErr
}

func (p *Profiler) sampleRSSLoop(ctx context.Context, processes map[string]*processConfig, result *ProfileResult, stopCh <-chan struct{}) {
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()

	// Take initial sample immediately
	p.sampleRSS(processes, result)

	for {
		select {
		case <-ctx.Done():
			return
		case <-stopCh:
			// Take final sample
			p.sampleRSS(processes, result)
			return
		case <-ticker.C:
			p.sampleRSS(processes, result)
		}
	}
}

func (p *Profiler) sampleRSS(processes map[string]*processConfig, result *ProfileResult) {
	now := time.Now()
	for name, proc := range processes {
		sample := readProcMemory(proc.pid)
		sample.Timestamp = now

		result.mu.Lock()
		if pp, ok := result.Processes[name]; ok {
			pp.RSSSamples = append(pp.RSSSamples, sample)
		}
		result.mu.Unlock()
	}
	result.mu.Lock()
	result.SampleCount++
	result.mu.Unlock()
}

func (p *Profiler) samplePprofLoop(ctx context.Context, processes map[string]*processConfig, result *ProfileResult, stopCh <-chan struct{}) {
	// Sample pprof less frequently (every 100ms or 10x the RSS interval, whichever is larger)
	pprofInterval := p.interval * 10
	if pprofInterval < 100*time.Millisecond {
		pprofInterval = 100 * time.Millisecond
	}

	ticker := time.NewTicker(pprofInterval)
	defer ticker.Stop()

	// Take initial sample
	p.samplePprof(ctx, processes, result)

	for {
		select {
		case <-ctx.Done():
			return
		case <-stopCh:
			// Take final sample
			p.samplePprof(ctx, processes, result)
			return
		case <-ticker.C:
			p.samplePprof(ctx, processes, result)
		}
	}
}

func (p *Profiler) samplePprof(ctx context.Context, processes map[string]*processConfig, result *ProfileResult) {
	var wg sync.WaitGroup
	for name, proc := range processes {
		if proc.httpPort == 0 {
			continue
		}
		wg.Add(1)
		go func(name string, proc *processConfig) {
			defer wg.Done()
			stats, err := fetchHeapProfile(ctx, proc.httpPort)
			if err != nil {
				return // Silently skip failed fetches
			}
			result.mu.Lock()
			if pp, ok := result.Processes[name]; ok {
				pp.HeapProfiles = append(pp.HeapProfiles, *stats)
			}
			result.mu.Unlock()
		}(name, proc)
	}
	wg.Wait()
}

// readProcMemory reads memory stats from /proc/{pid}/status.
func readProcMemory(pid int) MemorySample {
	sample := MemorySample{}

	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return sample
	}

	for _, line := range strings.Split(string(data), "\n") {
		switch {
		case strings.HasPrefix(line, "VmRSS:"):
			sample.RSS = parseKBLine(line) * 1024
		case strings.HasPrefix(line, "VmHWM:"):
			sample.VmHWM = parseKBLine(line) * 1024
		}
	}
	return sample
}

func parseKBLine(line string) int64 {
	fields := strings.Fields(line)
	if len(fields) >= 2 {
		v, _ := strconv.ParseInt(fields[1], 10, 64)
		return v
	}
	return 0
}

// fetchHeapProfile fetches and parses a heap profile from the pprof endpoint.
func fetchHeapProfile(ctx context.Context, port int) (*PprofHeapStats, error) {
	url := fmt.Sprintf("http://localhost:%d/debug/pprof/heap", port)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("pprof returned status %d", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	return parseHeapProfile(data)
}

// parseHeapProfile parses a protobuf-encoded heap profile.
func parseHeapProfile(data []byte) (*PprofHeapStats, error) {
	prof, err := profile.ParseData(data)
	if err != nil {
		return nil, fmt.Errorf("parse profile: %w", err)
	}

	stats := &PprofHeapStats{
		Timestamp: time.Now(),
	}

	// Find the sample types we care about
	var allocBytesIdx, allocObjIdx, inuseBytesIdx, inuseObjIdx int = -1, -1, -1, -1
	for i, st := range prof.SampleType {
		switch st.Type {
		case "alloc_space":
			allocBytesIdx = i
		case "alloc_objects":
			allocObjIdx = i
		case "inuse_space":
			inuseBytesIdx = i
		case "inuse_objects":
			inuseObjIdx = i
		}
	}

	// Aggregate totals and track top allocations
	type allocKey struct {
		function string
		file     string
		line     int64
	}
	allocMap := make(map[allocKey]*Allocation)

	for _, sample := range prof.Sample {
		if allocBytesIdx >= 0 && len(sample.Value) > allocBytesIdx {
			stats.AllocBytes += sample.Value[allocBytesIdx]
		}
		if allocObjIdx >= 0 && len(sample.Value) > allocObjIdx {
			stats.AllocObjects += sample.Value[allocObjIdx]
		}
		if inuseBytesIdx >= 0 && len(sample.Value) > inuseBytesIdx {
			stats.InuseBytes += sample.Value[inuseBytesIdx]
		}
		if inuseObjIdx >= 0 && len(sample.Value) > inuseObjIdx {
			stats.InuseObjects += sample.Value[inuseObjIdx]
		}

		// Track allocation by top frame
		if len(sample.Location) > 0 && len(sample.Location[0].Line) > 0 {
			loc := sample.Location[0]
			line := loc.Line[0]
			var funcName, fileName string
			var lineNo int64
			if line.Function != nil {
				funcName = line.Function.Name
				fileName = line.Function.Filename
			}
			lineNo = line.Line

			key := allocKey{funcName, fileName, lineNo}
			if _, ok := allocMap[key]; !ok {
				allocMap[key] = &Allocation{
					Function: funcName,
					File:     fileName,
					Line:     lineNo,
				}
			}
			if inuseBytesIdx >= 0 && len(sample.Value) > inuseBytesIdx {
				allocMap[key].Bytes += sample.Value[inuseBytesIdx]
			}
			if inuseObjIdx >= 0 && len(sample.Value) > inuseObjIdx {
				allocMap[key].Objects += sample.Value[inuseObjIdx]
			}
		}
	}

	// Convert map to slice and sort by bytes
	allocs := make([]Allocation, 0, len(allocMap))
	for _, a := range allocMap {
		allocs = append(allocs, *a)
	}
	sort.Slice(allocs, func(i, j int) bool {
		return allocs[i].Bytes > allocs[j].Bytes
	})

	// Keep top 10
	if len(allocs) > 10 {
		allocs = allocs[:10]
	}
	stats.TopAllocations = allocs

	return stats, nil
}

func computeStats(proc *ProcessProfile) {
	if len(proc.RSSSamples) == 0 {
		return
	}

	var total int64
	proc.RSSMin = proc.RSSSamples[0].RSS
	proc.RSSMax = proc.RSSSamples[0].RSS

	for _, s := range proc.RSSSamples {
		total += s.RSS
		if s.RSS < proc.RSSMin {
			proc.RSSMin = s.RSS
		}
		if s.RSS > proc.RSSMax {
			proc.RSSMax = s.RSS
		}
	}
	proc.RSSAvg = total / int64(len(proc.RSSSamples))
	proc.RSSDelta = proc.RSSSamples[len(proc.RSSSamples)-1].RSS - proc.RSSSamples[0].RSS

	// Find peak heap from pprof samples
	for _, hp := range proc.HeapProfiles {
		if hp.InuseBytes > proc.HeapPeak {
			proc.HeapPeak = hp.InuseBytes
		}
	}
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

// Summary returns a human-readable summary of the profile.
func (r *ProfileResult) Summary() string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("Profile Duration: %v (%d samples @ %v interval)\n\n",
		r.Duration, r.SampleCount, r.SampleInterval))

	// Sort process names for consistent output
	names := make([]string, 0, len(r.Processes))
	for name := range r.Processes {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		proc := r.Processes[name]
		sb.WriteString(fmt.Sprintf("=== %s (PID %d) ===\n", proc.Name, proc.PID))
		sb.WriteString(fmt.Sprintf("  RSS: min=%s, max=%s, avg=%s, delta=%s\n",
			FormatBytes(proc.RSSMin),
			FormatBytes(proc.RSSMax),
			FormatBytes(proc.RSSAvg),
			FormatBytes(proc.RSSDelta)))

		if proc.HeapPeak > 0 {
			sb.WriteString(fmt.Sprintf("  Heap Peak (pprof): %s\n", FormatBytes(proc.HeapPeak)))
		}

		if len(proc.HeapProfiles) > 0 {
			last := proc.HeapProfiles[len(proc.HeapProfiles)-1]
			sb.WriteString(fmt.Sprintf("  Final Heap: inuse=%s (%d obj), alloc=%s (%d obj)\n",
				FormatBytes(last.InuseBytes), last.InuseObjects,
				FormatBytes(last.AllocBytes), last.AllocObjects))

			if len(last.TopAllocations) > 0 {
				sb.WriteString("  Top Allocations:\n")
				for i, a := range last.TopAllocations[:min(5, len(last.TopAllocations))] {
					sb.WriteString(fmt.Sprintf("    %d. %s: %s (%d obj)\n",
						i+1, shortenFunc(a.Function), FormatBytes(a.Bytes), a.Objects))
				}
			}
		}
		sb.WriteString("\n")
	}

	return sb.String()
}

func shortenFunc(fn string) string {
	// Shorten long function names
	if len(fn) > 60 {
		parts := strings.Split(fn, "/")
		if len(parts) > 2 {
			fn = ".../" + strings.Join(parts[len(parts)-2:], "/")
		}
	}
	return fn
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// WriteHeapProfile writes a heap profile of the current process to a writer.
// This is useful for profiling the harness itself.
func WriteHeapProfile(w io.Writer) error {
	return pprof.WriteHeapProfile(w)
}
