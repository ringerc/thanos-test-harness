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
	"gopkg.in/yaml.v3"
)

// Config holds harness configuration.
type Config struct {
	BaseDir          string
	PrometheusBinary string
	ThanosBinary     string
	BasePort         int    // Starting port number (components use BasePort, BasePort+1, etc.)
	MemoryLimit      int64  // Per-component memory limit in bytes (0 = unlimited)
	GOGC             int    // Go GC target percentage (0 = use default 100)
	GOMEMLIMIT       int64  // Go soft memory limit in bytes (0 = unlimited)
	ObjStoreConfig    string // Path to object store config file (enables Store Gateway if set)
	ObjStoreCACert    string // Path to CA cert file for object store TLS (copied to local)
	ObjStorePrefix    string // Override prefix/path in object store config
	TSDBBlockDuration string // TSDB block duration for Prometheus (default 2h)
	DistributedMode   bool   // Run two queriers: leaf (connects to stores) and root (connects to leaf)
	NumInstances      int    // Number of Prometheus+Sidecar instances in distributed mode (default 1)
}

// DefaultConfig returns sensible defaults.
func DefaultConfig() Config {
	return Config{
		BaseDir:          "/tmp/thanos-harness",
		PrometheusBinary: "prometheus",
		ThanosBinary:     "thanos",
		BasePort:         19090,
		MemoryLimit:      0, // Unlimited by default
	}
}

// Instance represents a single Prometheus+Sidecar instance.
type Instance struct {
	Index              int
	Name               string // e.g., "cluster-0", "cluster-1"
	PrometheusHTTPPort int
	SidecarHTTPPort    int
	SidecarGRPCPort    int
}

// Harness orchestrates a local Thanos pipeline.
type Harness struct {
	config  Config
	manager *process.Manager

	// Instances (one or more Prometheus+Sidecar pairs)
	instances []Instance

	// Shared component port assignments
	storeHTTPPort       int
	storeGRPCPort       int
	querierHTTPPort     int // Leaf querier (or single querier in non-distributed mode)
	querierGRPCPort     int
	querierRootHTTPPort int // Root querier (distributed mode only)
	querierRootGRPCPort int
}

// New creates a new harness with the given configuration.
// Automatically loads any existing state from a previous run.
func New(cfg Config) (*Harness, error) {
	if err := os.MkdirAll(cfg.BaseDir, 0755); err != nil {
		return nil, fmt.Errorf("create base dir: %w", err)
	}

	mgr := process.NewManager(cfg.BaseDir)

	// Try to load state from previous run
	if err := mgr.LoadState(); err != nil {
		// Log but don't fail - state loading is best-effort
		fmt.Fprintf(os.Stderr, "Warning: failed to load state: %v\n", err)
	}

	// Default to 1 instance
	numInstances := cfg.NumInstances
	if numInstances < 1 {
		numInstances = 1
	}

	// Create instances with port assignments
	// Each instance gets 3 ports: Prometheus HTTP, Sidecar HTTP, Sidecar gRPC
	// Port layout: BasePort + i*10 + offset
	instances := make([]Instance, numInstances)
	for i := 0; i < numInstances; i++ {
		instances[i] = Instance{
			Index:              i,
			Name:               fmt.Sprintf("cluster-%d", i),
			PrometheusHTTPPort: cfg.BasePort + i*10,
			SidecarHTTPPort:    cfg.BasePort + i*10 + 1,
			SidecarGRPCPort:    cfg.BasePort + i*10 + 2,
		}
	}

	// Shared components get ports after instances (BasePort + 100+)
	h := &Harness{
		config:    cfg,
		manager:   mgr,
		instances: instances,

		storeHTTPPort:       cfg.BasePort + 100,
		storeGRPCPort:       cfg.BasePort + 101,
		querierHTTPPort:     cfg.BasePort + 110,
		querierGRPCPort:     cfg.BasePort + 111,
		querierRootHTTPPort: cfg.BasePort + 120,
		querierRootGRPCPort: cfg.BasePort + 121,
	}

	return h, nil
}

// goRuntimeEnv returns environment variables for Go runtime tuning.
func (h *Harness) goRuntimeEnv() []string {
	var env []string
	if h.config.GOGC > 0 {
		env = append(env, fmt.Sprintf("GOGC=%d", h.config.GOGC))
	}
	if h.config.GOMEMLIMIT > 0 {
		env = append(env, fmt.Sprintf("GOMEMLIMIT=%d", h.config.GOMEMLIMIT))
	}
	return env
}

