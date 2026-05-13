package process

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

// Scope wraps a process in a systemd scope for cgroup isolation.
type Scope struct {
	Name    string
	Cmd     *exec.Cmd
	pid     int
	enabled bool
}

// NewScope creates a scope wrapper. If systemd is not available, it falls back to plain exec.
func NewScope(name string) *Scope {
	return &Scope{
		Name:    name,
		enabled: systemdAvailable(),
	}
}

// Start executes the command within a systemd scope (or plain exec if unavailable).
func (s *Scope) Start(ctx context.Context, command string, args []string, env []string, dir string) error {
	if s.enabled {
		return s.startWithScope(ctx, command, args, env, dir)
	}
	return s.startPlain(ctx, command, args, env, dir)
}

func (s *Scope) startWithScope(ctx context.Context, command string, args []string, env []string, dir string) error {
	scopeArgs := []string{
		"--scope",
		"--unit=" + s.Name,
		"--slice=thanos-harness.slice",
		command,
	}
	scopeArgs = append(scopeArgs, args...)

	s.Cmd = exec.CommandContext(ctx, "systemd-run", scopeArgs...)
	s.Cmd.Env = append(os.Environ(), env...)
	s.Cmd.Dir = dir
	s.Cmd.Stdout = os.Stdout
	s.Cmd.Stderr = os.Stderr

	if err := s.Cmd.Start(); err != nil {
		return fmt.Errorf("start scope %s: %w", s.Name, err)
	}
	s.pid = s.Cmd.Process.Pid
	return nil
}

func (s *Scope) startPlain(ctx context.Context, command string, args []string, env []string, dir string) error {
	s.Cmd = exec.CommandContext(ctx, command, args...)
	s.Cmd.Env = append(os.Environ(), env...)
	s.Cmd.Dir = dir
	s.Cmd.Stdout = os.Stdout
	s.Cmd.Stderr = os.Stderr

	if err := s.Cmd.Start(); err != nil {
		return fmt.Errorf("start process %s: %w", s.Name, err)
	}
	s.pid = s.Cmd.Process.Pid
	return nil
}

// Stop terminates the process gracefully, then forcefully if needed.
func (s *Scope) Stop() error {
	if s.Cmd == nil || s.Cmd.Process == nil {
		return nil
	}

	// Try graceful shutdown first
	if err := s.Cmd.Process.Signal(syscall.SIGTERM); err != nil {
		return s.Cmd.Process.Kill()
	}

	// Wait briefly for graceful shutdown
	done := make(chan error, 1)
	go func() { done <- s.Cmd.Wait() }()

	select {
	case <-done:
		return nil
	default:
		return s.Cmd.Process.Kill()
	}
}

// PID returns the process ID.
func (s *Scope) PID() int {
	return s.pid
}

// AttachToPID attaches to an existing process by PID (for state recovery).
func (s *Scope) AttachToPID(pid int) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("find process %d: %w", pid, err)
	}
	// Check if process is actually running by sending signal 0
	if err := proc.Signal(syscall.Signal(0)); err != nil {
		return fmt.Errorf("process %d not running: %w", pid, err)
	}
	s.pid = pid
	return nil
}

// Running returns true if the process is still running.
func (s *Scope) Running() bool {
	if s.Cmd == nil || s.Cmd.Process == nil {
		return false
	}
	return s.Cmd.ProcessState == nil || !s.Cmd.ProcessState.Exited()
}

// CgroupPath returns the cgroup path for this scope.
func (s *Scope) CgroupPath() string {
	if !s.enabled {
		return ""
	}
	return fmt.Sprintf("/sys/fs/cgroup/thanos-harness.slice/%s.scope", s.Name)
}

// systemdAvailable checks if systemd-run is available.
func systemdAvailable() bool {
	_, err := exec.LookPath("systemd-run")
	if err != nil {
		return false
	}
	// Also check if we're in a systemd environment
	if _, err := os.Stat("/run/systemd/system"); os.IsNotExist(err) {
		return false
	}
	return true
}

// ScopeEnabled returns true if systemd scopes are being used.
func (s *Scope) ScopeEnabled() bool {
	return s.enabled
}

// UnitName returns the systemd unit name.
func (s *Scope) UnitName() string {
	if !s.enabled {
		return ""
	}
	return s.Name + ".scope"
}

// parseMemoryStat parses a cgroup memory stat value.
func parseMemoryStat(content string, key string) int64 {
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(line, key+" ") {
			var value int64
			fmt.Sscanf(line, key+" %d", &value)
			return value
		}
	}
	return 0
}
