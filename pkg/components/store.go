package components

import (
	"fmt"
	"os"

	"github.com/thanos-workspace/test-harness/pkg/process"
)

// StoreConfig holds Thanos Store Gateway configuration.
type StoreConfig struct {
	Name              string
	DataDir           string
	HTTPPort          int
	GRPCPort          int
	ObjStoreConfig    string // Path to object store config file
	SyncBlockDuration string // How often to sync blocks from bucket (default 3m)
}

// DefaultStoreConfig returns default Store configuration.
func DefaultStoreConfig(name string, dataDir string, httpPort, grpcPort int, objStoreConfig string) StoreConfig {
	return StoreConfig{
		Name:              name,
		DataDir:           dataDir,
		HTTPPort:          httpPort,
		GRPCPort:          grpcPort,
		ObjStoreConfig:    objStoreConfig,
		SyncBlockDuration: "3m",
	}
}

// ToProcessConfig converts to a process.ComponentConfig.
func (c StoreConfig) ToProcessConfig(thanosBinary string) (process.ComponentConfig, error) {
	if err := os.MkdirAll(c.DataDir, 0755); err != nil {
		return process.ComponentConfig{}, fmt.Errorf("create store dir: %w", err)
	}

	// Verify objstore config exists
	if _, err := os.Stat(c.ObjStoreConfig); err != nil {
		return process.ComponentConfig{}, fmt.Errorf("objstore config not found: %w", err)
	}

	args := []string{
		"store",
		fmt.Sprintf("--http-address=0.0.0.0:%d", c.HTTPPort),
		fmt.Sprintf("--grpc-address=0.0.0.0:%d", c.GRPCPort),
		fmt.Sprintf("--data-dir=%s", c.DataDir),
		fmt.Sprintf("--objstore.config-file=%s", c.ObjStoreConfig),
		"--log.level=warn",
	}

	if c.SyncBlockDuration != "" {
		args = append(args, fmt.Sprintf("--sync-block-duration=%s", c.SyncBlockDuration))
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
