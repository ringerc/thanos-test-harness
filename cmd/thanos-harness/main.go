package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/thanos-workspace/test-harness/pkg/build"
	"github.com/thanos-workspace/test-harness/pkg/datagen"
	"github.com/thanos-workspace/test-harness/pkg/harness"
	"github.com/thanos-workspace/test-harness/pkg/process"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "build":
		cmdBuild(os.Args[2:])
	case "start":
		cmdStart(os.Args[2:])
	case "query":
		cmdQuery(os.Args[2:])
	case "seed":
		cmdSeed(os.Args[2:])
	case "backfill":
		cmdBackfill(os.Args[2:])
	case "stats":
		cmdStats(os.Args[2:])
	case "info":
		cmdInfo(os.Args[2:])
	case "help", "-h", "--help":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println(`thanos-harness - Local Thanos test harness

Usage:
  thanos-harness <command> [options]

Commands:
  build     Build Thanos and Prometheus from local source directories
  start     Start the Thanos pipeline (Prometheus + Sidecar + Querier)
  query     Execute a PromQL query and show resource metrics
  seed      Generate and inject test data via remote write
  backfill  Create TSDB blocks with historical data for Store Gateway
  stats     Show current resource stats for all components
  info      Show harness configuration and state
  help      Show this help message

Examples:
  # Build from local source (auto-detects workspace layout)
  thanos-harness build

  # Build with explicit paths
  thanos-harness build --thanos=./thanos --prometheus=./thanos-prometheus

  # Build with debug symbols for delve
  thanos-harness build --debug

  # Start the pipeline with built binaries
  thanos-harness start

  # Start with explicit binary paths
  thanos-harness start --prometheus=/path/to/prometheus --thanos=/path/to/thanos

  # Start with per-component memory limit
  thanos-harness start --memory-limit=1G

  # Tune Go GC (GOGC=50 means collect at 50% heap growth instead of 100%)
  thanos-harness start --gogc=50 --gomemlimit=512M

  # Enable Store Gateway with object store config
  thanos-harness start --objstore-config=/path/to/objstore.yaml

  # Generate test data
  thanos-harness seed --series=10000 --duration=1h

  # Run a query with resource tracking
  thanos-harness query 'count(test_metric)'`)
}

