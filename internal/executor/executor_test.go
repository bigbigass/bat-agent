package executor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/liqixin/deploy-agent/internal/registry"
)

func makeRegistry(t *testing.T, files map[string]string) *registry.Registry {
	t.Helper()

	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	reg, err := registry.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

func TestRunCollectRejectsInvalidScriptName(t *testing.T) {
	exec := New(makeRegistry(t, nil), time.Second)

	res, err := exec.RunCollect(context.Background(), `..\evil.bat`)

	if !errors.Is(err, ErrInvalidScriptName) {
		t.Fatalf("expected ErrInvalidScriptName, got %v", err)
	}
	if res.Script != `..\evil.bat` {
		t.Fatalf("Script = %q, want requested script", res.Script)
	}
}

func TestRunCollectRejectsMissingScript(t *testing.T) {
	exec := New(makeRegistry(t, nil), time.Second)

	res, err := exec.RunCollect(context.Background(), "missing.bat")

	if !errors.Is(err, ErrScriptNotFound) {
		t.Fatalf("expected ErrScriptNotFound, got %v", err)
	}
	if res.Script != "missing.bat" {
		t.Fatalf("Script = %q, want requested script", res.Script)
	}
}

func TestRunStreamReturnsBusyWhenSameScriptIsLocked(t *testing.T) {
	reg := makeRegistry(t, map[string]string{"busy.bat": "@echo off\r\necho busy\r\n"})
	entry, err := reg.Lookup("busy.bat")
	if err != nil {
		t.Fatal(err)
	}
	if !entry.TryLock() {
		t.Fatal("expected lock")
	}
	defer entry.Unlock()

	exec := New(reg, time.Second)
	res, err := exec.RunStream(context.Background(), "busy.bat", nil)

	if !errors.Is(err, ErrScriptBusy) {
		t.Fatalf("expected ErrScriptBusy, got %v", err)
	}
	if res.Script != "busy.bat" {
		t.Fatalf("Script = %q, want registry entry name", res.Script)
	}
}

func TestStableErrorText(t *testing.T) {
	cases := map[error]string{
		ErrInvalidScriptName: "invalid script name",
		ErrScriptNotFound:    "script not found",
		ErrScriptBusy:        "script is already running",
		ErrRunnerStart:       "runner start failed",
		ErrScriptTimedOut:    "script timed out",
	}

	for err, want := range cases {
		if got := StableError(err); got != want {
			t.Fatalf("StableError(%v) = %q, want %q", err, got, want)
		}
	}
}

func TestRunCollectCapturesOutput(t *testing.T) {
	reg := makeRegistry(t, map[string]string{"hello.bat": "@echo off\r\necho hello\r\n"})
	exec := New(reg, 5*time.Second)

	res, err := exec.RunCollect(context.Background(), "hello.bat")
	if err != nil {
		t.Fatalf("RunCollect returned error: %v", err)
	}

	if res.Script != "hello.bat" {
		t.Fatalf("Script = %q, want hello.bat", res.Script)
	}
	if res.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0", res.ExitCode)
	}
	if !strings.Contains(res.Stdout, "hello") {
		t.Fatalf("Stdout = %q, want it to contain hello", res.Stdout)
	}
	if res.StartedAt.IsZero() {
		t.Fatal("StartedAt should be set")
	}
	if res.FinishedAt.IsZero() {
		t.Fatal("FinishedAt should be set")
	}
	if res.TimedOut {
		t.Fatal("TimedOut = true, want false")
	}
}

func TestRunStreamForwardsOutputChunks(t *testing.T) {
	reg := makeRegistry(t, map[string]string{"hello.bat": "@echo off\r\necho hello\r\n"})
	exec := New(reg, 5*time.Second)

	var stdout string
	res, err := exec.RunStream(context.Background(), "hello.bat", func(chunk OutputChunk) {
		if chunk.Stream == "stdout" {
			stdout += chunk.Data
		}
	})
	if err != nil {
		t.Fatalf("RunStream returned error: %v", err)
	}

	if res.Script != "hello.bat" {
		t.Fatalf("Script = %q, want hello.bat", res.Script)
	}
	if !strings.Contains(stdout, "hello") {
		t.Fatalf("streamed stdout = %q, want it to contain hello", stdout)
	}
}

func TestRunCollectNonZeroExitCodeReturnsResultWithoutError(t *testing.T) {
	reg := makeRegistry(t, map[string]string{"fail.bat": "@echo off\r\nexit /b 7\r\n"})
	exec := New(reg, 5*time.Second)

	res, err := exec.RunCollect(context.Background(), "fail.bat")
	if err != nil {
		t.Fatalf("RunCollect returned error: %v", err)
	}

	if res.Script != "fail.bat" {
		t.Fatalf("Script = %q, want fail.bat", res.Script)
	}
	if res.ExitCode != 7 {
		t.Fatalf("ExitCode = %d, want 7", res.ExitCode)
	}
}
