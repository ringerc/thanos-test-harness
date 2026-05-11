package components

import (
	"fmt"
	"os"

	"github.com/thanos-workspace/test-harness/pkg/process"
)

// QuerierConfig holds Thanos Querier configuration.
type QuerierConfig struct {
	Name       string
	DataDir    string
	HTTPPort   int
	GRPCPort   int
	StoreAddrs []string // gRPC addresses of stores (sidecars, store gateways)
}

// DefaultQuerierConfig returns default Querier configuration.
func DefaultQuerierConfig(name string, dataDir string, httpPort, grpcPort int, storeAddrs []string) QuerierConfig {
	return QuerierConfig{
		Name:       name,
		DataDir:    dataDir,
		HTTPPort:   httpPort,
		GRPCPort:   grpcPort,
		StoreAddrs: storeAddrs,
	}
}

// ToProcessConfig converts to a process.ComponentConfig.
func (c QuerierConfig) ToProcessConfig(thanosBinary string) (process.ComponentConfig, error) {
	if err := os.MkdirAll(c.DataDir, 0755); err != nil {
		return process.ComponentConfig{}, fmt.Errorf("create querier dir: %w", err)
	}

	args := []string{
		"query",
		fmt.Sprintf("--http-address=0.0.0.0:%d", c.HTTPPort),
		fmt.Sprintf("--grpc-address=0.0.0.0:%d", c.GRPCPort),
		"--log.level=warn",
		"--query.replica-label=replica",
	}

	for _, addr := range c.StoreAddrs {
		args = append(args, "--endpoint="+addr)
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