func cmdBuild(args []string) {
	fs := flag.NewFlagSet("build", flag.ExitOnError)
	thanosDir := fs.String("thanos", "", "Path to Thanos source directory")
	promDir := fs.String("prometheus", "", "Path to Prometheus source directory")
	outputDir := fs.String("output", "", "Output directory for binaries (default: ./bin)")
	verbose := fs.Bool("verbose", false, "Show build output")
	debug := fs.Bool("debug", false, "Build with debug symbols (for delve)")
	race := fs.Bool("race", false, "Build with race detector")
	fs.Parse(args)

	// Track if user explicitly set empty values
	thanosExplicitlyEmpty := false
	promExplicitlyEmpty := false
	for _, arg := range args {
		if arg == "--thanos=" {
			thanosExplicitlyEmpty = true
		}
		if arg == "--prometheus=" {
			promExplicitlyEmpty = true
		}
	}

	// Auto-detect workspace if paths not specified (and not explicitly empty)
	if (*thanosDir == "" && !thanosExplicitlyEmpty) || (*promDir == "" && !promExplicitlyEmpty) {
		// Try to find workspace root
		wd, _ := os.Getwd()
		workspaceRoot := findWorkspaceRoot(wd)
		if workspaceRoot != "" {
			defaultThanos, defaultProm := build.DefaultPaths(workspaceRoot)
			if *thanosDir == "" && !thanosExplicitlyEmpty {
				*thanosDir = defaultThanos
			}
			if *promDir == "" && !promExplicitlyEmpty {
				*promDir = defaultProm
			}
		}
	}

	if *thanosDir == "" && *promDir == "" {
		fmt.Fprintln(os.Stderr, "Error: specify --thanos and/or --prometheus source directories")
		fmt.Fprintln(os.Stderr, "       or run from a workspace containing thanos/ and thanos-prometheus/")
		os.Exit(1)
	}

	if *outputDir == "" {
		wd, _ := os.Getwd()
		workspaceRoot := findWorkspaceRoot(wd)
		if workspaceRoot != "" {
			*outputDir = filepath.Join(workspaceRoot, "bin")
		} else {
			*outputDir = "./bin"
		}
	}

	cfg := build.Config{
		ThanosDir:     *thanosDir,
		PrometheusDir: *promDir,
		OutputDir:     *outputDir,
		Verbose:       *verbose,
		Debug:         *debug,
		Race:          *race,
	}

	fmt.Printf("Building binaries...\n")
	if cfg.ThanosDir != "" {
		fmt.Printf("  Thanos source:     %s\n", cfg.ThanosDir)
	}
	if cfg.PrometheusDir != "" {
		fmt.Printf("  Prometheus source: %s\n", cfg.PrometheusDir)
	}
	fmt.Printf("  Output directory:  %s\n", cfg.OutputDir)
	if cfg.Debug {
		fmt.Printf("  Debug symbols:     enabled\n")
	}
	fmt.Println()

	ctx := context.Background()
	result, err := build.Build(ctx, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Build failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("Build complete:")
	if result.ThanosBinary != "" {
		version, _ := build.GetVersion(result.ThanosBinary)
		fmt.Printf("  Thanos:     %s (%v)\n", result.ThanosBinary, result.ThanosDuration.Round(time.Second))
		if version != "" {
			fmt.Printf("              %s\n", version)
		}
	}
	if result.PrometheusBinary != "" {
		version, _ := build.GetVersion(result.PrometheusBinary)
		fmt.Printf("  Prometheus: %s (%v)\n", result.PrometheusBinary, result.PrometheusDuration.Round(time.Second))
		if version != "" {
			fmt.Printf("              %s\n", version)
		}
	}

	fmt.Printf("\nTo start the pipeline:\n")
	fmt.Printf("  thanos-harness start --thanos=%s --prometheus=%s\n",
		result.ThanosBinary, result.PrometheusBinary)
}

// findWorkspaceRoot looks for a directory containing thanos/ and thanos-prometheus/
func findWorkspaceRoot(start string) string {
	dir := start
	for i := 0; i < 5; i++ { // Look up to 5 levels
		thanosDir := filepath.Join(dir, "thanos")
		promDir := filepath.Join(dir, "thanos-prometheus")
		if dirExists(thanosDir) || dirExists(promDir) {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func cmdStart(args []string) {
	fs := flag.NewFlagSet("start", flag.ExitOnError)
	baseDir := fs.String("dir", "/tmp/thanos-harness", "Base directory for data")
	promBinary := fs.String("prometheus", "", "Path to prometheus binary (auto-detects from workspace/bin)")
	thanosBinary := fs.String("thanos", "", "Path to thanos binary (auto-detects from workspace/bin)")
	basePort := fs.Int("port", 19090, "Starting port number")
	memoryLimit := fs.String("memory-limit", "", "Per-component memory limit (e.g., 512M, 1G, 2G)")
	gogc := fs.Int("gogc", 0, "GOGC value for Go runtime (0 = default 100)")
	gomemlimit := fs.String("gomemlimit", "", "GOMEMLIMIT for Go runtime (e.g., 512M, 1G)")
	objstoreConfig := fs.String("objstore-config", "", "Path to object store config file (enables Store Gateway)")
	objstoreCACert := fs.String("objstore-ca-cert", "", "Path to CA cert for object store TLS (copied to local dir)")
	objstorePrefix := fs.String("objstore-prefix", "", "Override storage prefix/path in object store config")
	tsdbBlockDuration := fs.String("tsdb-block-duration", "", "TSDB block duration (default 2h, use shorter for testing)")
	distributedMode := fs.Bool("distributed", false, "Run in distributed mode with leaf and root queriers")
	numInstances := fs.Int("instances", 1, "Number of Prometheus+Sidecar instances (requires --distributed)")
	fs.Parse(args)

	// Auto-detect binaries if not specified
	if *promBinary == "" || *thanosBinary == "" {
		wd, _ := os.Getwd()
		workspaceRoot := findWorkspaceRoot(wd)
		if workspaceRoot != "" {
			binDir := filepath.Join(workspaceRoot, "bin")
			if *promBinary == "" {
				// Prefer thanos-prometheus/prometheus, then bin/prometheus, then PATH
				candidates := []string{
					filepath.Join(workspaceRoot, "thanos-prometheus", "prometheus"),
					filepath.Join(binDir, "prometheus"),
				}
				*promBinary = "prometheus" // Default to PATH
				for _, candidate := range candidates {
					if _, err := os.Stat(candidate); err == nil {
						*promBinary = candidate
						break
					}
				}
			}
			if *thanosBinary == "" {
				// Prefer thanos/thanos, then bin/thanos, then PATH
				candidates := []string{
					filepath.Join(workspaceRoot, "thanos", "thanos"),
					filepath.Join(binDir, "thanos"),
				}
				*thanosBinary = "thanos" // Default to PATH
				for _, candidate := range candidates {
					if _, err := os.Stat(candidate); err == nil {
						*thanosBinary = candidate
						break
					}
				}
			}
		} else {
			if *promBinary == "" {
				*promBinary = "prometheus"
			}
			if *thanosBinary == "" {
				*thanosBinary = "thanos"
			}
		}
	}

	var memLimitBytes int64
	if *memoryLimit != "" {
		var err error
		memLimitBytes, err = parseMemorySize(*memoryLimit)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Invalid memory limit: %v\n", err)
			os.Exit(1)
		}
	}

	var goMemLimitBytes int64
	if *gomemlimit != "" {
		var err error
		goMemLimitBytes, err = parseMemorySize(*gomemlimit)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Invalid GOMEMLIMIT: %v\n", err)
			os.Exit(1)
		}
	}

	cfg := harness.Config{
		BaseDir:          *baseDir,
		PrometheusBinary: *promBinary,
		ThanosBinary:     *thanosBinary,
		BasePort:         *basePort,
		MemoryLimit:      memLimitBytes,
		GOGC:             *gogc,
		GOMEMLIMIT:       goMemLimitBytes,
		ObjStoreConfig:    *objstoreConfig,
		ObjStoreCACert:    *objstoreCACert,
		ObjStorePrefix:    *objstorePrefix,
		TSDBBlockDuration: *tsdbBlockDuration,
		DistributedMode:   *distributedMode,
		NumInstances:      *numInstances,
	}

	h, err := harness.New(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create harness: %v\n", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fmt.Println("Starting Thanos pipeline...")
	if err := h.Start(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to start: %v\n", err)
		os.Exit(1)
	}

	info := h.Info()
	fmt.Printf("\nPipeline running:\n")
	if h.NumInstances() == 1 {
		fmt.Printf("  Prometheus: %s\n", h.PrometheusURL())
	} else {
		for _, inst := range h.Instances() {
			fmt.Printf("  Prometheus (%s): http://localhost:%d\n", inst.Name, inst.PrometheusHTTPPort)
		}
	}
	if h.DistributedMode() {
		fmt.Printf("  Querier (leaf): %s\n", h.LeafQuerierURL())
		fmt.Printf("  Querier (root): %s\n", h.RootQuerierURL())
	} else {
		fmt.Printf("  Querier:    %s\n", h.QuerierURL())
	}
	if h.StoreEnabled() {
		fmt.Printf("  Store:      %s\n", h.StoreURL())
	}
	fmt.Printf("  Data dir:   %s\n", cfg.BaseDir)
	fmt.Printf("  Cgroup v%d:  %v\n", h.Manager().CgroupVersion(), info["scopes_enabled"])
	if cfg.MemoryLimit > 0 {
		fmt.Printf("  Memory limit: %s per component\n", process.FormatBytes(cfg.MemoryLimit))
	}
	if cfg.GOGC > 0 {
		fmt.Printf("  GOGC: %d\n", cfg.GOGC)
	}
	if cfg.GOMEMLIMIT > 0 {
		fmt.Printf("  GOMEMLIMIT: %s\n", process.FormatBytes(cfg.GOMEMLIMIT))
	}
	fmt.Printf("\nPress Ctrl+C to stop...\n")

	// Wait for interrupt
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	fmt.Println("\nStopping...")
	if err := h.Stop(); err != nil {
		fmt.Fprintf(os.Stderr, "Error stopping: %v\n", err)
	}
	fmt.Println("Done.")
}

func cmdQuery(args []string) {
	fs := flag.NewFlagSet("query", flag.ExitOnError)
	baseDir := fs.String("dir", "/tmp/thanos-harness", "Base directory")
	basePort := fs.Int("port", 19090, "Base port")
	outputJSON := fs.Bool("json", false, "Output as JSON")
	fs.Parse(args)

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "Usage: thanos-harness query [options] <promql>")
		os.Exit(1)
	}
	query := fs.Arg(0)

	cfg := harness.Config{
		BaseDir:  *baseDir,
		BasePort: *basePort,
	}

	h, _ := harness.New(cfg)
	ctx := context.Background()

	result, err := h.Query(ctx, query)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Query failed: %v\n", err)
		os.Exit(1)
	}

	if *outputJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(result)
	} else {
		fmt.Printf("Query: %s\n", result.Query)
		fmt.Printf("Status: %s\n", result.Status)
		fmt.Printf("Duration: %v\n", result.Duration)
		fmt.Printf("Results: %d series\n", result.ResultCount)
		if result.Error != "" {
			fmt.Printf("Error: %s\n", result.Error)
		}
		fmt.Println("\nResource Usage:")
		for name, stats := range result.Stats {
			fmt.Printf("  %s:\n", name)
			fmt.Printf("    Memory: %s (peak: %s)\n",
				process.FormatBytes(stats.MemoryUsage),
				process.FormatBytes(stats.MemoryMax))
			fmt.Printf("    CPU: %dms\n", stats.CPUUsageUsec/1000)
		}
	}
}