// prepareObjStoreConfig prepares the object store config for use.
// It copies the CA cert to a local path and modifies the config with
// the local CA cert path and any prefix override.
// If a CA cert is specified in the config at .config.http_config.tls_config.ca_file,
// it is read (relative to the objstore config file) and copied locally.
// The --objstore-ca-cert flag overrides any CA cert in the config.
// Returns the path to the prepared config file.
func (h *Harness) prepareObjStoreConfig() (string, error) {
	if h.config.ObjStoreConfig == "" {
		return "", nil
	}

	objstoreConfigDir := filepath.Dir(h.config.ObjStoreConfig)

	// Read original config
	data, err := os.ReadFile(h.config.ObjStoreConfig)
	if err != nil {
		return "", fmt.Errorf("read objstore config: %w", err)
	}

	// Parse YAML into generic map
	var config map[string]interface{}
	if err := yaml.Unmarshal(data, &config); err != nil {
		return "", fmt.Errorf("parse objstore config: %w", err)
	}

	// Get or create the config section
	innerConfig, ok := config["config"].(map[string]interface{})
	if !ok {
		innerConfig = make(map[string]interface{})
		config["config"] = innerConfig
	}

	// Determine CA cert source: CLI flag takes precedence, otherwise use config value
	caCertSource := h.config.ObjStoreCACert
	if caCertSource == "" {
		// Check if ca_file is already specified in the config
		if httpConfig, ok := innerConfig["http_config"].(map[string]interface{}); ok {
			if tlsConfig, ok := httpConfig["tls_config"].(map[string]interface{}); ok {
				if caFile, ok := tlsConfig["ca_file"].(string); ok && caFile != "" {
					// Resolve path relative to objstore config file
					if !filepath.IsAbs(caFile) {
						caCertSource = filepath.Join(objstoreConfigDir, caFile)
					} else {
						caCertSource = caFile
					}
				}
			}
		}
	}

	// Copy CA cert to local directory if we have one
	if caCertSource != "" {
		localCertPath := filepath.Join(h.config.BaseDir, "objstore-ca.crt")
		certData, err := os.ReadFile(caCertSource)
		if err != nil {
			return "", fmt.Errorf("read CA cert %s: %w", caCertSource, err)
		}
		if err := os.WriteFile(localCertPath, certData, 0644); err != nil {
			return "", fmt.Errorf("write local CA cert: %w", err)
		}

		// Get or create http_config section
		httpConfig, ok := innerConfig["http_config"].(map[string]interface{})
		if !ok {
			httpConfig = make(map[string]interface{})
			innerConfig["http_config"] = httpConfig
		}

		// Get or create tls_config section
		tlsConfig, ok := httpConfig["tls_config"].(map[string]interface{})
		if !ok {
			tlsConfig = make(map[string]interface{})
			httpConfig["tls_config"] = tlsConfig
		}

		// Set CA file path to local copy
		tlsConfig["ca_file"] = localCertPath
	}

	if h.config.ObjStorePrefix != "" {
		innerConfig["prefix"] = h.config.ObjStorePrefix
	}

	// Write modified config
	modifiedData, err := yaml.Marshal(config)
	if err != nil {
		return "", fmt.Errorf("marshal modified config: %w", err)
	}

	localConfigPath := filepath.Join(h.config.BaseDir, "objstore-config.yaml")
	if err := os.WriteFile(localConfigPath, modifiedData, 0644); err != nil {
		return "", fmt.Errorf("write local config: %w", err)
	}

	return localConfigPath, nil
}

