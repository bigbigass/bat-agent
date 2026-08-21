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
		writeScript(t, dir, name, body)
	}
	reg, err := registry.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

func writeScript(t *testing.T, dir, name, body string) string {
	t.Helper()

	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
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

func TestRunCollectWithOptionsRejectsInvalidPreDownload(t *testing.T) {
	reg := makeRegistry(t, map[string]string{"deploy.bat": "@echo off\r\necho deploy\r\n"})
	exec := New(reg, 5*time.Second)

	res, err := exec.RunCollectWithOptions(context.Background(), "deploy.bat", RunOptions{
		PreDownload: PreDownloadRequest{
			Enabled:  true,
			Project:  "..",
			Artifact: "app.zip",
		},
	})

	if !errors.Is(err, ErrInvalidPreDownloadRequest) {
		t.Fatalf("expected ErrInvalidPreDownloadRequest, got %v", err)
	}
	if res.Script != "deploy.bat" {
		t.Fatalf("Script = %q, want deploy.bat", res.Script)
	}
}

func TestRunCollectWithOptionsRejectsMissingPreDownloadConfig(t *testing.T) {
	reg := makeRegistry(t, map[string]string{"deploy.bat": "@echo off\r\necho deploy\r\n"})
	exec := New(reg, 5*time.Second)

	_, err := exec.RunCollectWithOptions(context.Background(), "deploy.bat", RunOptions{
		PreDownload: PreDownloadRequest{
			Enabled:  true,
			Project:  "ProjectA",
			Artifact: "app.zip",
		},
	})

	if !errors.Is(err, ErrPreDownloadNotConfigured) {
		t.Fatalf("expected ErrPreDownloadNotConfigured, got %v", err)
	}
}

func TestStableErrorText(t *testing.T) {
	cases := map[error]string{
		ErrInvalidScriptName:         "invalid script name",
		ErrScriptNotFound:            "script not found",
		ErrScriptBusy:                "script is already running",
		ErrRunnerStart:               "runner start failed",
		ErrScriptTimedOut:            "script timed out",
		ErrInvalidScriptArg:          "invalid script argument",
		ErrPreDownloadNotConfigured:  "pre-run download is not configured",
		ErrInvalidPreDownloadRequest: "invalid pre-run download request",
		ErrPreDownloadFailed:         "pre-run download failed",
		ErrPreDownloadTimedOut:       "pre-run download timed out",
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

func TestRunCollectWithOptionsPassesArgsToTargetScript(t *testing.T) {
	reg := makeRegistry(t, map[string]string{"deploy.bat": "@echo off\r\necho project=%~1\r\necho artifact=%~2\r\n"})
	exec := New(reg, 5*time.Second)

	res, err := exec.RunCollectWithOptions(context.Background(), "deploy.bat", RunOptions{
		Args: []string{"ProjectA", "app.zip"},
	})
	if err != nil {
		t.Fatalf("RunCollectWithOptions returned error: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0", res.ExitCode)
	}
	if !strings.Contains(res.Stdout, "project=ProjectA") {
		t.Fatalf("Stdout = %q, want project argument", res.Stdout)
	}
	if !strings.Contains(res.Stdout, "artifact=app.zip") {
		t.Fatalf("Stdout = %q, want artifact argument", res.Stdout)
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

func TestRunStreamWithOptionsRunsDownloadBeforeTarget(t *testing.T) {
	dir := t.TempDir()
	download := writeScript(t, dir, "download.bat", "@echo off\r\necho download %~1 %~2\r\n")
	writeScript(t, dir, "deploy.bat", "@echo off\r\necho target %~1 %~2\r\n")
	reg, err := registry.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	exec := New(reg, 5*time.Second, WithPreDownloadConfig(PreDownloadConfig{
		ScriptPath: download,
		Timeout:    5 * time.Second,
	}))

	res, err := exec.RunStreamWithOptions(context.Background(), "deploy.bat", RunOptions{
		Args:        []string{"ProjectA", "app.zip"},
		PreDownload: PreDownloadRequest{Enabled: true, Project: "ProjectA", Artifact: "app.zip"},
	}, nil)
	if err != nil {
		t.Fatalf("RunStreamWithOptions returned error: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0", res.ExitCode)
	}
	if !strings.Contains(res.Stdout, "download ProjectA app.zip") {
		t.Fatalf("Stdout = %q, want download output", res.Stdout)
	}
	if !strings.Contains(res.Stdout, "target ProjectA app.zip") {
		t.Fatalf("Stdout = %q, want target args output", res.Stdout)
	}
	if strings.Index(res.Stdout, "download") > strings.Index(res.Stdout, "target") {
		t.Fatalf("Stdout = %q, want download before target", res.Stdout)
	}
}

func TestRunStreamWithOptionsSkipsTargetWhenDownloadFails(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "target-ran.txt")
	download := writeScript(t, dir, "download.bat", "@echo off\r\necho download failed\r\nexit /b 5\r\n")
	writeScript(t, dir, "deploy.bat", "@echo off\r\necho target > "+marker+"\r\n")
	reg, err := registry.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	exec := New(reg, 5*time.Second, WithPreDownloadConfig(PreDownloadConfig{
		ScriptPath: download,
		Timeout:    5 * time.Second,
	}))

	res, err := exec.RunStreamWithOptions(context.Background(), "deploy.bat", RunOptions{
		PreDownload: PreDownloadRequest{Enabled: true, Project: "ProjectA", Artifact: "app.zip"},
	}, nil)

	if !errors.Is(err, ErrPreDownloadFailed) {
		t.Fatalf("expected ErrPreDownloadFailed, got %v", err)
	}
	if res.ExitCode != 5 {
		t.Fatalf("ExitCode = %d, want download exit code 5", res.ExitCode)
	}
	if !strings.Contains(res.Stdout, "download failed") {
		t.Fatalf("Stdout = %q, want download output", res.Stdout)
	}
	if _, statErr := os.Stat(marker); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("target marker stat error = %v, want not exist", statErr)
	}
}

func TestRunStreamWithOptionsReportsDownloadTimeout(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "target-ran.txt")
	download := writeScript(t, dir, "download.bat", "@echo off\r\nping -n 3 127.0.0.1 >nul\r\n")
	writeScript(t, dir, "deploy.bat", "@echo off\r\necho target > "+marker+"\r\n")
	reg, err := registry.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	exec := New(reg, 5*time.Second, WithPreDownloadConfig(PreDownloadConfig{
		ScriptPath: download,
		Timeout:    100 * time.Millisecond,
	}))

	res, err := exec.RunStreamWithOptions(context.Background(), "deploy.bat", RunOptions{
		PreDownload: PreDownloadRequest{Enabled: true, Project: "ProjectA", Artifact: "app.zip"},
	}, nil)

	if !errors.Is(err, ErrPreDownloadTimedOut) {
		t.Fatalf("expected ErrPreDownloadTimedOut, got %v", err)
	}
	if !res.TimedOut {
		t.Fatal("TimedOut = false, want true")
	}
	if _, statErr := os.Stat(marker); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("target marker stat error = %v, want not exist", statErr)
	}
}

func TestRunStreamWithOptionsKeepsTargetLockDuringDownload(t *testing.T) {
	dir := t.TempDir()
	started := filepath.Join(dir, "download-started.txt")
	download := writeScript(t, dir, "download.bat", "@echo off\r\necho started > "+started+"\r\nping -n 3 127.0.0.1 >nul\r\n")
	writeScript(t, dir, "deploy.bat", "@echo off\r\necho target\r\n")
	reg, err := registry.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	exec := New(reg, 5*time.Second, WithPreDownloadConfig(PreDownloadConfig{ScriptPath: download, Timeout: 5 * time.Second}))

	done := make(chan error, 1)
	go func() {
		_, err := exec.RunStreamWithOptions(context.Background(), "deploy.bat", RunOptions{PreDownload: PreDownloadRequest{Enabled: true, Project: "ProjectA", Artifact: "app.zip"}}, nil)
		done <- err
	}()

	deadline := time.After(3 * time.Second)
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("first run finished before download marker: %v", err)
		case <-deadline:
			t.Fatal("timed out waiting for download marker")
		case <-time.After(25 * time.Millisecond):
		}
	}

	_, err = exec.RunStreamWithOptions(context.Background(), "deploy.bat", RunOptions{PreDownload: PreDownloadRequest{Enabled: true, Project: "ProjectB", Artifact: "other.zip"}}, nil)
	if !errors.Is(err, ErrScriptBusy) {
		t.Fatalf("expected ErrScriptBusy while first run is downloading, got %v", err)
	}

	if err := <-done; err != nil {
		t.Fatalf("first run returned error: %v", err)
	}
}
