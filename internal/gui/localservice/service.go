package localservice

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
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
	m.cmd = nil
	m.mu.Unlock()

	if err := exec.Command("taskkill", "/F", "/T", "/PID", strconv.Itoa(pid)).Run(); err != nil {
		if killErr := cmd.Process.Kill(); killErr != nil {
			return true, fmt.Errorf("stop process tree: %w; kill process: %v", err, killErr)
		}
		return true, fmt.Errorf("stop process tree: %w", err)
	}
	return true, nil
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