// Start starts all components in the pipeline.
func (h *Harness) Start(ctx context.Context) error {
	goEnv := h.goRuntimeEnv()

	// Build store endpoints list from all instances
	var storeEndpoints []string

	// Start Prometheus and Sidecar for each instance
	for _, inst := range h.instances {
		// Determine component names based on number of instances
		promName := "prometheus"
		sidecarName := "sidecar"
		if len(h.instances) > 1 {
			promName = fmt.Sprintf("prometheus-%s", inst.Name)
			sidecarName = fmt.Sprintf("sidecar-%s", inst.Name)
		}

		// Start Prometheus with instance-specific external labels
		promCfg := components.DefaultPrometheusConfig(
			promName,
			filepath.Join(h.config.BaseDir, promName),
			inst.PrometheusHTTPPort,
		)
		promCfg.TSDBBlockDuration = h.config.TSDBBlockDuration
		if len(h.instances) > 1 {
			promCfg.ExternalLabels["cluster"] = inst.Name
		}
		promProcessCfg, err := promCfg.ToProcessConfig(h.config.PrometheusBinary)
		if err != nil {
			return fmt.Errorf("create prometheus config for %s: %w", inst.Name, err)
		}
		promProcessCfg.MemoryLimit = h.config.MemoryLimit
		promProcessCfg.Env = append(promProcessCfg.Env, goEnv...)
		if _, err := h.manager.Start(ctx, promProcessCfg); err != nil {
			return fmt.Errorf("start prometheus for %s: %w", inst.Name, err)
		}

		// Wait for Prometheus to be ready
		if err := h.manager.WaitReady(ctx, promName, 30*time.Second); err != nil {
			return fmt.Errorf("prometheus %s not ready: %w", inst.Name, err)
		}

		// Start Sidecar
		sidecarCfg := components.DefaultSidecarConfig(
			sidecarName,
			filepath.Join(h.config.BaseDir, sidecarName),
			inst.SidecarHTTPPort,
			inst.SidecarGRPCPort,
			fmt.Sprintf("http://localhost:%d", inst.PrometheusHTTPPort),
			filepath.Join(h.config.BaseDir, promName, "tsdb"),
		)
		sidecarProcessCfg, err := sidecarCfg.ToProcessConfig(h.config.ThanosBinary)
		if err != nil {
			return fmt.Errorf("create sidecar config for %s: %w", inst.Name, err)
		}
		sidecarProcessCfg.MemoryLimit = h.config.MemoryLimit
		sidecarProcessCfg.Env = append(sidecarProcessCfg.Env, goEnv...)
		if _, err := h.manager.Start(ctx, sidecarProcessCfg); err != nil {
			return fmt.Errorf("start sidecar for %s: %w", inst.Name, err)
		}

		// Wait for Sidecar
		if err := h.manager.WaitReady(ctx, sidecarName, 30*time.Second); err != nil {
			return fmt.Errorf("sidecar %s not ready: %w", inst.Name, err)
		}

		storeEndpoints = append(storeEndpoints, fmt.Sprintf("localhost:%d", inst.SidecarGRPCPort))
	}

	// Start Store Gateway if object store config is provided
	if h.config.ObjStoreConfig != "" {
		// Prepare config with CA cert and prefix override
		objstoreConfigPath, err := h.prepareObjStoreConfig()
		if err != nil {
			return fmt.Errorf("prepare objstore config: %w", err)
		}

		storeCfg := components.DefaultStoreConfig(
			"store",
			filepath.Join(h.config.BaseDir, "store"),
			h.storeHTTPPort,
			h.storeGRPCPort,
			objstoreConfigPath,
		)
		storeProcessCfg, err := storeCfg.ToProcessConfig(h.config.ThanosBinary)
		if err != nil {
			return fmt.Errorf("create store config: %w", err)
		}
		storeProcessCfg.MemoryLimit = h.config.MemoryLimit
		storeProcessCfg.Env = append(storeProcessCfg.Env, goEnv...)
		if _, err := h.manager.Start(ctx, storeProcessCfg); err != nil {
			return fmt.Errorf("start store: %w", err)
		}

		if err := h.manager.WaitReady(ctx, "store", 60*time.Second); err != nil {
			return fmt.Errorf("store not ready: %w", err)
		}

		storeEndpoints = append(storeEndpoints, fmt.Sprintf("localhost:%d", h.storeGRPCPort))
	}

	// Start Querier (leaf querier in distributed mode)
	querierName := "querier"
	if h.config.DistributedMode {
		querierName = "querier-leaf"
	}
	querierCfg := components.DefaultQuerierConfig(
		querierName,
		filepath.Join(h.config.BaseDir, querierName),
		h.querierHTTPPort,
		h.querierGRPCPort,
		storeEndpoints,
	)
	querierProcessCfg, err := querierCfg.ToProcessConfig(h.config.ThanosBinary)
	if err != nil {
		return fmt.Errorf("create querier config: %w", err)
	}
	querierProcessCfg.MemoryLimit = h.config.MemoryLimit
	querierProcessCfg.Env = append(querierProcessCfg.Env, goEnv...)
	if _, err := h.manager.Start(ctx, querierProcessCfg); err != nil {
		return fmt.Errorf("start querier: %w", err)
	}

	// Wait for Querier
	if err := h.manager.WaitReady(ctx, querierName, 30*time.Second); err != nil {
		return fmt.Errorf("querier not ready: %w", err)
	}

	// Start root Querier in distributed mode (connects to leaf querier)
	if h.config.DistributedMode {
		rootQuerierCfg := components.DefaultQuerierConfig(
			"querier-root",
			filepath.Join(h.config.BaseDir, "querier-root"),
			h.querierRootHTTPPort,
			h.querierRootGRPCPort,
			[]string{fmt.Sprintf("localhost:%d", h.querierGRPCPort)},
		)
		rootQuerierProcessCfg, err := rootQuerierCfg.ToProcessConfig(h.config.ThanosBinary)
		if err != nil {
			return fmt.Errorf("create root querier config: %w", err)
		}
		rootQuerierProcessCfg.MemoryLimit = h.config.MemoryLimit
		rootQuerierProcessCfg.Env = append(rootQuerierProcessCfg.Env, goEnv...)
		if _, err := h.manager.Start(ctx, rootQuerierProcessCfg); err != nil {
			return fmt.Errorf("start root querier: %w", err)
		}

		if err := h.manager.WaitReady(ctx, "querier-root", 30*time.Second); err != nil {
			return fmt.Errorf("root querier not ready: %w", err)
		}
	}

	// Save state so other commands can find the running processes
	if err := h.manager.SaveState(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to save state: %v\n", err)
	}

	return nil
}

