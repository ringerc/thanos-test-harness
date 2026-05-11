package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
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
  seed      Generate and inject test data
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

	cfg := harness.Config{
		BaseDir:          *baseDir,
		PrometheusBinary: *promBinary,
		ThanosBinary:     *thanosBinary,
		BasePort:         *basePort,
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
	fmt.Printf("  Prometheus: %s\n", h.PrometheusURL())
	fmt.Printf("  Querier:    %s\n", h.QuerierURL())
	fmt.Printf("  Data dir:   %s\n", cfg.BaseDir)
	fmt.Printf("  Cgroup v%d:  %v\n", h.Manager().CgroupVersion(), info["scopes_enabled"])
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
	numSeries := fs.Int("series", 1000, "Number of unique series")
	duration := fs.Duration("duration", 1*time.Hour, "Duration of data to generate")
	metricName := fs.String("metric", "test_metric", "Base metric name")
	withInfo := fs.Bool("info", false, "Generate info metrics for join testing")
	fs.Parse(args)

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

	fmt.Printf("Generating %d series over %v...\n", gen.SeriesCount(), *duration)

	ctx := context.Background()
	prometheusURL := fmt.Sprintf("http://localhost:%d", *basePort)

	if err := gen.GenerateRemoteWrite(ctx, prometheusURL); err != nil {
		fmt.Fprintf(os.Stderr, "Seed failed: %v\n", err)
		os.Exit(1)
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
