package process

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Scope wraps a process in a systemd scope for cgroup isolation.
type Scope struct {
	Name        string
	Cmd         *exec.Cmd
	pid         int
	enabled     bool
	memoryLimit int64  // Memory limit in bytes (0 = unlimited)
	cgroupPath  string // Path to cgroup for manual limits
}

// NewScope creates a scope wrapper. If systemd is not available, it falls back to plain exec.
func NewScope(name string) *Scope {
	return &Scope{
		Name:    name,
		enabled: systemdAvailable(),
	}
}

// SetMemoryLimit sets a memory limit in bytes. Must be called before Start.
func (s *Scope) SetMemoryLimit(bytes int64) {
	s.memoryLimit = bytes
}

// MemoryLimit returns the configured memory limit in bytes (0 = unlimited).
func (s *Scope) MemoryLimit() int64 {
	return s.memoryLimit
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
	}

	// Add memory limit if configured
	if s.memoryLimit > 0 {
		scopeArgs = append(scopeArgs, fmt.Sprintf("--property=MemoryMax=%d", s.memoryLimit))
		scopeArgs = append(scopeArgs, "--property=MemorySwapMax=0") // Prevent swap to make limits strict
	}

	scopeArgs = append(scopeArgs, command)
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
	// If memory limit is set, start inside cgroup from the beginning
	if s.memoryLimit > 0 {
		return s.startInCgroup(ctx, command, args, env, dir)
	}

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

func (s *Scope) startInCgroup(ctx context.Context, command string, args []string, env []string, dir string) error {
	cgroupBase := "/sys/fs/cgroup/thanos-harness"
	s.cgroupPath = fmt.Sprintf("%s/%s", cgroupBase, s.Name)

	// Set up cgroup with memory limit before starting process
	if err := s.setupCgroup(); err != nil {
		return fmt.Errorf("setup cgroup: %w", err)
	}

	// Build wrapper script that moves shell into cgroup then execs the command
	// This ensures the process starts inside the cgroup with memory accounting from the beginning
	uid := os.Getuid()
	gid := os.Getgid()
	procsPath := fmt.Sprintf("%s/cgroup.procs", s.cgroupPath)

	// Use sudo to run a shell that: moves itself to cgroup, drops to user, execs command
	wrapperScript := fmt.Sprintf(
		"echo $$ > %s && exec setpriv --reuid=%d --regid=%d --init-groups %s %s",
		procsPath, uid, gid, command, strings.Join(args, " "),
	)

	s.Cmd = exec.CommandContext(ctx, "sudo", "sh", "-c", wrapperScript)
	s.Cmd.Env = append(os.Environ(), env...)
	s.Cmd.Dir = dir
	s.Cmd.Stdout = os.Stdout
	s.Cmd.Stderr = os.Stderr

	if err := s.Cmd.Start(); err != nil {
		return fmt.Errorf("start process in cgroup %s: %w", s.Name, err)
	}

	// The sudo wrapper PID is not what we want - we need the actual process PID.
	// Read it from cgroup.procs which contains the PIDs of processes in this cgroup.
	// The wrapper script moves itself (and thus the exec'd process) into the cgroup.
	s.pid = s.Cmd.Process.Pid // fallback to wrapper PID

	// Wait briefly for the process to move into the cgroup and exec
	for i := 0; i < 50; i++ { // 500ms max
		time.Sleep(10 * time.Millisecond)
		actualPID, err := s.readActualPIDFromCgroup()
		if err == nil && actualPID > 0 && actualPID != s.Cmd.Process.Pid {
			s.pid = actualPID
			break
		}
	}

	return nil
}

func (s *Scope) setupCgroup() error {
	cgroupBase := "/sys/fs/cgroup/thanos-harness"

	// Create parent cgroup
	if err := exec.Command("sudo", "mkdir", "-p", cgroupBase).Run(); err != nil {
		return fmt.Errorf("mkdir cgroup base: %w", err)
	}

	// Enable memory controller
	subtreeCtl := fmt.Sprintf("%s/cgroup.subtree_control", cgroupBase)
	cmd := exec.Command("sudo", "tee", subtreeCtl)
	cmd.Stdin = strings.NewReader("+memory")
	cmd.Run()

	// Create child cgroup
	if err := exec.Command("sudo", "mkdir", "-p", s.cgroupPath).Run(); err != nil {
		return fmt.Errorf("mkdir cgroup: %w", err)
	}

	// Set memory limit
	memMaxPath := fmt.Sprintf("%s/memory.max", s.cgroupPath)
	cmd = exec.Command("sudo", "tee", memMaxPath)
	cmd.Stdin = strings.NewReader(fmt.Sprintf("%d", s.memoryLimit))
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("set memory.max: %w", err)
	}

	// Disable swap
	memSwapPath := fmt.Sprintf("%s/memory.swap.max", s.cgroupPath)
	swapCmd := exec.Command("sudo", "tee", memSwapPath)
	swapCmd.Stdin = strings.NewReader("0")
	swapCmd.Run()

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

// readActualPIDFromCgroup reads the actual process PID from cgroup.procs.
// When starting via sudo wrapper, the wrapper moves itself into the cgroup,
// then execs the actual command. This function finds that actual process.
func (s *Scope) readActualPIDFromCgroup() (int, error) {
	if s.cgroupPath == "" {
		return 0, fmt.Errorf("no cgroup path")
	}

	procsPath := fmt.Sprintf("%s/cgroup.procs", s.cgroupPath)
	data, err := os.ReadFile(procsPath)
	if err != nil {
		return 0, err
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}
		pid, err := strconv.Atoi(line)
		if err != nil {
			continue
		}
		// Return the first PID that's not the sudo wrapper
		if pid != s.Cmd.Process.Pid {
			return pid, nil
		}
	}

	// If only one PID (the wrapper), return it
	if len(lines) > 0 {
		pid, _ := strconv.Atoi(lines[0])
		return pid, nil
	}

	return 0, fmt.Errorf("no PIDs in cgroup")
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
