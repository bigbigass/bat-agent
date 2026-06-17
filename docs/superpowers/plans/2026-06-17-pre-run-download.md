# Pre-Run Download Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an optional GUI-controlled pre-run artifact download step before executing a selected bat/cmd script.

**Architecture:** Extend the Windows runner to pass arguments to batch files, add executor-level run options for pre-download orchestration, then expose those options through HTTP, the GUI API client, and the Fyne management console. Keep old request shapes working by adding new `WithOptions` methods instead of changing existing callers.

**Tech Stack:** Go, Windows `cmd.exe`, Fyne GUI, `net/http`, YAML config, existing Go test suite.

---

## File Structure

- Modify `internal/runner/runner.go`: add `RunStreamWithArgs` and route existing `Run`/`RunStream` through it.
- Modify `internal/runner/runner_test.go`: prove batch arguments reach `%~1` and `%~2`.
- Modify `internal/config/config.go`: add `PreRunConfig`, `PreRunDownloadConfig`, defaults, validation, and config-relative path resolution.
- Modify `internal/config/config_test.go`: cover defaults, parsing, validation, and relative path resolution.
- Modify `internal/executor/executor.go`: add run options, pre-download config, validation, orchestration, stable errors, and stdout/stderr merging.
- Modify `internal/executor/executor_test.go`: cover validation, configured download execution, failure skip, timeout, and lock coverage.
- Modify `internal/httpapi/handler.go`: decode optional `preDownload`, convert it to executor options, and map validation errors.
- Modify `internal/httpapi/handler_test.go`: cover backward compatibility, bad request validation, stream output ordering, and failed download status.
- Modify `main.go`: resolve configured download helper from `config.yaml` location and pass it into executor construction.
- Modify `main_test.go`: cover config-to-executor option mapping.
- Modify `internal/gui/apiclient/client.go`: add `RunStreamWithOptions` and request structs.
- Modify `internal/gui/apiclient/client_test.go`: cover old and new request shapes.
- Create `cmd/deploy-agent-gui/pre_download.go`: pure GUI validation and option-building helper testable without CGO/Fyne.
- Modify `cmd/deploy-agent-gui/main.go`: add checkbox and inputs, update run-button gating, send options.
- Modify `cmd/deploy-agent-gui/main_test.go`: cover pure pre-download GUI validation helper.
- Modify `config.example.yaml`: document `preRun.download`.
- Modify `README.md`: document config, GUI behavior, and HTTP request shape.

## Task 1: Runner Batch Arguments

**Files:**
- Modify: `internal/runner/runner_test.go`
- Modify: `internal/runner/runner.go`

- [ ] **Step 1: Write the failing runner argument test**

Add this test to `internal/runner/runner_test.go`:

```go
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
```

- [ ] **Step 2: Run the test to verify it fails**

Run:

```cmd
go test ./internal/runner -run TestRunStreamWithArgsPassesArguments -count=1
```

Expected: FAIL with `undefined: RunStreamWithArgs`.

- [ ] **Step 3: Implement runner argument support**

In `internal/runner/runner.go`, update the public runner functions to use a new argument-aware function:

```go
func Run(ctx context.Context, path string, timeout time.Duration) (Result, error) {
	return RunStreamWithArgs(ctx, path, nil, timeout, nil)
}

func RunStream(ctx context.Context, path string, timeout time.Duration, onOutput OutputFunc) (Result, error) {
	return RunStreamWithArgs(ctx, path, nil, timeout, onOutput)
}

func RunStreamWithArgs(ctx context.Context, path string, scriptArgs []string, timeout time.Duration, onOutput OutputFunc) (Result, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmdArgs := []string{"/c", path}
	cmdArgs = append(cmdArgs, scriptArgs...)
	cmd := exec.CommandContext(ctx, "cmd.exe", cmdArgs...)
	cmd.Dir = filepath.Dir(path)

	stdoutBuf := &cappedBuffer{limit: maxOutputBytes}
	stderrBuf := &cappedBuffer{limit: maxOutputBytes}
	emit := serializedOutputFunc(onOutput)
	stdoutWriter := &streamCaptureWriter{capture: stdoutBuf, stream: StreamStdout, onOutput: emit}
	stderrWriter := &streamCaptureWriter{capture: stderrBuf, stream: StreamStderr, onOutput: emit}
	cmd.Stdout = stdoutWriter
	cmd.Stderr = stderrWriter

	res := Result{ExitCode: -1, StartedAt: time.Now()}

	err := cmd.Start()
	if err != nil {
		res.FinishedAt = time.Now()
		return res, err
	}

	waitErr := cmd.Wait()
	stdoutWriter.Flush()
	stderrWriter.Flush()
	res.FinishedAt = time.Now()

	if ctx.Err() == context.DeadlineExceeded {
		res.TimedOut = true
		if cmd.Process != nil {
			killTree(cmd.Process.Pid)
		}
	}

	res.Stdout = decodeOutput(stdoutBuf.Bytes())
	res.Stderr = decodeOutput(stderrBuf.Bytes())

	if res.TimedOut {
		return res, nil
	}

	if waitErr != nil {
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			res.ExitCode = exitErr.ExitCode()
			return res, nil
		}
		return res, waitErr
	}
	res.ExitCode = 0
	return res, nil
}
```