// Stop stops all components.
func (h *Harness) Stop() error {
	err := h.manager.StopAll()
	// Clear state file since processes are stopped
	h.manager.ClearState()
	return err
}

// QueryResult holds query results and resource metrics.
type QueryResult struct {
	Query       string                          `json:"query"`
	StartTime   time.Time                       `json:"start_time"`
	EndTime     time.Time                       `json:"end_time"`
	Duration    time.Duration                   `json:"duration"`
	Status      string                          `json:"status"`
	Error       string                          `json:"error,omitempty"`
	ResultCount int                             `json:"result_count"`
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

// PrometheusURL returns the Prometheus HTTP URL for the first instance.
// Use PrometheusURLs() for all instances.
func (h *Harness) PrometheusURL() string {
	if len(h.instances) == 0 {
		return ""
	}
	return fmt.Sprintf("http://localhost:%d", h.instances[0].PrometheusHTTPPort)
}

// PrometheusURLs returns Prometheus HTTP URLs for all instances.
func (h *Harness) PrometheusURLs() []string {
	urls := make([]string, len(h.instances))
	for i, inst := range h.instances {
		urls[i] = fmt.Sprintf("http://localhost:%d", inst.PrometheusHTTPPort)
	}
	return urls
}

// Instances returns all configured instances.
func (h *Harness) Instances() []Instance {
	return h.instances
}

// NumInstances returns the number of Prometheus+Sidecar instances.
func (h *Harness) NumInstances() int {
	return len(h.instances)
}

// QuerierURL returns the Querier HTTP URL.
// In distributed mode, this returns the leaf querier URL.
// Use RootQuerierURL() to get the root querier in distributed mode.
func (h *Harness) QuerierURL() string {
	if h.config.DistributedMode {
		return fmt.Sprintf("http://localhost:%d", h.querierRootHTTPPort)
	}
	return fmt.Sprintf("http://localhost:%d", h.querierHTTPPort)
}

// LeafQuerierURL returns the leaf Querier HTTP URL (same as QuerierURL in non-distributed mode).
func (h *Harness) LeafQuerierURL() string {
	return fmt.Sprintf("http://localhost:%d", h.querierHTTPPort)
}

// RootQuerierURL returns the root Querier HTTP URL (empty if not in distributed mode).
func (h *Harness) RootQuerierURL() string {
	if !h.config.DistributedMode {
		return ""
	}
	return fmt.Sprintf("http://localhost:%d", h.querierRootHTTPPort)
}

// DistributedMode returns true if running in distributed query mode.
func (h *Harness) DistributedMode() bool {
	return h.config.DistributedMode
}

// StoreURL returns the Store Gateway HTTP URL (empty if not enabled).
func (h *Harness) StoreURL() string {
	if h.config.ObjStoreConfig == "" {
		return ""
	}
	return fmt.Sprintf("http://localhost:%d", h.storeHTTPPort)
}

// StoreEnabled returns true if the Store Gateway is configured.
func (h *Harness) StoreEnabled() bool {
	return h.config.ObjStoreConfig != ""
}

// Manager returns the process manager for advanced operations.
func (h *Harness) Manager() *process.Manager {
	return h.manager
}

// Info returns information about the harness state.
func (h *Harness) Info() map[string]interface{} {
	info := map[string]interface{}{
		"base_dir":       h.config.BaseDir,
		"components":     h.manager.List(),
		"cgroup_version": h.manager.CgroupVersion(),
		"scopes_enabled": h.manager.ScopesEnabled(),
		"querier_url":    h.QuerierURL(),
		"num_instances":  len(h.instances),
	}
	if len(h.instances) == 1 {
		info["prometheus_url"] = h.PrometheusURL()
	} else {
		info["prometheus_urls"] = h.PrometheusURLs()
	}
	if h.StoreEnabled() {
		info["store_url"] = h.StoreURL()
		info["objstore_config"] = h.config.ObjStoreConfig
	}
	if h.DistributedMode() {
		info["distributed_mode"] = true
		info["leaf_querier_url"] = h.LeafQuerierURL()
		info["root_querier_url"] = h.RootQuerierURL()
	}
	return info
}
