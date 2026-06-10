package localservice

import (
	"path/filepath"
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
