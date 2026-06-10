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
