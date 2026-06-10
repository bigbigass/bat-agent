package localservice

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestAgentPathUsesSameDirectory(t *testing.T) {
	got := AgentPath(filepath.Join("C:", "tools", "deploy-agent-gui.exe"))
	want := filepath.Join("C:", "tools", "deploy-agent.exe")
	if got != want {
		t.Fatalf("AgentPath = %q, want %q", got, want)
	}
}

func TestManagerStartsStopped(t *testing.T) {
	m := New(filepath.Join(t.TempDir(), "deploy-agent.exe"))

	if m.Running() {
		t.Fatal("Running = true, want false")
	}
	if m.PID() != 0 {
		t.Fatalf("PID = %d, want 0", m.PID())
	}
	stopped, err := m.Stop()
	if err != nil {
		t.Fatalf("Stop returned error: %v", err)
	}
	if stopped {
		t.Fatal("Stop stopped a process, want false")
	}
}

func TestStopClearsTrackedProcessAfterTreeKill(t *testing.T) {
	m := managerWithFakeProcess(t)
	restoreKillers(t, func(int) error {
		return nil
	}, func(*os.Process) error {
		t.Fatal("fallback process kill should not be called")
		return nil
	})

	stopped, err := m.Stop()

	if err != nil {
		t.Fatalf("Stop returned error: %v", err)
	}
	if !stopped {
		t.Fatal("Stop stopped = false, want true")
	}
	if m.Running() {
		t.Fatal("Running = true, want false")
	}
}

func TestStopClearsTrackedProcessWhenFallbackKillsParent(t *testing.T) {
	m := managerWithFakeProcess(t)
	restoreKillers(t, func(int) error {
		return errors.New("taskkill failed")
	}, func(*os.Process) error {
		return nil
	})

	stopped, err := m.Stop()

	if err == nil {
		t.Fatal("Stop returned nil error, want tree kill error")
	}
	if !strings.Contains(err.Error(), "stop process tree") {
		t.Fatalf("Stop error = %q, want tree kill context", err.Error())
	}
	if !stopped {
		t.Fatal("Stop stopped = false, want true")
	}
	if m.Running() {
		t.Fatal("Running = true, want false")
	}
}

func TestStopKeepsTrackedProcessWhenAllKillAttemptsFail(t *testing.T) {
	m := managerWithFakeProcess(t)
	restoreKillers(t, func(int) error {
		return errors.New("taskkill failed")
	}, func(*os.Process) error {
		return errors.New("kill failed")
	})

	stopped, err := m.Stop()

	if err == nil {
		t.Fatal("Stop returned nil error, want kill error")
	}
	if stopped {
		t.Fatal("Stop stopped = true, want false")
	}
	if !m.Running() {
		t.Fatal("Running = false, want true so stop can be retried")
	}
}

func managerWithFakeProcess(t *testing.T) *Manager {
	t.Helper()

	m := New(filepath.Join(t.TempDir(), "deploy-agent.exe"))
	m.cmd = &exec.Cmd{Process: &os.Process{Pid: 999999}}
	return m
}

func restoreKillers(t *testing.T, tree func(int) error, process func(*os.Process) error) {
	t.Helper()

	oldTree := killProcessTree
	oldProcess := killProcess
	killProcessTree = tree
	killProcess = process
	t.Cleanup(func() {
		killProcessTree = oldTree
		killProcess = oldProcess
	})
}
