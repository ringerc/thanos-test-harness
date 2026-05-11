package components

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/thanos-workspace/test-harness/pkg/process"
)

// PrometheusConfig holds Prometheus-specific configuration.
type PrometheusConfig struct {
	Name           string
	DataDir        string
	HTTPPort       int
	ScrapeInterval string
	ExternalLabels map[string]string
}

// DefaultPrometheusConfig returns default Prometheus configuration.
func DefaultPrometheusConfig(name string, dataDir string, httpPort int) PrometheusConfig {
	return PrometheusConfig{
		Name:           name,
		DataDir:        dataDir,
		HTTPPort:       httpPort,
		ScrapeInterval: "15s",
		ExternalLabels: map[string]string{
			"prometheus": name,
			"replica":    "0",
		},
	}
}

// ToProcessConfig converts to a process.ComponentConfig.
func (c PrometheusConfig) ToProcessConfig(prometheusBinary string) (process.ComponentConfig, error) {
	if err := os.MkdirAll(c.DataDir, 0755); err != nil {
		return process.ComponentConfig{}, fmt.Errorf("create data dir: %w", err)
	}

	configPath := filepath.Join(c.DataDir, "prometheus.yml")
	if err := c.writeConfig(configPath); err != nil {
		return process.ComponentConfig{}, err
	}

	args := []string{
		"--config.file=" + configPath,
		"--storage.tsdb.path=" + filepath.Join(c.DataDir, "tsdb"),
		"--storage.tsdb.min-block-duration=2h",
		"--storage.tsdb.max-block-duration=2h",
		fmt.Sprintf("--web.listen-address=:%d", c.HTTPPort),
		"--web.enable-remote-write-receiver",
		"--log.level=warn",
	}

	return process.ComponentConfig{
		Name:       c.Name,
		Command:    prometheusBinary,
		Args:       args,
		Dir:        c.DataDir,
		HTTPPort:   c.HTTPPort,
		HealthPath: "/-/ready",
	}, nil
}

func (c PrometheusConfig) writeConfig(path string) error {
	// Build external_labels YAML
	externalLabels := ""
	for k, v := range c.ExternalLabels {
		externalLabels += fmt.Sprintf("    %s: %s\n", k, v)
	}

	config := fmt.Sprintf(`global:
  scrape_interval: %s
  external_labels:
%s
scrape_configs:
  - job_name: 'prometheus'
    static_configs:
      - targets: ['localhost:%d']
`, c.ScrapeInterval, externalLabels, c.HTTPPort)

	return os.WriteFile(path, []byte(config), 0644)
}