- [ ] **Step 4: Run runner tests**

Run:

```cmd
go test ./internal/runner -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit runner support**

Run:

```cmd
git add internal/runner/runner.go internal/runner/runner_test.go
git commit -m "feat: pass arguments to batch runner"
```

## Task 2: Config For Pre-Run Download

**Files:**
- Modify: `internal/config/config_test.go`
- Modify: `internal/config/config.go`

- [ ] **Step 1: Write failing config tests**

Add these tests to `internal/config/config_test.go`:

```go
func TestLoadDefaultsPreRunDownloadTimeout(t *testing.T) {
	path := writeConfig(t, `
auth:
  username: admin
  password: change-me-please
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.PreRun.Download.Script != "" {
		t.Fatalf("preRun.download.script = %q, want empty", cfg.PreRun.Download.Script)
	}
	if cfg.PreRun.Download.TimeoutSeconds != 300 {
		t.Fatalf("preRun.download.timeoutSeconds = %d, want 300", cfg.PreRun.Download.TimeoutSeconds)
	}
}

func TestLoadParsesPreRunDownload(t *testing.T) {
	path := writeConfig(t, `
auth:
  username: admin
  password: change-me-please
preRun:
  download:
    script: tools/download_simple.bat
    timeoutSeconds: 120
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.PreRun.Download.Script != "tools/download_simple.bat" {
		t.Fatalf("script = %q, want configured script", cfg.PreRun.Download.Script)
	}
	if cfg.PreRun.Download.TimeoutSeconds != 120 {
		t.Fatalf("timeoutSeconds = %d, want 120", cfg.PreRun.Download.TimeoutSeconds)
	}
}

func TestLoadRejectsInvalidPreRunDownloadTimeout(t *testing.T) {
	path := writeConfig(t, `
auth:
  username: admin
  password: change-me-please
preRun:
  download:
    timeoutSeconds: 0
`)

	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "preRun.download.timeoutSeconds must be > 0") {
		t.Fatalf("expected preRun download timeout validation error, got %v", err)
	}
}

func TestResolvePreRunDownloadScriptUsesConfigDirectory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	cfg := &Config{
		PreRun: PreRunConfig{
			Download: PreRunDownloadConfig{
				Script: "tools/download_simple.bat",
			},
		},
	}

	got, err := cfg.ResolvePreRunDownloadScript(path)
	if err != nil {
		t.Fatalf("ResolvePreRunDownloadScript returned error: %v", err)
	}
	want := filepath.Join(dir, "tools", "download_simple.bat")
	if got != want {
		t.Fatalf("path = %q, want %q", got, want)
	}
}
```

- [ ] **Step 2: Run the config tests to verify they fail**

Run:

```cmd
go test ./internal/config -run "PreRunDownload|ResolvePreRun" -count=1
```

Expected: FAIL with missing `PreRun` fields and `ResolvePreRunDownloadScript`.

- [ ] **Step 3: Implement config structs, defaults, validation, and path resolution**

Update `internal/config/config.go` with these definitions and changes:

```go
type Config struct {
	Server   ServerConfig   `yaml:"server"`
	Services ServicesConfig `yaml:"services"`
	Auth     AuthConfig     `yaml:"auth"`
	Runner   RunnerConfig   `yaml:"runner"`
	PreRun   PreRunConfig   `yaml:"preRun"`
}

type PreRunConfig struct {
	Download PreRunDownloadConfig `yaml:"download"`
}

type PreRunDownloadConfig struct {
	Script         string `yaml:"script"`
	TimeoutSeconds int    `yaml:"timeoutSeconds"`
}
```

Set the default in `defaults()`:

```go
PreRun: PreRunConfig{
	Download: PreRunDownloadConfig{TimeoutSeconds: 300},
},
```

Add validation in `validate()`:

```go
if c.PreRun.Download.TimeoutSeconds <= 0 {
	return fmt.Errorf("preRun.download.timeoutSeconds must be > 0")
}
```

Add this method:

```go
func (c *Config) ResolvePreRunDownloadScript(configPath string) (string, error) {
	script := strings.TrimSpace(c.PreRun.Download.Script)
	if script == "" {
		return "", nil
	}
	if filepath.IsAbs(script) {
		return filepath.Clean(script), nil
	}
	base := filepath.Dir(configPath)
	return filepath.Abs(filepath.Join(base, script))
}
```

Add `strings` to the imports in `internal/config/config.go`.

- [ ] **Step 4: Run config tests**

Run:

```cmd
go test ./internal/config -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit config support**

Run:

```cmd
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat: add pre-run download config"
```

## Task 3: Executor Run Options And Validation

**Files:**
- Modify: `internal/executor/executor_test.go`
- Modify: `internal/executor/executor.go`