func cmdSeed(args []string) {
	fs := flag.NewFlagSet("seed", flag.ExitOnError)
	basePort := fs.Int("port", 19090, "Base port (Prometheus port)")
	numInstances := fs.Int("instances", 1, "Number of instances to seed (uses port, port+10, port+20, ...)")
	numSeries := fs.Int("series", 1000, "Number of unique series per instance")
	duration := fs.Duration("duration", 1*time.Hour, "Duration of data to generate")
	metricName := fs.String("metric", "test_metric", "Base metric name")
	withInfo := fs.Bool("info", false, "Generate info metrics for join testing")
	fs.Parse(args)

	// Build list of Prometheus URLs based on instance count
	var prometheusURLs []string
	for i := 0; i < *numInstances; i++ {
		port := *basePort + i*10
		prometheusURLs = append(prometheusURLs, fmt.Sprintf("http://localhost:%d", port))
	}

	ctx := context.Background()

	for i, promURL := range prometheusURLs {
		cfg := datagen.Config{
			NumSeries:        *numSeries,
			MetricName:       *metricName,
			LabelNames:       []string{"instance", "job", "env"},
			LabelCardinality: []int{*numSeries / 10, 10, 3}, // Rough distribution
			SampleInterval:   15 * time.Second,
			Duration:         *duration,
		}

		if *withInfo {
			cfg.InfoMetrics = []datagen.InfoMetricConfig{
				{
					Name:       "instance_info",
					JoinLabel:  "instance",
					InfoLabels: map[string]string{"node": "node", "region": "us-east"},
				},
			}
		}

		gen := datagen.NewGenerator(cfg)

		if *numInstances > 1 {
			fmt.Printf("Seeding instance %d (%s): %d series over %v...\n", i, promURL, gen.SeriesCount(), *duration)
		} else {
			fmt.Printf("Generating %d series over %v...\n", gen.SeriesCount(), *duration)
		}

		if err := gen.GenerateRemoteWrite(ctx, promURL); err != nil {
			fmt.Fprintf(os.Stderr, "Seed failed for %s: %v\n", promURL, err)
			os.Exit(1)
		}
	}

	fmt.Println("Done.")
}

