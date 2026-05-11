package process

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// Component represents a running Thanos/Prometheus component.
type Component struct {
	Name      string
	Scope     *Scope
	HTTPAddr  string // address for health checks and pprof
	DataDir   string
	StartTime time.Time
	Config    ComponentConfig
}

// ComponentConfig holds configuration for starting a component.
type ComponentConfig struct {
	Name       string
	Command    string
	Args       []string
	Env        []string
	Dir        string
	HTTPPort   int
	GRPCPort   int
	HealthPath string // HTTP path for health check, e.g., "/-/ready"
}

// Manager manages the lifecycle of multiple components.
type Manager struct {
	mu         sync.RWMutex
	components map[string]*Component
	cgroup     *CgroupReader
	baseDir    string
}

// NewManager creates a new process manager.
func NewManager(baseDir string) *Manager {
	return &Manager{
		components: make(map[string]*Component),
		cgroup:     NewCgroupReader(),
		baseDir:    baseDir,
	}
}

// Start starts a component with the given configuration.
func (m *Manager) Start(ctx context.Context, cfg ComponentConfig) (*Component, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.components[cfg.Name]; exists {
		return nil, fmt.Errorf("component %s already running", cfg.Name)
	}

	scope := NewScope("thanos-harness-" + cfg.Name)
	if err := scope.Start(ctx, cfg.Command, cfg.Args, cfg.Env, cfg.Dir); err != nil {
		return nil, fmt.Errorf("start component %s: %w", cfg.Name, err)
	}

	comp := &Component{
		Name:      cfg.Name,
		Scope:     scope,
		HTTPAddr:  fmt.Sprintf("http://localhost:%d", cfg.HTTPPort),
		DataDir:   cfg.Dir,
		StartTime: time.Now(),
		Config:    cfg,
	}
	m.components[cfg.Name] = comp

	return comp, nil
}

// Stop stops a component by name.
func (m *Manager) Stop(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	comp, exists := m.components[name]
	if !exists {
		return fmt.Errorf("component %s not found", name)
	}

	if err := comp.Scope.Stop(); err != nil {
		return fmt.Errorf("stop component %s: %w", name, err)
	}

	delete(m.components, name)
	return nil
}

// StopAll stops all running components.
func (m *Manager) StopAll() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	var errs []error
	for name, comp := range m.components {
		if err := comp.Scope.Stop(); err != nil {
			errs = append(errs, fmt.Errorf("stop %s: %w", name, err))
		}
	}
	m.components = make(map[string]*Component)

	if len(errs) > 0 {
		return fmt.Errorf("errors stopping components: %v", errs)
	}
	return nil
}

// WaitReady waits for a component to become healthy.
func (m *Manager) WaitReady(ctx context.Context, name string, timeout time.Duration) error {
	m.mu.RLock()
	comp, exists := m.components[name]
	m.mu.RUnlock()

	if !exists {
		return fmt.Errorf("component %s not found", name)
	}

	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	healthURL := comp.HTTPAddr + comp.Config.HealthPath

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if time.Now().After(deadline) {
				return fmt.Errorf("timeout waiting for %s to be ready", name)
			}

			if !comp.Scope.Running() {
				return fmt.Errorf("component %s exited unexpectedly", name)
			}

			resp, err := http.Get(healthURL)
			if err == nil {
				resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					return nil
				}
			}
		}
	}
}

// GetStats returns current cgroup stats for a component.
func (m *Manager) GetStats(name string) (*CgroupStats, error) {
	m.mu.RLock()
	comp, exists := m.components[name]
	m.mu.RUnlock()

	if !exists {
		return nil, fmt.Errorf("component %s not found", name)
	}

	cgroupPath := comp.Scope.CgroupPath()
	if cgroupPath != "" {
		return m.cgroup.ReadStats(cgroupPath)
	}

	// Fall back to PID-based lookup
	return m.cgroup.ReadStatsByPID(comp.Scope.PID())
}

// GetAllStats returns stats for all running components.
func (m *Manager) GetAllStats() map[string]*CgroupStats {
	m.mu.RLock()
	defer m.mu.RUnlock()

	stats := make(map[string]*CgroupStats)
	for name := range m.components {
		if s, err := m.GetStats(name); err == nil {
			stats[name] = s
		}
	}
	return stats
}

// List returns names of all running components.
func (m *Manager) List() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	names := make([]string, 0, len(m.components))
	for name := range m.components {
		names = append(names, name)
	}
	return names
}

// Get returns a component by name.
func (m *Manager) Get(name string) (*Component, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	comp, exists := m.components[name]
	return comp, exists
}

// Running returns true if the named component is running.
func (m *Manager) Running(name string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	comp, exists := m.components[name]
	return exists && comp.Scope.Running()
}

// CgroupVersion returns the cgroup version in use.
func (m *Manager) CgroupVersion() int {
	return m.cgroup.Version()
}

// ScopesEnabled returns true if systemd scopes are being used.
func (m *Manager) ScopesEnabled() bool {
	// Check by looking at any component
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, comp := range m.components {
		return comp.Scope.ScopeEnabled()
	}
	// If no components, check if systemd is available
	return systemdAvailable()
}