- [ ] **Step 1: Write failing executor validation tests**

Add these tests to `internal/executor/executor_test.go`:

```go
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
```

Extend `TestStableErrorText` with:

```go
ErrPreDownloadNotConfigured:   "pre-run download is not configured",
ErrInvalidPreDownloadRequest:  "invalid pre-run download request",
ErrPreDownloadFailed:          "pre-run download failed",
ErrPreDownloadTimedOut:        "pre-run download timed out",
```

- [ ] **Step 2: Run executor tests to verify they fail**

Run:

```cmd
go test ./internal/executor -run "PreDownload|StableError" -count=1
```

Expected: FAIL with undefined pre-download types and errors.

- [ ] **Step 3: Add executor option types and validation**

Add these declarations to `internal/executor/executor.go`:

```go
var (
	ErrPreDownloadNotConfigured  = errors.New("pre-run download is not configured")
	ErrInvalidPreDownloadRequest = errors.New("invalid pre-run download request")
	ErrPreDownloadFailed         = errors.New("pre-run download failed")
	ErrPreDownloadTimedOut       = errors.New("pre-run download timed out")
)

type RunOptions struct {
	PreDownload PreDownloadRequest
}

type PreDownloadRequest struct {
	Enabled  bool
	Project  string
	Artifact string
}

type PreDownloadConfig struct {
	ScriptPath string
	Timeout    time.Duration
}

type Option func(*Executor)
```

Update `Executor` and `New`:

```go
type Executor struct {
	reg         *registry.Registry
	timeout     time.Duration
	preDownload PreDownloadConfig
}

func New(reg *registry.Registry, timeout time.Duration, opts ...Option) *Executor {
	e := &Executor{reg: reg, timeout: timeout}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

func WithPreDownloadConfig(cfg PreDownloadConfig) Option {
	return func(e *Executor) {
		e.preDownload = cfg
	}
}
```

Add option-aware wrappers:

```go
func (e *Executor) RunCollect(ctx context.Context, script string) (Result, error) {
	return e.RunCollectWithOptions(ctx, script, RunOptions{})
}

func (e *Executor) RunCollectWithOptions(ctx context.Context, script string, opts RunOptions) (Result, error) {
	return e.RunStreamWithOptions(ctx, script, opts, nil)
}

func (e *Executor) RunStream(ctx context.Context, script string, onOutput OutputFunc) (Result, error) {
	return e.RunStreamWithOptions(ctx, script, RunOptions{}, onOutput)
}
```

Add validation helpers:

```go
func validatePreDownload(req PreDownloadRequest) (PreDownloadRequest, error) {
	if !req.Enabled {
		return PreDownloadRequest{}, nil
	}
	req.Project = strings.TrimSpace(req.Project)
	req.Artifact = strings.TrimSpace(req.Artifact)
	if req.Project == "" || req.Artifact == "" {
		return req, ErrInvalidPreDownloadRequest
	}
	if unsafeDownloadValue(req.Project) || unsafeDownloadValue(req.Artifact) {
		return req, ErrInvalidPreDownloadRequest
	}
	return req, nil
}

func unsafeDownloadValue(value string) bool {
	return strings.ContainsAny(value, `/\:&|<>^%"!`) || strings.Contains(value, "..")
}
```

Add `strings` to the imports.

Update `StableError` to return the new stable strings.

- [ ] **Step 4: Implement a minimal `RunStreamWithOptions` that passes validation tests**

Replace the old `RunStream` body with this option-aware structure:

```go
func (e *Executor) RunStreamWithOptions(ctx context.Context, script string, opts RunOptions, onOutput OutputFunc) (Result, error) {
	entry, err := e.reg.Lookup(script)
	if err != nil {
		return lookupErrorResult(script, err)
	}

	preDownload, err := validatePreDownload(opts.PreDownload)
	if err != nil {
		return Result{Script: entry.Name, ExitCode: -1}, err
	}
	if preDownload.Enabled && strings.TrimSpace(e.preDownload.ScriptPath) == "" {
		return Result{Script: entry.Name, ExitCode: -1}, ErrPreDownloadNotConfigured
	}

	if !entry.TryLock() {
		return Result{Script: entry.Name, ExitCode: -1}, ErrScriptBusy
	}
	defer entry.Unlock()

	res, err := runner.RunStream(ctx, entry.Path, e.timeout, adaptOutputFunc(onOutput))
	out := resultFromRunner(entry.Name, res)
	if res.TimedOut {
		return out, ErrScriptTimedOut
	}
	if err != nil {
		return out, runnerStartError{err: err}
	}
	return out, nil
}