func cmdStats(args []string) {
	fs := flag.NewFlagSet("stats", flag.ExitOnError)
	baseDir := fs.String("dir", "/tmp/thanos-harness", "Base directory")
	basePort := fs.Int("port", 19090, "Base port")
	outputJSON := fs.Bool("json", false, "Output as JSON")
	fs.Parse(args)

	cfg := harness.Config{
		BaseDir:  *baseDir,
		BasePort: *basePort,
	}

	h, _ := harness.New(cfg)
	stats := h.GetStats()

	if *outputJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(stats)
	} else {
		fmt.Println("Component Resource Stats:")
		for name, s := range stats {
			fmt.Printf("\n%s:\n", name)
			fmt.Printf("  Memory Usage: %s\n", process.FormatBytes(s.MemoryUsage))
			fmt.Printf("  Memory Peak:  %s\n", process.FormatBytes(s.MemoryMax))
			fmt.Printf("  Memory RSS:   %s\n", process.FormatBytes(s.MemoryRSS))
			fmt.Printf("  Memory Cache: %s\n", process.FormatBytes(s.MemoryCache))
			fmt.Printf("  CPU Total:    %dms\n", s.CPUUsageUsec/1000)
			fmt.Printf("  CPU User:     %dms\n", s.CPUUserUsec/1000)
			fmt.Printf("  CPU System:   %dms\n", s.CPUSysUsec/1000)
		}
	}
}

