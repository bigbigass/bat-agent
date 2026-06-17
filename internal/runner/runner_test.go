//go:build windows

package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
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

func TestRunStreamCapturesAndStreamsStderr(t *testing.T) {
	scriptPath := writeBatch(t, "echo problem 1>&2\r\n")

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
	if !strings.Contains(res.Stderr, "problem") {
		t.Fatalf("Stderr = %q, want it to contain problem", res.Stderr)
	}

	var streamedStderr string
	for _, chunk := range chunks {
		if chunk.Stream == StreamStderr {
			streamedStderr += chunk.Data
		}
	}
	if !strings.Contains(streamedStderr, "problem") {
		t.Fatalf("streamed stderr = %q, want it to contain problem", streamedStderr)
	}
}

func TestRunStreamWithArgsPassesArguments(t *testing.T) {
	scriptPath := writeBatch(t, "@echo off\r\necho project=%~1\r\necho artifact=%~2\r\n")

	res, err := RunStreamWithArgs(context.Background(), scriptPath, []string{"Project A", "app.zip"}, 5*time.Second, nil)
	if err != nil {
		t.Fatalf("RunStreamWithArgs returned error: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0", res.ExitCode)
	}
	if !strings.Contains(res.Stdout, "project=Project A") {
		t.Fatalf("Stdout = %q, want project argument", res.Stdout)
	}
	if !strings.Contains(res.Stdout, "artifact=app.zip") {
		t.Fatalf("Stdout = %q, want artifact argument", res.Stdout)
	}
}

func TestRunStreamWithArgsPassesShellSensitiveArgumentsLiterally(t *testing.T) {
	tempDir := t.TempDir()
	markerPath := filepath.Join(tempDir, "marker.txt")
	scriptPath := writeBatch(t, strings.Join([]string{
		"@echo off",
		"echo arg1=%~1",
		"echo arg2=%~2",
		"echo arg3=%~3",
		"",
	}, "\r\n"))

	res, err := RunStreamWithArgs(context.Background(), scriptPath, []string{
		"Project&A > " + markerPath,
		"Project%PATH%",
		"name^value",
	}, 5*time.Second, nil)
	if err != nil {
		t.Fatalf("RunStreamWithArgs returned error: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0; stderr=%q", res.ExitCode, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "arg1=Project&A > "+markerPath) {
		t.Fatalf("Stdout = %q, want literal ampersand argument", res.Stdout)
	}
	if !strings.Contains(res.Stdout, "arg2=Project%PATH%") {
		t.Fatalf("Stdout = %q, want literal percent argument", res.Stdout)
	}
	if !strings.Contains(res.Stdout, "arg3=name^value") {
		t.Fatalf("Stdout = %q, want literal caret argument", res.Stdout)
	}
	if _, err := os.Stat(markerPath); !os.IsNotExist(err) {
		t.Fatalf("marker file exists or stat failed unexpectedly: %v", err)
	}
}

func TestRunStreamWithArgsRejectsUnsafeBatchArguments(t *testing.T) {
	tempDir := t.TempDir()
	markerPath := filepath.Join(tempDir, "marker.txt")
	scriptPath := writeBatch(t, strings.Join([]string{
		"@echo off",
		"echo target-started",
		"",
	}, "\r\n"))

	tests := []struct {
		name string
		arg  string
	}{
		{
			name: "double quote injection",
			arg:  `safe" & echo injected > ` + markerPath + ` & rem "`,
		},
		{
			name: "carriage return",
			arg:  "safe\rvalue",
		},
		{
			name: "line feed",
			arg:  "safe\nvalue",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_ = os.Remove(markerPath)

			res, err := RunStreamWithArgs(context.Background(), scriptPath, []string{tt.arg}, 5*time.Second, nil)
			if !errors.Is(err, ErrInvalidScriptArgument) {
				t.Fatalf("error = %v, want ErrInvalidScriptArgument", err)
			}
			if res.ExitCode != -1 {
				t.Fatalf("ExitCode = %d, want -1", res.ExitCode)
			}
			if res.StartedAt.IsZero() {
				t.Fatal("StartedAt is zero")
			}
			if res.FinishedAt.IsZero() {
				t.Fatal("FinishedAt is zero")
			}
			if res.Stdout != "" {
				t.Fatalf("Stdout = %q, want empty", res.Stdout)
			}
			if res.Stderr != "" {
				t.Fatalf("Stderr = %q, want empty", res.Stderr)
			}
			if _, err := os.Stat(markerPath); !os.IsNotExist(err) {
				t.Fatalf("marker file exists or stat failed unexpectedly: %v", err)
			}
		})
	}
}

