package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/thanos-workspace/test-harness/pkg/components"
	"github.com/thanos-workspace/test-harness/pkg/process"
)

// Config holds harness configuration.
type Config struct {
	BaseDir          string
	PrometheusBinary string
	ThanosBinary     string
	BasePort         int // Starting port number (components use BasePort, BasePort+1, etc.)
}

// DefaultConfig returns sensible defaults.
func DefaultConfig() Config {
	return Config{
		BaseDir:          "/tmp/thanos-harness",
		PrometheusBinary: "prometheus",
		ThanosBinary:     "thanos",
		BasePort:         19090,
	}
}

// Harness orchestrates a local Thanos pipeline.
type Harness struct {
	config  Config
	manager *process.Manager

	// Port assignments
	prometheusHTTPPort int
	sidecarHTTPPort    int
	sidecarGRPCPort    int
	querierHTTPPort    int
	querierGRPCPort    int
}

// New creates a new harness with the given configuration.
func New(cfg Config) (*Harness, error) {
	if err := os.MkdirAll(cfg.BaseDir, 0755); err != nil {
		return nil, fmt.Errorf("create base dir: %w", err)
	}

	h := &Harness{
		config:  cfg,
		manager: process.NewManager(cfg.BaseDir),

		prometheusHTTPPort: cfg.BasePort,
		sidecarHTTPPort:    cfg.BasePort + 1,
		sidecarGRPCPort:    cfg.BasePort + 2,
		querierHTTPPort:    cfg.BasePort + 3,
		querierGRPCPort:    cfg.BasePort + 4,
	}

	return h, nil
}

// Start starts all components in the pipeline.
func (h *Harness) Start(ctx context.Context) error {
	// Start Prometheus
	promCfg := components.DefaultPrometheusConfig(
		"prometheus",
		filepath.Join(h.config.BaseDir, "prometheus"),
		h.prometheusHTTPPort,
	)
	promProcessCfg, err := promCfg.ToProcessConfig(h.config.PrometheusBinary)
	if err != nil {
		return fmt.Errorf("create prometheus config: %w", err)
	}
	if _, err := h.manager.Start(ctx, promProcessCfg); err != nil {
		return fmt.Errorf("start prometheus: %w", err)
	}

	// Wait for Prometheus to be ready
	if err := h.manager.WaitReady(ctx, "prometheus", 30*time.Second); err != nil {
		return fmt.Errorf("prometheus not ready: %w", err)
	}

	// Start Sidecar
	sidecarCfg := components.DefaultSidecarConfig(
		"sidecar",
		filepath.Join(h.config.BaseDir, "sidecar"),
		h.sidecarHTTPPort,
		h.sidecarGRPCPort,
		fmt.Sprintf("http://localhost:%d", h.prometheusHTTPPort),
		filepath.Join(h.config.BaseDir, "prometheus", "tsdb"),
	)
	sidecarProcessCfg, err := sidecarCfg.ToProcessConfig(h.config.ThanosBinary)
	if err != nil {
		return fmt.Errorf("create sidecar config: %w", err)
	}
	if _, err := h.manager.Start(ctx, sidecarProcessCfg); err != nil {
		return fmt.Errorf("start sidecar: %w", err)
	}

	// Wait for Sidecar
	if err := h.manager.WaitReady(ctx, "sidecar", 30*time.Second); err != nil {
		return fmt.Errorf("sidecar not ready: %w", err)
	}

	// Start Querier
	querierCfg := components.DefaultQuerierConfig(
		"querier",
		filepath.Join(h.config.BaseDir, "querier"),
		h.querierHTTPPort,
		h.querierGRPCPort,
		[]string{fmt.Sprintf("localhost:%d", h.sidecarGRPCPort)},
	)
	querierProcessCfg, err := querierCfg.ToProcessConfig(h.config.ThanosBinary)
	if err != nil {
		return fmt.Errorf("create querier config: %w", err)
	}
	if _, err := h.manager.Start(ctx, querierProcessCfg); err != nil {
		return fmt.Errorf("start querier: %w", err)
	}

	// Wait for Querier
	if err := h.manager.WaitReady(ctx, "querier", 30*time.Second); err != nil {
		return fmt.Errorf("querier not ready: %w", err)
	}

	return nil
}

// Stop stops all components.
func (h *Harness) Stop() error {
	return h.manager.StopAll()
}

// QueryResult holds query results and resource metrics.
type QueryResult struct {
	Query       string                       `json:"query"`
	StartTime   time.Time                    `json:"start_time"`
	EndTime     time.Time                    `json:"end_time"`
	Duration    time.Duration                `json:"duration"`
	Status      string                       `json:"status"`
	Error       string                       `json:"error,omitempty"`
	ResultCount int                          `json:"result_count"`
	Stats       map[string]*process.CgroupStats `json:"component_stats"`
}