func cmdInfo(args []string) {
	fs := flag.NewFlagSet("info", flag.ExitOnError)
	baseDir := fs.String("dir", "/tmp/thanos-harness", "Base directory")
	basePort := fs.Int("port", 19090, "Base port")
	fs.Parse(args)

	cfg := harness.Config{
		BaseDir:  *baseDir,
		BasePort: *basePort,
	}

	h, _ := harness.New(cfg)
	info := h.Info()

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(info)
}

func cmdBackfill(args []string) {
	fs := flag.NewFlagSet("backfill", flag.ExitOnError)
	bucketDir := fs.String("bucket", "/tmp/thanos-bucket", "Object store bucket directory")
	numSeries := fs.Int("series", 1000, "Number of unique series per instance")
	numInstances := fs.Int("instances", 1, "Number of instances to create data for (uses cluster-0, cluster-1, ...)")
	duration := fs.Duration("duration", 24*time.Hour, "Duration of historical data")
	endTime := fs.String("end", "", "End time (RFC3339, default: now)")
	metricName := fs.String("metric", "test_metric", "Base metric name")
	promtoolPath := fs.String("promtool", "", "Path to promtool binary (auto-detects)")
	labels := fs.String("labels", "", "Extra labels as key=value,key=value")
	churnRate := fs.Float64("churn-rate", 0, "Fraction of churnable series to replace per churn-interval (0-1, e.g., 0.1 = 10%)")
	churnInterval := fs.Duration("churn-interval", 5*time.Minute, "How often to apply churn (series turnover happens at this interval)")
	churnFraction := fs.Float64("churn-fraction", 0.5, "Fraction of series that can churn (0-1), rest are stable")
	fs.Parse(args)

	// Auto-detect promtool
	if *promtoolPath == "" {
		wd, _ := os.Getwd()
		workspaceRoot := findWorkspaceRoot(wd)
		if workspaceRoot != "" {
			candidates := []string{
				filepath.Join(workspaceRoot, "thanos-prometheus", "promtool"),
				filepath.Join(workspaceRoot, "bin", "promtool"),
			}
			for _, c := range candidates {
				if _, err := os.Stat(c); err == nil {
					*promtoolPath = c
					break
				}
			}
		}
		if *promtoolPath == "" {
			*promtoolPath = "promtool" // Try PATH
		}
	}

	// Parse end time
	var end time.Time
	if *endTime == "" {
		end = time.Now()
	} else {
		var err error
		end, err = time.Parse(time.RFC3339, *endTime)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Invalid end time: %v\n", err)
			os.Exit(1)
		}
	}
	start := end.Add(-*duration)

	// Parse extra labels
	extraLabels := make(map[string]string)
	if *labels != "" {
		for _, pair := range strings.Split(*labels, ",") {
			parts := strings.SplitN(pair, "=", 2)
			if len(parts) == 2 {
				extraLabels[parts[0]] = parts[1]
			}
		}
	}

	// Ensure bucket directory exists
	if err := os.MkdirAll(*bucketDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create bucket dir: %v\n", err)
		os.Exit(1)
	}

	// Generate data for each instance
	totalBlocks := 0
	for inst := 0; inst < *numInstances; inst++ {
		instanceName := fmt.Sprintf("cluster-%d", inst)
		if *numInstances > 1 {
			fmt.Printf("\n=== Instance %s ===\n", instanceName)
		}

		// Build instance-specific labels
		instanceLabels := make(map[string]string)
		for k, v := range extraLabels {
			instanceLabels[k] = v
		}
		if *numInstances > 1 {
			instanceLabels["cluster"] = instanceName
		}
		instanceLabels["prometheus"] = fmt.Sprintf("prometheus-%s", instanceName)
		instanceLabels["replica"] = "0"

		blocks, err := generateBackfillBlocks(*promtoolPath, *bucketDir, *numSeries, start, end, *metricName, instanceLabels, *churnRate, *churnInterval, *churnFraction)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to generate blocks for %s: %v\n", instanceName, err)
			os.Exit(1)
		}
		totalBlocks += blocks
	}

	fmt.Printf("\nDone. Created %d total blocks in %s\n", totalBlocks, *bucketDir)
	fmt.Println("Store Gateway will sync these blocks on next refresh (default 3m)")
}

