//go:build windows

package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunStreamCapturesAndStreamsStdout(t *testing.T) {
	scriptPath := writeBatch(t, "echo hello\r\n")

	var chunks []OutputChunk
	res, err := RunStream(context.Background(), scriptPath, 5*time.Second, func(chunk OutputChunk) {
		chunks = append(chunks, chunk)
	})
	if err != nil {
		t.Fatalf("RunStream returned error: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0", res.ExitCode)
	}
	if !strings.Contains(res.Stdout, "hello") {
		t.Fatalf("Stdout = %q, want it to contain hello", res.Stdout)
	}

	var streamedStdout string
	for _, chunk := range chunks {
		if chunk.Stream == StreamStdout {
			streamedStdout += chunk.Data
		}
	}
	if !strings.Contains(streamedStdout, "hello") {
		t.Fatalf("streamed stdout = %q, want it to contain hello", streamedStdout)
	}
}

func TestRunPreservesCollectOnlyBehavior(t *testing.T) {
	scriptPath := writeBatch(t, "echo collected\r\n")

	res, err := Run(context.Background(), scriptPath, 5*time.Second)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0", res.ExitCode)
	}
	if !strings.Contains(res.Stdout, "collected") {
		t.Fatalf("Stdout = %q, want it to contain collected", res.Stdout)
	}
}

func TestRunStreamNonZeroExitCodeReturnsNilError(t *testing.T) {
	scriptPath := writeBatch(t, "exit /b 7\r\n")

	res, err := RunStream(context.Background(), scriptPath, 5*time.Second, nil)
	if err != nil {
		t.Fatalf("RunStream returned error: %v", err)
	}
	if res.ExitCode != 7 {
		t.Fatalf("ExitCode = %d, want 7", res.ExitCode)
	}
}

func writeBatch(t *testing.T, contents string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "script.bat")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write batch file: %v", err)
	}
	return path
}