// Query executes a PromQL query and returns results with resource metrics.
func (h *Harness) Query(ctx context.Context, query string) (*QueryResult, error) {
	result := &QueryResult{
		Query:     query,
		StartTime: time.Now(),
	}

	// Collect pre-query stats
	preStats := h.manager.GetAllStats()

	// Execute query
	queryURL := fmt.Sprintf("http://localhost:%d/api/v1/query", h.querierHTTPPort)
	params := url.Values{}
	params.Set("query", query)

	resp, err := http.Get(queryURL + "?" + params.Encode())
	result.EndTime = time.Now()
	result.Duration = result.EndTime.Sub(result.StartTime)

	if err != nil {
		result.Status = "error"
		result.Error = err.Error()
		return result, nil
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	var apiResp struct {
		Status string `json:"status"`
		Data   struct {
			ResultType string            `json:"resultType"`
			Result     []json.RawMessage `json:"result"`
		} `json:"data"`
		Error string `json:"error"`
	}

	if err := json.Unmarshal(body, &apiResp); err != nil {
		result.Status = "error"
		result.Error = "failed to parse response: " + err.Error()
		return result, nil
	}

	result.Status = apiResp.Status
	result.Error = apiResp.Error
	result.ResultCount = len(apiResp.Data.Result)

	// Collect post-query stats and compute delta
	postStats := h.manager.GetAllStats()
	result.Stats = make(map[string]*process.CgroupStats)
	for name, post := range postStats {
		if pre, ok := preStats[name]; ok {
			// Use post stats but show delta for CPU
			delta := *post
			delta.CPUUsageUsec -= pre.CPUUsageUsec
			delta.CPUUserUsec -= pre.CPUUserUsec
			delta.CPUSysUsec -= pre.CPUSysUsec
			result.Stats[name] = &delta
		} else {
			result.Stats[name] = post
		}
	}

	return result, nil
}

// QueryRange executes a range query.
func (h *Harness) QueryRange(ctx context.Context, query string, start, end time.Time, step time.Duration) (*QueryResult, error) {
	result := &QueryResult{
		Query:     query,
		StartTime: time.Now(),
	}

	preStats := h.manager.GetAllStats()

	queryURL := fmt.Sprintf("http://localhost:%d/api/v1/query_range", h.querierHTTPPort)
	params := url.Values{}
	params.Set("query", query)
	params.Set("start", fmt.Sprintf("%d", start.Unix()))
	params.Set("end", fmt.Sprintf("%d", end.Unix()))
	params.Set("step", step.String())

	resp, err := http.Get(queryURL + "?" + params.Encode())
	result.EndTime = time.Now()
	result.Duration = result.EndTime.Sub(result.StartTime)

	if err != nil {
		result.Status = "error"
		result.Error = err.Error()
		return result, nil
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	var apiResp struct {
		Status string `json:"status"`
		Data   struct {
			ResultType string            `json:"resultType"`
			Result     []json.RawMessage `json:"result"`
		} `json:"data"`
		Error string `json:"error"`
	}

	if err := json.Unmarshal(body, &apiResp); err != nil {
		result.Status = "error"
		result.Error = "failed to parse response: " + err.Error()
		return result, nil
	}

	result.Status = apiResp.Status
	result.Error = apiResp.Error
	result.ResultCount = len(apiResp.Data.Result)

	postStats := h.manager.GetAllStats()
	result.Stats = make(map[string]*process.CgroupStats)
	for name, post := range postStats {
		if pre, ok := preStats[name]; ok {
			delta := *post
			delta.CPUUsageUsec -= pre.CPUUsageUsec
			delta.CPUUserUsec -= pre.CPUUserUsec
			delta.CPUSysUsec -= pre.CPUSysUsec
			result.Stats[name] = &delta
		} else {
			result.Stats[name] = post
		}
	}

	return result, nil
}

// GetStats returns current resource stats for all components.
func (h *Harness) GetStats() map[string]*process.CgroupStats {
	return h.manager.GetAllStats()
}

// PrometheusURL returns the Prometheus HTTP URL.
func (h *Harness) PrometheusURL() string {
	return fmt.Sprintf("http://localhost:%d", h.prometheusHTTPPort)
}

// QuerierURL returns the Querier HTTP URL.
func (h *Harness) QuerierURL() string {
	return fmt.Sprintf("http://localhost:%d", h.querierHTTPPort)
}

// Manager returns the process manager for advanced operations.
func (h *Harness) Manager() *process.Manager {
	return h.manager
}

// Info returns information about the harness state.
func (h *Harness) Info() map[string]interface{} {
	return map[string]interface{}{
		"base_dir":        h.config.BaseDir,
		"components":      h.manager.List(),
		"cgroup_version":  h.manager.CgroupVersion(),
		"scopes_enabled":  h.manager.ScopesEnabled(),
		"prometheus_url":  h.PrometheusURL(),
		"querier_url":     h.QuerierURL(),
	}
}
