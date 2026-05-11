package components

import (
	"fmt"
	"os"

	"github.com/thanos-workspace/test-harness/pkg/process"
)

// SidecarConfig holds Thanos Sidecar configuration.
type SidecarConfig struct {
	Name           string
	DataDir        string
	HTTPPort       int
	GRPCPort       int
	PrometheusURL  string
	TSDBPath       string
}

// DefaultSidecarConfig returns default Sidecar configuration.
func DefaultSidecarConfig(name string, dataDir string, httpPort, grpcPort int, prometheusURL, tsdbPath string) SidecarConfig {
	return SidecarConfig{
		Name:          name,
		DataDir:       dataDir,
		HTTPPort:      httpPort,
		GRPCPort:      grpcPort,
		PrometheusURL: prometheusURL,
		TSDBPath:      tsdbPath,
	}
}

// ToProcessConfig converts to a process.ComponentConfig.
func (c SidecarConfig) ToProcessConfig(thanosBinary string) (process.ComponentConfig, error) {
	if err := os.MkdirAll(c.DataDir, 0755); err != nil {
		return process.ComponentConfig{}, fmt.Errorf("create sidecar dir: %w", err)
	}

	args := []string{
		"sidecar",
		fmt.Sprintf("--http-address=0.0.0.0:%d", c.HTTPPort),
		fmt.Sprintf("--grpc-address=0.0.0.0:%d", c.GRPCPort),
		"--prometheus.url=" + c.PrometheusURL,
		"--tsdb.path=" + c.TSDBPath,
		"--log.level=warn",
	}

	return process.ComponentConfig{
		Name:       c.Name,
		Command:    thanosBinary,
		Args:       args,
		Dir:        c.DataDir,
		HTTPPort:   c.HTTPPort,
		GRPCPort:   c.GRPCPort,
		HealthPath: "/-/ready",
	}, nil
}
