package localservice

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
)

var (
	killProcessTree = func(pid int) error {
		return exec.Command("taskkill", "/F", "/T", "/PID", strconv.Itoa(pid)).Run()
	}
	killProcess = func(process *os.Process) error {
		return process.Kill()
	}
)

type Manager struct {
	path string
	mu   sync.Mutex
	cmd  *exec.Cmd
}

func AgentPath(guiPath string) string {
	return filepath.Join(filepath.Dir(guiPath), "deploy-agent.exe")
}

func New(path string) *Manager {
	return &Manager{path: path}
}

func (m *Manager) Path() string {
	return m.path
}

func (m *Manager) Start(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.cmd != nil && m.cmd.Process != nil {
		return nil
	}
	cmd := exec.CommandContext(ctx, m.path)
	cmd.Dir = filepath.Dir(m.path)
	if err := cmd.Start(); err != nil {
		return err
	}
	m.cmd = cmd
	go func() {
		_ = cmd.Wait()
		m.mu.Lock()
		if m.cmd == cmd {
			m.cmd = nil
		}
		m.mu.Unlock()
	}()
	return nil
}

func (m *Manager) Stop() (bool, error) {
	m.mu.Lock()
	if m.cmd == nil || m.cmd.Process == nil {
		m.mu.Unlock()
		return false, nil
	}
	cmd := m.cmd
	pid := cmd.Process.Pid
	m.mu.Unlock()

	if err := killProcessTree(pid); err != nil {
		if killErr := killProcess(cmd.Process); killErr != nil {
			return false, fmt.Errorf("stop process tree: %w; kill process: %v", err, killErr)
		}
		m.clear(cmd)
		return true, fmt.Errorf("stop process tree: %w", err)
	}
	m.clear(cmd)
	return true, nil
}

func (m *Manager) clear(cmd *exec.Cmd) {
	m.mu.Lock()
	if m.cmd == cmd {
		m.cmd = nil
	}
	m.mu.Unlock()
}

func (m *Manager) Running() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cmd != nil && m.cmd.Process != nil
}

func (m *Manager) PID() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cmd == nil || m.cmd.Process == nil {
		return 0
	}
	return m.cmd.Process.Pid
}
