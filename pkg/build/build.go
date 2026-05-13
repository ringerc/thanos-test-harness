package build

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Config holds build configuration.
type Config struct {
	ThanosDir     string // Path to Thanos source directory
	PrometheusDir string // Path to Prometheus source directory
	OutputDir     string // Where to put built binaries
	Verbose       bool   // Print build output
	Race          bool   // Build with race detector
	Debug         bool   // Build with debug symbols (disable optimizations)
}

// Result holds build results.
type Result struct {
	ThanosBinary     string
	PrometheusBinary string
	ThanosDuration   time.Duration
	PrometheusDuration time.Duration
}

// Build compiles Thanos and Prometheus from source.
func Build(ctx context.Context, cfg Config) (*Result, error) {
	if err := os.MkdirAll(cfg.OutputDir, 0755); err != nil {
		return nil, fmt.Errorf("create output dir: %w", err)
	}

	result := &Result{}

	// Build Thanos
	if cfg.ThanosDir != "" {
		start := time.Now()
		binary, err := buildBinary(ctx, cfg.ThanosDir, "thanos", "./cmd/thanos", cfg)
		if err != nil {
			return nil, fmt.Errorf("build thanos: %w", err)
		}
		result.ThanosBinary = binary
		result.ThanosDuration = time.Since(start)
	}

	// Build Prometheus
	if cfg.PrometheusDir != "" {
		start := time.Now()
		binary, err := buildBinary(ctx, cfg.PrometheusDir, "prometheus", "./cmd/prometheus", cfg)
		if err != nil {
			return nil, fmt.Errorf("build prometheus: %w", err)
		}
		result.PrometheusBinary = binary
		result.PrometheusDuration = time.Since(start)
	}

	return result, nil
}

func buildBinary(ctx context.Context, srcDir, name, pkg string, cfg Config) (string, error) {
	// Verify source directory exists
	if _, err := os.Stat(srcDir); os.IsNotExist(err) {
		return "", fmt.Errorf("source directory not found: %s", srcDir)
	}

	// Check for go.mod
	if _, err := os.Stat(filepath.Join(srcDir, "go.mod")); os.IsNotExist(err) {
		return "", fmt.Errorf("not a Go module (no go.mod): %s", srcDir)
	}

	outputPath := filepath.Join(cfg.OutputDir, name)
	if runtime.GOOS == "windows" {
		outputPath += ".exe"
	}

	// Build command
	args := []string{"build", "-o", outputPath}

	// Build flags
	var ldflags []string
	var gcflags []string
	var tags []string

	// Thanos requires slicelabels tag for correct unsafe pointer handling in labelpb
	if name == "thanos" {
		tags = append(tags, "slicelabels")
	}

	if cfg.Debug {
		gcflags = append(gcflags, "all=-N -l")
	}

	if len(tags) > 0 {
		args = append(args, "-tags", strings.Join(tags, ","))
	}
	if len(ldflags) > 0 {
		args = append(args, "-ldflags", strings.Join(ldflags, " "))
	}
	if len(gcflags) > 0 {
		args = append(args, "-gcflags", strings.Join(gcflags, " "))
	}
	if cfg.Race {
		args = append(args, "-race")
	}

	args = append(args, pkg)

	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = srcDir
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")

	if cfg.Verbose {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		fmt.Printf("Building %s: go %s\n", name, strings.Join(args, " "))
	} else {
		// Capture output for error reporting
		output, err := cmd.CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("build failed:\n%s", output)
		}
		return outputPath, nil
	}

	if err := cmd.Run(); err != nil {
		return "", err
	}

	return outputPath, nil
}

// GetVersion runs the binary with --version and returns the output.
func GetVersion(binaryPath string) (string, error) {
	cmd := exec.Command(binaryPath, "--version")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", err
	}
	// Return first line only
	lines := strings.Split(string(output), "\n")
	if len(lines) > 0 {
		return strings.TrimSpace(lines[0]), nil
	}
	return "", nil
}

// DefaultPaths returns default source paths based on workspace layout.
func DefaultPaths(workspaceDir string) (thanosDir, prometheusDir string) {
	thanosDir = filepath.Join(workspaceDir, "thanos")
	prometheusDir = filepath.Join(workspaceDir, "thanos-prometheus")
	return
}