func resultFromRunner(script string, res runner.Result) Result {
	return Result{
		Script:     script,
		ExitCode:   res.ExitCode,
		Stdout:     res.Stdout,
		Stderr:     res.Stderr,
		StartedAt:  res.StartedAt,
		FinishedAt: res.FinishedAt,
		TimedOut:   res.TimedOut,
	}
}
```

- [ ] **Step 5: Run executor validation tests**

Run:

```cmd
go test ./internal/executor -run "PreDownload|StableError" -count=1
```

Expected: PASS.

## Task 4: Executor Pre-Download Orchestration

**Files:**
- Modify: `internal/executor/executor_test.go`
- Modify: `internal/executor/executor.go`

- [ ] **Step 1: Write failing orchestration tests**

Add this helper to `internal/executor/executor_test.go`:

```go
func writeScript(t *testing.T, dir, name, body string) string {
	t.Helper()

	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
```

Add these tests:

```go
func TestRunStreamWithOptionsRunsDownloadBeforeTarget(t *testing.T) {
	dir := t.TempDir()
	download := writeScript(t, dir, "download.bat", "@echo off\r\necho download %~1 %~2\r\n")
	writeScript(t, dir, "deploy.bat", "@echo off\r\necho target\r\n")
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
	if err != nil {
		t.Fatalf("RunStreamWithOptions returned error: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0", res.ExitCode)
	}
	if !strings.Contains(res.Stdout, "download ProjectA app.zip") {
		t.Fatalf("Stdout = %q, want download output", res.Stdout)
	}
	if !strings.Contains(res.Stdout, "target") {
		t.Fatalf("Stdout = %q, want target output", res.Stdout)
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
	download := writeScript(t, dir, "download.bat", "@echo off\r\necho before timeout\r\nping -n 3 127.0.0.1 >nul\r\n")
	writeScript(t, dir, "deploy.bat", "@echo off\r\necho target\r\n")
	reg, err := registry.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	exec := New(reg, 5*time.Second, WithPreDownloadConfig(PreDownloadConfig{
		ScriptPath: download,
		Timeout:    20 * time.Millisecond,
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
	if !strings.Contains(res.Stdout, "before timeout") {
		t.Fatalf("Stdout = %q, want download timeout output", res.Stdout)
	}
}
```

- [ ] **Step 2: Run orchestration tests to verify they fail**

Run:

```cmd
go test ./internal/executor -run "RunsDownload|SkipsTarget|DownloadTimeout" -count=1
```

Expected: FAIL because the target script runs without the download step.

- [ ] **Step 3: Implement download execution and result merging**

Add these helpers to `internal/executor/executor.go`:

```go
func (e *Executor) runPreDownload(ctx context.Context, script string, req PreDownloadRequest, onOutput OutputFunc) (Result, error) {
	res, err := runner.RunStreamWithArgs(ctx, e.preDownload.ScriptPath, []string{req.Project, req.Artifact}, e.preDownload.Timeout, adaptOutputFunc(onOutput))
	out := resultFromRunner(script, res)
	if res.TimedOut {
		return out, ErrPreDownloadTimedOut
	}
	if err != nil {
		return out, preDownloadStartError{err: err}
	}
	if res.ExitCode != 0 {
		return out, ErrPreDownloadFailed
	}
	return out, nil
}

func mergeResults(script string, pre Result, target Result) Result {
	return Result{
		Script:     script,
		ExitCode:   target.ExitCode,
		Stdout:     pre.Stdout + target.Stdout,
		Stderr:     pre.Stderr + target.Stderr,
		StartedAt:  firstTime(pre.StartedAt, target.StartedAt),
		FinishedAt: target.FinishedAt,
		TimedOut:   target.TimedOut,
	}
}

func firstTime(a, b time.Time) time.Time {
	if !a.IsZero() {
		return a
	}
	return b
}

type preDownloadStartError struct {
	err error
}

func (e preDownloadStartError) Error() string {
	return ErrPreDownloadFailed.Error() + ": " + e.err.Error()
}

func (e preDownloadStartError) Unwrap() error {
	return e.err
}

func (e preDownloadStartError) Is(target error) bool {
	return target == ErrPreDownloadFailed
}
```

In `RunStreamWithOptions`, after acquiring the target lock and before running the target script, insert:

```go
var preResult Result
if preDownload.Enabled {
	preResult, err = e.runPreDownload(ctx, entry.Name, preDownload, onOutput)
	if err != nil {
		return preResult, err
	}
}
```

After target execution succeeds, return merged output when pre-download was enabled:

```go
out := resultFromRunner(entry.Name, res)
if preDownload.Enabled {
	out = mergeResults(entry.Name, preResult, out)
}
```

- [ ] **Step 4: Run executor tests**

Run:

```cmd
go test ./internal/executor -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit executor orchestration**

Run:

```cmd
git add internal/executor/executor.go internal/executor/executor_test.go
git commit -m "feat: run optional pre-download before scripts"
```

## Task 5: HTTP API Request Support

**Files:**
- Modify: `internal/httpapi/handler_test.go`
- Modify: `internal/httpapi/handler.go`

- [ ] **Step 1: Write failing HTTP tests**

Add this helper to `internal/httpapi/handler_test.go`:

```go
func postRunBody(body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/run", bytes.NewBufferString(body))
	req.Header.Set("Authorization", authHeader())
	req.Header.Set("Content-Type", "application/json")
	return req
}
```

Add these tests:

```go
func TestRunRejectsInvalidPreDownloadRequest(t *testing.T) {
	server := makeServer(t, map[string]string{"deploy.bat": "@echo off\r\necho deploy\r\n"})
	rec := httptest.NewRecorder()

	server.handler.ServeHTTP(rec, postRunBody(`{"script":"deploy.bat","preDownload":{"enabled":true,"project":"..","artifact":"app.zip"}}`))

	assertErrorResponse(t, rec, http.StatusBadRequest, "invalid pre-run download request")
}

func TestRunStreamWithPreDownloadStreamsDownloadBeforeTarget(t *testing.T) {
	dir := t.TempDir()
	download := filepath.Join(dir, "download.bat")
	if err := os.WriteFile(download, []byte("@echo off\r\necho download %~1 %~2\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "deploy.bat"), []byte("@echo off\r\necho target\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	reg, err := registry.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	exec := executor.New(reg, 5*time.Second, executor.WithPreDownloadConfig(executor.PreDownloadConfig{
		ScriptPath: download,
		Timeout:    5 * time.Second,
	}))
	api := New(exec)
	handler := api.Routes(func(h http.Handler) http.Handler {
		return auth.BasicAuth("admin", "change-me-please", h)
	})
	req := httptest.NewRequest(http.MethodPost, "/run/stream", bytes.NewBufferString(`{"script":"deploy.bat","preDownload":{"enabled":true,"project":"ProjectA","artifact":"app.zip"}}`))
	req.Header.Set("Authorization", authHeader())
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "download ProjectA app.zip") {
		t.Fatalf("body = %s, want download output", body)
	}
	if !strings.Contains(body, "target") {
		t.Fatalf("body = %s, want target output", body)
	}
	if strings.Index(body, "download") > strings.Index(body, "target") {
		t.Fatalf("body = %s, want download output before target output", body)
	}
}
```

- [ ] **Step 2: Run HTTP tests to verify they fail**

Run:

```cmd
go test ./internal/httpapi -run "PreDownload" -count=1
```

Expected: FAIL because `preDownload` is ignored and invalid pre-download is not mapped to 400.

- [ ] **Step 3: Decode HTTP `preDownload` and pass executor options**

Update request types in `internal/httpapi/handler.go`:

```go
type runRequest struct {
	Script      string              `json:"script"`
	PreDownload *preDownloadRequest `json:"preDownload,omitempty"`
}

type preDownloadRequest struct {
	Enabled  bool   `json:"enabled"`
	Project  string `json:"project"`
	Artifact string `json:"artifact"`
}

func runOptionsFromRequest(req runRequest) executor.RunOptions {
	if req.PreDownload == nil {
		return executor.RunOptions{}
	}
	return executor.RunOptions{
		PreDownload: executor.PreDownloadRequest{
			Enabled:  req.PreDownload.Enabled,
			Project:  req.PreDownload.Project,
			Artifact: req.PreDownload.Artifact,
		},
	}
}
```

In `handleRun`, call:

```go
result, err := s.exec.RunCollectWithOptions(context.Background(), req.Script, runOptionsFromRequest(req))
```

In `handleRunStream`, call:

```go
result, err := s.exec.RunStreamWithOptions(context.Background(), req.Script, runOptionsFromRequest(req), func(chunk executor.OutputChunk) {
	outputs <- streamOutputResponse{
		Type:   "output",
		Script: req.Script,
		Stream: chunk.Stream,
		Data:   chunk.Data,
	}
})
```

Map invalid pre-download request in both normal and stream preflight error handling:

```go
case errors.Is(err, executor.ErrInvalidPreDownloadRequest):
	writeJSON(w, http.StatusBadRequest, map[string]string{"error": executor.StableError(err)})
	return
```

- [ ] **Step 4: Run HTTP tests**

Run:

```cmd
go test ./internal/httpapi -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit HTTP API support**

Run:

```cmd
git add internal/httpapi/handler.go internal/httpapi/handler_test.go
git commit -m "feat: accept pre-download run requests"
```

## Task 6: Wire Config Into Service Startup

**Files:**
- Modify: `main_test.go`
- Modify: `main.go`

- [ ] **Step 1: Write failing startup mapping tests**

Add this test to `main_test.go`:

```go
func TestExecutorOptionsFromConfigMapsPreRunDownload(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	cfg := &config.Config{
		PreRun: config.PreRunConfig{
			Download: config.PreRunDownloadConfig{
				Script:         "tools/download_simple.bat",
				TimeoutSeconds: 123,
			},
		},
	}

	opts, err := executorOptionsFromConfig(cfg, cfgPath)
	if err != nil {
		t.Fatalf("executorOptionsFromConfig returned error: %v", err)
	}
	if len(opts) != 1 {
		t.Fatalf("options len = %d, want 1", len(opts))
	}
}
```

Add imports for `path/filepath` and update the existing import block.

- [ ] **Step 2: Run the startup mapping test to verify it fails**

Run:

```cmd
go test . -run TestExecutorOptionsFromConfigMapsPreRunDownload -count=1
```

Expected: FAIL with `undefined: executorOptionsFromConfig`.

- [ ] **Step 3: Add startup option mapping**

In `main.go`, add:

```go
func executorOptionsFromConfig(cfg *config.Config, cfgPath string) ([]executor.Option, error) {
	downloadScript, err := cfg.ResolvePreRunDownloadScript(cfgPath)
	if err != nil {
		return nil, fmt.Errorf("resolve preRun.download.script: %w", err)
	}
	if downloadScript == "" {
		return nil, nil
	}
	return []executor.Option{
		executor.WithPreDownloadConfig(executor.PreDownloadConfig{
			ScriptPath: downloadScript,
			Timeout:    time.Duration(cfg.PreRun.Download.TimeoutSeconds) * time.Second,
		}),
	}, nil
}
```

In `run()`, replace executor construction with:

```go
execOptions, err := executorOptionsFromConfig(cfg, cfgPath)
if err != nil {
	return err
}
exec := executor.New(reg, timeout, execOptions...)
```

- [ ] **Step 4: Run package tests**

Run:

```cmd
go test . -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit startup wiring**

Run:

```cmd
git add main.go main_test.go
git commit -m "feat: wire pre-download config into executor"
```

## Task 7: GUI API Client Request Options

**Files:**
- Modify: `internal/gui/apiclient/client_test.go`
- Modify: `internal/gui/apiclient/client.go`

- [ ] **Step 1: Write failing API client request-shape test**

Add this test to `internal/gui/apiclient/client_test.go`:

```go
func TestRunStreamWithOptionsSendsPreDownload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("ReadAll body returned error: %v", err)
		}
		var body struct {
			Script      string `json:"script"`
			PreDownload struct {
				Enabled  bool   `json:"enabled"`
				Project  string `json:"project"`
				Artifact string `json:"artifact"`
			} `json:"preDownload"`
		}
		if err := json.Unmarshal(data, &body); err != nil {
			t.Fatalf("request body is not JSON: %v", err)
		}
		if body.Script != "deploy.bat" {
			t.Fatalf("script = %q, want deploy.bat", body.Script)
		}
		if !body.PreDownload.Enabled || body.PreDownload.Project != "ProjectA" || body.PreDownload.Artifact != "app.zip" {
			t.Fatalf("preDownload = %#v", body.PreDownload)
		}
		w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
		fmt.Fprintln(w, `{"type":"final","script":"deploy.bat","exitCode":0,"timedOut":false}`)
	}))
	defer server.Close()

	client := New(server.URL, "admin", "password")
	err := client.RunStreamWithOptions(context.Background(), "deploy.bat", RunStreamOptions{
		PreDownload: PreDownloadOptions{Enabled: true, Project: "ProjectA", Artifact: "app.zip"},
	}, nil)
	if err != nil {
		t.Fatalf("RunStreamWithOptions returned error: %v", err)
	}
}
```

- [ ] **Step 2: Run API client tests to verify they fail**

Run:

```cmd
go test ./internal/gui/apiclient -run "RunStreamWithOptions|RunStreamReadsOutput" -count=1
```

Expected: FAIL with undefined `RunStreamWithOptions`.

- [ ] **Step 3: Implement API client options while preserving old method**

Add request option types to `internal/gui/apiclient/client.go`:

```go
type RunStreamOptions struct {
	PreDownload PreDownloadOptions
}

type PreDownloadOptions struct {
	Enabled  bool   `json:"enabled"`
	Project  string `json:"project"`
	Artifact string `json:"artifact"`
}

type runStreamRequest struct {
	Script      string              `json:"script"`
	PreDownload *PreDownloadOptions `json:"preDownload,omitempty"`
}
```

Change `RunStream` and add `RunStreamWithOptions`:

```go
func (c *Client) RunStream(ctx context.Context, script string, onEvent func(StreamEvent)) error {
	return c.RunStreamWithOptions(ctx, script, RunStreamOptions{}, onEvent)
}

func (c *Client) RunStreamWithOptions(ctx context.Context, script string, opts RunStreamOptions, onEvent func(StreamEvent)) error {
	reqBody := runStreamRequest{Script: script}
	if opts.PreDownload.Enabled {
		preDownload := opts.PreDownload
		reqBody.PreDownload = &preDownload
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/run/stream", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	c.setAuth(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return decodeHTTPError(resp)
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)
	sawFinal := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var event StreamEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			return err
		}
		if event.Type == EventFinal {
			sawFinal = true
		}
		if onEvent != nil {
			onEvent(event)
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if !sawFinal {
		return fmt.Errorf("stream ended before final event")
	}
	return nil
}
```

- [ ] **Step 4: Run API client tests**

Run:

```cmd
go test ./internal/gui/apiclient -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit API client support**

Run:

```cmd
git add internal/gui/apiclient/client.go internal/gui/apiclient/client_test.go
git commit -m "feat: send pre-download options from gui client"
```

## Task 8: GUI Validation And Controls

**Files:**
- Create: `cmd/deploy-agent-gui/pre_download.go`
- Modify: `cmd/deploy-agent-gui/main_test.go`
- Modify: `cmd/deploy-agent-gui/main.go`

- [ ] **Step 1: Write failing pure GUI validation tests**

Add these tests to `cmd/deploy-agent-gui/main_test.go`:

```go
func TestPreDownloadOptionsDisabledReturnsEmptyOptions(t *testing.T) {
	opts, err := preDownloadOptions(false, "", "")
	if err != nil {
		t.Fatalf("preDownloadOptions returned error: %v", err)
	}
	if opts.PreDownload.Enabled {
		t.Fatalf("PreDownload.Enabled = true, want false")
	}
}

func TestPreDownloadOptionsRequiresProjectAndArtifact(t *testing.T) {
	_, err := preDownloadOptions(true, "ProjectA", "")
	if err == nil {
		t.Fatal("preDownloadOptions returned nil error, want missing artifact error")
	}
	if !strings.Contains(err.Error(), "产物文件名") {
		t.Fatalf("error = %q, want artifact message", err.Error())
	}
}

func TestPreDownloadOptionsBuildsEnabledRequest(t *testing.T) {
	opts, err := preDownloadOptions(true, " ProjectA ", " app.zip ")
	if err != nil {
		t.Fatalf("preDownloadOptions returned error: %v", err)
	}
	if !opts.PreDownload.Enabled {
		t.Fatal("PreDownload.Enabled = false, want true")
	}
	if opts.PreDownload.Project != "ProjectA" {
		t.Fatalf("Project = %q, want ProjectA", opts.PreDownload.Project)
	}
	if opts.PreDownload.Artifact != "app.zip" {
		t.Fatalf("Artifact = %q, want app.zip", opts.PreDownload.Artifact)
	}
}
```

- [ ] **Step 2: Run GUI package tests to verify they fail**

Run:

```cmd
go test ./cmd/deploy-agent-gui -run PreDownloadOptions -count=1
```

Expected: FAIL with `undefined: preDownloadOptions`.

- [ ] **Step 3: Implement pure GUI option helper**

Create `cmd/deploy-agent-gui/pre_download.go`:

```go
package main

import (
	"fmt"
	"strings"

	"github.com/liqixin/deploy-agent/internal/gui/apiclient"
)

func preDownloadOptions(enabled bool, project string, artifact string) (apiclient.RunStreamOptions, error) {
	if !enabled {
		return apiclient.RunStreamOptions{}, nil
	}
	project = strings.TrimSpace(project)
	artifact = strings.TrimSpace(artifact)
	if project == "" {
		return apiclient.RunStreamOptions{}, fmt.Errorf("请填写项目编号")
	}
	if artifact == "" {
		return apiclient.RunStreamOptions{}, fmt.Errorf("请填写产物文件名")
	}
	return apiclient.RunStreamOptions{
		PreDownload: apiclient.PreDownloadOptions{
			Enabled:  true,
			Project:  project,
			Artifact: artifact,
		},
	}, nil
}

func preDownloadInputsReady(enabled bool, project string, artifact string) bool {
	if !enabled {
		return true
	}
	return strings.TrimSpace(project) != "" && strings.TrimSpace(artifact) != ""
}
```

- [ ] **Step 4: Update Fyne GUI state and run flow**

In `cmd/deploy-agent-gui/main.go`, add fields to `guiState`:

```go
preDownloadCheck   *widget.Check
projectEntry       *widget.Entry
artifactEntry      *widget.Entry
```

In `buildUI()`, create the controls before `s.scriptSelect`:

```go
s.preDownloadCheck = widget.NewCheck("执行前下载", func(bool) {
	s.updatePreDownloadInputs()
	s.updateRunButton()
})
s.projectEntry = widget.NewEntry()
s.projectEntry.SetPlaceHolder("项目编号")
s.projectEntry.OnChanged = func(string) {
	s.updateRunButton()
}
s.artifactEntry = widget.NewEntry()
s.artifactEntry.SetPlaceHolder("产物文件名")
s.artifactEntry.OnChanged = func(string) {
	s.updateRunButton()
}
s.updatePreDownloadInputs()
```

Add the controls to the existing form:

```go
widget.NewFormItem("执行前下载", s.preDownloadCheck),
widget.NewFormItem("项目编号", s.projectEntry),
widget.NewFormItem("产物文件名", s.artifactEntry),
```

Add this method:

```go
func (s *guiState) updatePreDownloadInputs() {
	if s.preDownloadCheck == nil || s.projectEntry == nil || s.artifactEntry == nil {
		return
	}
	if s.preDownloadCheck.Checked {
		s.projectEntry.Enable()
		s.artifactEntry.Enable()
		return
	}
	s.projectEntry.Disable()
	s.artifactEntry.Disable()
}
```

Update `updateRunButton()` so the run button requires complete pre-download inputs:

```go
ready := true
if s.preDownloadCheck != nil && s.projectEntry != nil && s.artifactEntry != nil {
	ready = preDownloadInputsReady(s.preDownloadCheck.Checked, s.projectEntry.Text, s.artifactEntry.Text)
}
if s.client != nil && !s.running && s.scriptSelect.Selected != "" && ready {
	s.runButton.Enable()
	return
}
```

In `runSelectedScript()`, build options before setting `running=true`:

```go
opts, err := preDownloadOptions(s.preDownloadCheck != nil && s.preDownloadCheck.Checked, entryText(s.projectEntry), entryText(s.artifactEntry))
if err != nil {
	s.setStatus(err.Error())
	return
}
```

Call the new client method:

```go
err := client.RunStreamWithOptions(context.Background(), script, opts, func(event apiclient.StreamEvent) {
	fyne.Do(func() {
		s.handleEvent(event)
	})
})
```

Add this helper:

```go
func entryText(entry *widget.Entry) string {
	if entry == nil {
		return ""
	}
	return entry.Text
}
```

- [ ] **Step 5: Run GUI package tests**

Run:

```cmd
go test ./cmd/deploy-agent-gui -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit GUI support**

Run:

```cmd
git add cmd/deploy-agent-gui/pre_download.go cmd/deploy-agent-gui/main.go cmd/deploy-agent-gui/main_test.go
git commit -m "feat: add gui pre-download controls"
```

## Task 9: Documentation And Example Config

**Files:**
- Modify: `config.example.yaml`
- Modify: `README.md`

- [ ] **Step 1: Update example config**

Add this section to `config.example.yaml` after `runner`:

```yaml
preRun:
  download:
    # 执行脚本前的下载工具；留空 = 禁用前置下载
    # 相对路径基于 config.yaml 所在目录解析
    script: "tools/download_simple.bat"
    # 下载步骤最大执行时间（秒）
    timeoutSeconds: 300
```

- [ ] **Step 2: Update README config docs**

In `README.md`, add `preRun.download` to the config sample and explain:

```markdown
### 前置下载

GUI 可以在执行脚本前勾选“执行前下载”。勾选后需要填写项目编号和产物文件名，服务端会先执行 `preRun.download.script`：

```cmd
download_simple.bat <项目编号> <产物文件名>
```

下载脚本按固定远端路径获取产物：

```text
/交付产物/<项目编号>/<产物文件名>
```

下载成功后才会执行目标脚本；下载失败或超时时不会执行目标脚本。`preRun.download.script` 相对路径基于 `config.yaml` 所在目录解析。`tools/cookie.ini` 是本机凭据文件，不要提交或分享。
```

- [ ] **Step 3: Update HTTP docs**

In the `/run` and `/run/stream` request sections, add this optional request shape:

```json
{
  "script": "deploy.bat",
  "preDownload": {
    "enabled": true,
    "project": "ProjectA",
    "artifact": "app.zip"
  }
}
```

Document that omitting `preDownload` keeps old behavior.

- [ ] **Step 4: Run docs-neutral checks**

Run:

```cmd
git diff --check
```

Expected: no whitespace errors.

- [ ] **Step 5: Commit docs**

Run:

```cmd
git add config.example.yaml README.md
git commit -m "docs: document pre-run download"
```

## Task 10: Full Verification

**Files:**
- All modified Go and documentation files

- [ ] **Step 1: Format Go files**

Run:

```cmd
gofmt -w .
```

Expected: command exits 0.

- [ ] **Step 2: Run full tests**

Run:

```cmd
go test ./...
```

Expected: PASS.

- [ ] **Step 3: Run vet**

Run:

```cmd
go vet ./...
```

Expected: PASS.

- [ ] **Step 4: Build Windows executables**

Run:

```cmd
build.bat
```

Expected: exits 0 and produces `deploy-agent.exe` and `deploy-agent-gui.exe`.

- [ ] **Step 5: Check git status**

Run:

```cmd
git status --short
```

Expected: only intentional source/doc changes are tracked or committed. `tools/` and local credential/config files remain uncommitted unless the user explicitly asks to include sanitized examples.

## Self-Review

Spec coverage:

- GUI checkbox and project/artifact inputs: Task 8.
- HTTP optional `preDownload`: Task 5.
- Configured helper script and timeout: Tasks 2 and 6.
- Executor-level orchestration shared by callers: Tasks 3 and 4.
- Download failure skips target script: Task 4.
- Old clients remain compatible: Tasks 5 and 7.
- Documentation and config sample: Task 9.
- Windows verification: Task 10.

Placeholder scan:

- No unresolved placeholder markers.
- No deferred-work markers.
- No unexpanded "implement error handling" step.
- All commands include expected outcomes.

Type consistency:

- HTTP request type `preDownload` maps to `executor.PreDownloadRequest`.
- GUI client option `apiclient.PreDownloadOptions` maps to the same JSON fields.
- Executor option `WithPreDownloadConfig` is used by `main.go` only after config path resolution.