func TestRunStreamSerializesOutputCallbacks(t *testing.T) {
	scriptPath := writeBatch(t, strings.Join([]string{
		"@echo off",
		"for /l %%i in (1,1,200) do (",
		"  echo out %%i",
		"  echo err %%i 1>&2",
		")",
		"",
	}, "\r\n"))

	var inCallback atomic.Bool
	var reentered atomic.Bool
	res, err := RunStream(context.Background(), scriptPath, 5*time.Second, func(OutputChunk) {
		if !inCallback.CompareAndSwap(false, true) {
			reentered.Store(true)
			return
		}
		time.Sleep(time.Millisecond)
		inCallback.Store(false)
	})
	if err != nil {
		t.Fatalf("RunStream returned error: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0", res.ExitCode)
	}
	if reentered.Load() {
		t.Fatal("output callback was invoked concurrently")
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

func TestRunStreamTimeoutKeepsExitCodeMinusOne(t *testing.T) {
	scriptPath := writeBatch(t, strings.Join([]string{
		"@echo off",
		"echo before timeout",
		"ping 127.0.0.1 -n 3 >nul",
		"",
	}, "\r\n"))

	res, err := RunStream(context.Background(), scriptPath, 100*time.Millisecond, nil)
	if err != nil {
		t.Fatalf("RunStream returned error: %v", err)
	}
	if !res.TimedOut {
		t.Fatal("TimedOut = false, want true")
	}
	if res.ExitCode != -1 {
		t.Fatalf("ExitCode = %d, want -1", res.ExitCode)
	}
	if !strings.Contains(res.Stdout, "before timeout") {
		t.Fatalf("Stdout = %q, want it to contain before timeout", res.Stdout)
	}
}

func TestStreamCaptureWriterBuffersSplitUTF8Rune(t *testing.T) {
	var chunks []OutputChunk
	writer := &streamCaptureWriter{
		capture:  &cappedBuffer{limit: maxOutputBytes},
		stream:   StreamStdout,
		onOutput: func(chunk OutputChunk) { chunks = append(chunks, chunk) },
	}

	input := []byte("中")
	if _, err := writer.Write(input[:2]); err != nil {
		t.Fatalf("first Write returned error: %v", err)
	}
	if len(chunks) != 0 {
		t.Fatalf("chunks after partial rune = %#v, want none", chunks)
	}
	if _, err := writer.Write(input[2:]); err != nil {
		t.Fatalf("second Write returned error: %v", err)
	}
	writer.Flush()

	var streamed string
	for _, chunk := range chunks {
		streamed += chunk.Data
	}
	if streamed != "中" {
		t.Fatalf("streamed = %q, want 中", streamed)
	}
}

func TestStreamCaptureWriterBuffersSplitGBKRune(t *testing.T) {
	var chunks []OutputChunk
	writer := &streamCaptureWriter{
		capture:  &cappedBuffer{limit: maxOutputBytes},
		stream:   StreamStdout,
		onOutput: func(chunk OutputChunk) { chunks = append(chunks, chunk) },
	}

	input := []byte{0xD6, 0xD0} // "中" in GBK.
	if _, err := writer.Write(input[:1]); err != nil {
		t.Fatalf("first Write returned error: %v", err)
	}
	if len(chunks) != 0 {
		t.Fatalf("chunks after partial GBK rune = %#v, want none", chunks)
	}
	if _, err := writer.Write(input[1:]); err != nil {
		t.Fatalf("second Write returned error: %v", err)
	}
	writer.Flush()

	var streamed string
	for _, chunk := range chunks {
		streamed += chunk.Data
	}
	if streamed != "中" {
		t.Fatalf("streamed = %q, want 中", streamed)
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