// generateBackfillBlocks generates TSDB blocks with promtool and injects Thanos metadata
func generateBackfillBlocks(promtoolPath, bucketDir string, numSeries int, start, end time.Time, metricName string, thanosLabels map[string]string, churnRate float64, churnInterval time.Duration, churnFraction float64) (int, error) {
	duration := end.Sub(start)

	// Configure generator
	cfg := datagen.Config{
		NumSeries:        numSeries,
		MetricName:       metricName,
		LabelNames:       []string{"instance", "job", "env"},
		LabelCardinality: []int{numSeries / 10, 10, 3},
		SampleInterval:   60 * time.Second,
		Duration:         duration,
		ChurnRate:        churnRate,
		ChurnInterval:    churnInterval,
		ChurnFraction:    churnFraction,
	}

	gen := datagen.NewGenerator(cfg)

	fmt.Printf("Generating %d series from %s to %s...\n",
		gen.SeriesCount(), start.Format(time.RFC3339), end.Format(time.RFC3339))

	// Generate OpenMetrics data
	data, err := gen.GenerateOpenMetrics(start, end)
	if err != nil {
		return 0, fmt.Errorf("generate data: %w", err)
	}

	// Write to temp file
	tmpFile, err := os.CreateTemp("", "backfill-*.txt")
	if err != nil {
		return 0, fmt.Errorf("create temp file: %w", err)
	}
	defer os.Remove(tmpFile.Name())

	if _, err := tmpFile.WriteString(data); err != nil {
		return 0, fmt.Errorf("write data: %w", err)
	}
	tmpFile.Close()

	// Count existing blocks before running promtool
	existingBlocks := make(map[string]bool)
	entries, _ := os.ReadDir(bucketDir)
	for _, e := range entries {
		if e.IsDir() && len(e.Name()) == 26 {
			existingBlocks[e.Name()] = true
		}
	}

	// Run promtool to create blocks
	fmt.Printf("Creating TSDB blocks with promtool...\n")
	cmd := exec.Command(promtoolPath, "tsdb", "create-blocks-from", "openmetrics",
		tmpFile.Name(), bucketDir)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return 0, fmt.Errorf("promtool failed: %w", err)
	}

	// Find new blocks and inject Thanos metadata
	entries, _ = os.ReadDir(bucketDir)
	newBlockCount := 0
	for _, e := range entries {
		if e.IsDir() && len(e.Name()) == 26 && !existingBlocks[e.Name()] {
			newBlockCount++
			metaPath := filepath.Join(bucketDir, e.Name(), "meta.json")
			if err := injectThanosMetadata(metaPath, thanosLabels); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to inject Thanos metadata for %s: %v\n", e.Name(), err)
			}
		}
	}

	return newBlockCount, nil
}

// injectThanosMetadata adds the required Thanos section to a block's meta.json
func injectThanosMetadata(metaPath string, labels map[string]string) error {
	data, err := os.ReadFile(metaPath)
	if err != nil {
		return err
	}

	var meta map[string]interface{}
	if err := json.Unmarshal(data, &meta); err != nil {
		return err
	}

	// Add Thanos section
	meta["thanos"] = map[string]interface{}{
		"labels":     labels,
		"downsample": map[string]interface{}{"resolution": 0},
		"source":     "backfill",
	}

	// Write back with proper formatting
	output, err := json.MarshalIndent(meta, "", "\t")
	if err != nil {
		return err
	}

	return os.WriteFile(metaPath, output, 0644)
}

func parseMemorySize(s string) (int64, error) {
	if s == "" {
		return 0, nil
	}
	s = strings.ToUpper(strings.TrimSpace(s))
	var multiplier int64 = 1
	if strings.HasSuffix(s, "G") || strings.HasSuffix(s, "GB") {
		multiplier = 1024 * 1024 * 1024
		s = strings.TrimSuffix(strings.TrimSuffix(s, "GB"), "G")
	} else if strings.HasSuffix(s, "M") || strings.HasSuffix(s, "MB") {
		multiplier = 1024 * 1024
		s = strings.TrimSuffix(strings.TrimSuffix(s, "MB"), "M")
	} else if strings.HasSuffix(s, "K") || strings.HasSuffix(s, "KB") {
		multiplier = 1024
		s = strings.TrimSuffix(strings.TrimSuffix(s, "KB"), "K")
	}
	var value int64
	if _, err := fmt.Sscanf(s, "%d", &value); err != nil {
		return 0, fmt.Errorf("invalid size format: %s", s)
	}
	return value * multiplier, nil
}
