# MQTT Script Scheduling Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 为 `deploy-agent` 增加可配置的 MQTT 脚本调度入口，支持实时 stdout/stderr 进度消息，并保持现有 HTTP API 行为不变。

**Architecture:** 先抽出 `internal/executor` 统一承载白名单、锁、超时和 runner 调用规则，再让 HTTP 与 MQTT 分别通过 collect 和 stream 两种方式复用它。MQTT 入口放在 `internal/mqttapi`，只负责 broker 连接、命令解析、进度发布和最终 `done: true` 消息。

**Tech Stack:** Go 1.19+、标准库 `net/http` / `os/exec` / `encoding/json`、`gopkg.in/yaml.v3`、`golang.org/x/text`、`github.com/eclipse/paho.mqtt.golang`。

---

## Reference Spec

实现必须遵守规格文件：

```text
D:\Code\bat-agent\docs\superpowers\specs\2026-06-10-mqtt-script-scheduling-design.md
```

实现期间不得改变以下既有行为：

```text
/health 不鉴权
/scripts 和 /run 继续 Basic Auth
HTTP /run 响应结构保持稳定
脚本 exitCode 非 0 时 HTTP 仍返回 200
同名脚本不能并发，不同脚本可以并发
runner 输出每个 stream 最多保留 1MiB
runner 继续 UTF-8 passthrough 和 GBK fallback
runner 超时后继续 taskkill /F /T /PID 清理进程树
```

## File Structure

### New Files

```text
D:\Code\bat-agent\internal\executor\executor.go
D:\Code\bat-agent\internal\executor\executor_test.go
D:\Code\bat-agent\internal\mqttapi\protocol.go
D:\Code\bat-agent\internal\mqttapi\protocol_test.go
D:\Code\bat-agent\internal\mqttapi\client.go
D:\Code\bat-agent\internal\mqttapi\client_test.go
```

### Modified Files

```text
D:\Code\bat-agent\go.mod
D:\Code\bat-agent\go.sum
D:\Code\bat-agent\main.go
D:\Code\bat-agent\README.md
D:\Code\bat-agent\config.example.yaml
D:\Code\bat-agent\internal\config\config.go
D:\Code\bat-agent\internal\config\config_test.go
D:\Code\bat-agent\internal\httpapi\handler.go
D:\Code\bat-agent\internal\httpapi\handler_test.go
D:\Code\bat-agent\internal\runner\runner.go
```

### Responsibility Map

```text
internal/config
  解析 services.http 和 services.mqtt 配置，提供默认值和校验。

internal/runner
  只负责执行脚本、超时、进程树清理、输出捕获和实时输出回调。

internal/executor
  统一负责 registry 查找、脚本锁、runner 调用、稳定错误文本和 collect/stream 两种执行模式。

internal/httpapi
  保持 HTTP 协议不变，改为通过 executor.RunCollect 执行脚本。

internal/mqttapi
  负责 MQTT 命令协议、broker client、订阅命令 topic、发布进度和最终消息。

main.go
  根据配置启动 HTTP server 和 MQTT client，统一响应进程退出信号。
```

---

### Task 1: Extend Config With Service Toggles And MQTT Options

**Files:**

```text
Modify: D:\Code\bat-agent\internal\config\config.go
Create: D:\Code\bat-agent\internal\config\config_test.go
Modify: D:\Code\bat-agent\config.example.yaml
```

- [ ] **Step 1: Write failing tests for defaults and validation**

Create `D:\Code\bat-agent\internal\config\config_test.go` with tests that prove the new config contract:

```go
package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadDefaultsServicesToHTTPEnabledMQTTDisabled(t *testing.T) {
	path := writeConfig(t, `
auth:
  username: admin
  password: change-me-please
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if !cfg.Services.HTTP.Enabled {
		t.Fatalf("HTTP should default to enabled")
	}
	if cfg.Services.MQTT.Enabled {
		t.Fatalf("MQTT should default to disabled")
	}
	if cfg.Services.MQTT.Broker != "tcp://127.0.0.1:1883" {
		t.Fatalf("unexpected broker default: %q", cfg.Services.MQTT.Broker)
	}
	if cfg.Services.MQTT.ClientID != "deploy-agent" {
		t.Fatalf("unexpected clientId default: %q", cfg.Services.MQTT.ClientID)
	}
	if cfg.Services.MQTT.CommandTopic != "deploy-agent/run" {
		t.Fatalf("unexpected commandTopic default: %q", cfg.Services.MQTT.CommandTopic)
	}
	if cfg.Services.MQTT.QoS != 1 {
		t.Fatalf("unexpected qos default: %d", cfg.Services.MQTT.QoS)
	}
}

func TestLoadRejectsWhenHTTPAndMQTTAreDisabled(t *testing.T) {
	path := writeConfig(t, `
services:
  http:
    enabled: false
  mqtt:
    enabled: false
auth:
  username: admin
  password: change-me-please
`)

	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "at least one service must be enabled") {
		t.Fatalf("expected service validation error, got %v", err)
	}
}

func TestLoadAllowsMQTTOnlyWithoutHTTPAuth(t *testing.T) {
	path := writeConfig(t, `
services:
  http:
    enabled: false
  mqtt:
    enabled: true
    broker: "tcp://127.0.0.1:1883"
    commandTopic: "deploy-agent/run"
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.Services.HTTP.Enabled {
		t.Fatalf("HTTP should be disabled")
	}
	if !cfg.Services.MQTT.Enabled {
		t.Fatalf("MQTT should be enabled")
	}
}

func TestLoadRejectsEnabledMQTTWithoutCommandTopic(t *testing.T) {
	path := writeConfig(t, `
services:
  mqtt:
    enabled: true
    commandTopic: ""
auth:
  username: admin
  password: change-me-please
`)

	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "services.mqtt.commandTopic is empty") {
		t.Fatalf("expected commandTopic validation error, got %v", err)
	}
}

func TestLoadRejectsInvalidMQTTQoS(t *testing.T) {
	path := writeConfig(t, `
services:
  mqtt:
    enabled: true
    qos: 3
auth:
  username: admin
  password: change-me-please
`)

	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "services.mqtt.qos must be 0, 1, or 2") {
		t.Fatalf("expected qos validation error, got %v", err)
	}
}

func TestLoadAcceptsMQTTTLSBroker(t *testing.T) {
	path := writeConfig(t, `
services:
  mqtt:
    enabled: true
    broker: "ssl://mqtt.example.com:8883"
auth:
  username: admin
  password: change-me-please
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.Services.MQTT.Broker != "ssl://mqtt.example.com:8883" {
		t.Fatalf("unexpected broker: %q", cfg.Services.MQTT.Broker)
	}
}
```

- [ ] **Step 2: Run config tests and verify they fail**

Run:

```cmd
go test ./internal/config
```

Expected: failure because `Config.Services` and the MQTT config structs do not exist.

- [ ] **Step 3: Implement config structs, defaults, and validation**

Modify `D:\Code\bat-agent\internal\config\config.go` with these additions:

```go
type Config struct {
	Server   ServerConfig   `yaml:"server"`
	Auth     AuthConfig     `yaml:"auth"`
	Runner   RunnerConfig   `yaml:"runner"`
	Services ServicesConfig `yaml:"services"`
}

type ServicesConfig struct {
	HTTP HTTPServiceConfig `yaml:"http"`
	MQTT MQTTServiceConfig `yaml:"mqtt"`
}

type HTTPServiceConfig struct {
	Enabled bool `yaml:"enabled"`
}

type MQTTServiceConfig struct {
	Enabled      bool   `yaml:"enabled"`
	Broker       string `yaml:"broker"`
	ClientID     string `yaml:"clientId"`
	Username     string `yaml:"username"`
	Password     string `yaml:"password"`
	CommandTopic string `yaml:"commandTopic"`
	QoS          int    `yaml:"qos"`
}
```

Update `defaults()` so the default config preserves existing behavior:

```go
func defaults() *Config {
	return &Config{
		Server: ServerConfig{Host: "0.0.0.0", Port: 8080},
		Runner: RunnerConfig{TimeoutSeconds: 300},
		Services: ServicesConfig{
			HTTP: HTTPServiceConfig{Enabled: true},
			MQTT: MQTTServiceConfig{
				Enabled:      false,
				Broker:       "tcp://127.0.0.1:1883",
				ClientID:     "deploy-agent",
				CommandTopic: "deploy-agent/run",
				QoS:          1,
			},
		},
	}
}
```

Update validation:

```go
func (c *Config) validate() error {
	if c.Server.Port <= 0 || c.Server.Port > 65535 {
		return fmt.Errorf("server.port %d out of range", c.Server.Port)
	}
	if !c.Services.HTTP.Enabled && !c.Services.MQTT.Enabled {
		return fmt.Errorf("at least one service must be enabled")
	}
	if c.Services.HTTP.Enabled {
		if c.Auth.Username == "" {
			return fmt.Errorf("auth.username is empty")
		}
		if len(c.Auth.Password) < 8 {
			return fmt.Errorf("auth.password must be at least 8 characters")
		}
	}
	if c.Runner.TimeoutSeconds <= 0 {
		return fmt.Errorf("runner.timeoutSeconds must be > 0")
	}
	if c.Services.MQTT.Enabled {
		if c.Services.MQTT.Broker == "" {
			return fmt.Errorf("services.mqtt.broker is empty")
		}
		if c.Services.MQTT.CommandTopic == "" {
			return fmt.Errorf("services.mqtt.commandTopic is empty")
		}
		if c.Services.MQTT.QoS < 0 || c.Services.MQTT.QoS > 2 {
			return fmt.Errorf("services.mqtt.qos must be 0, 1, or 2")
		}
	}
	return nil
}
```

- [ ] **Step 4: Update example config**

Modify `D:\Code\bat-agent\config.example.yaml`:

```yaml
services:
  http:
    enabled: true
  mqtt:
    enabled: false
    broker: "tcp://127.0.0.1:1883"
    clientId: "deploy-agent"
    username: ""
    password: ""
    commandTopic: "deploy-agent/run"
    qos: 1

server:
  host: 0.0.0.0
  port: 8080

auth:
  username: admin
  # 至少 8 位；生产环境务必改掉
  password: change-me-please

runner:
  # 单个 bat 最大执行时间（秒）
  timeoutSeconds: 300
  # bat 脚本所在目录；留空 = deploy-agent.exe 所在目录
  scriptDir: ""
```

- [ ] **Step 5: Run config tests and commit**

Run:

```cmd
gofmt -w internal/config
go test ./internal/config
```

Expected: all config tests pass.

Commit:

```cmd
git add internal/config/config.go internal/config/config_test.go config.example.yaml
git commit -m "feat: add service configuration"
```

---

### Task 2: Add Streaming Support To Runner

**Files:**

```text
Modify: D:\Code\bat-agent\internal\runner\runner.go
```

Runner behavior is Windows-only. Verification should happen on Windows, which is the current project target.

- [ ] **Step 1: Add runner streaming types**

Modify `D:\Code\bat-agent\internal\runner\runner.go`:

```go
type Stream string

const (
	StreamStdout Stream = "stdout"
	StreamStderr Stream = "stderr"
)

type OutputChunk struct {
	Stream Stream
	Data   string
}

type OutputFunc func(OutputChunk)
```

- [ ] **Step 2: Replace direct cappedBuffer writes with stream capture**

Refactor `Run` into a wrapper:

```go
func Run(ctx context.Context, path string, timeout time.Duration) (Result, error) {
	return RunStream(ctx, path, timeout, nil)
}
```

Add:

```go
func RunStream(ctx context.Context, path string, timeout time.Duration, onOutput OutputFunc) (Result, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "cmd.exe", "/c", path)
	cmd.Dir = filepath.Dir(path)

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return Result{FinishedAt: time.Now()}, err
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return Result{FinishedAt: time.Now()}, err
	}

	stdoutBuf := &cappedBuffer{limit: maxOutputBytes}
	stderrBuf := &cappedBuffer{limit: maxOutputBytes}

	res := Result{StartedAt: time.Now(), ExitCode: -1}
	if err := cmd.Start(); err != nil {
		res.FinishedAt = time.Now()
		return res, err
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go copyOutput(&wg, stdoutPipe, stdoutBuf, StreamStdout, onOutput)
	go copyOutput(&wg, stderrPipe, stderrBuf, StreamStderr, onOutput)

	waitErr := cmd.Wait()
	wg.Wait()
	res.FinishedAt = time.Now()

	if ctx.Err() == context.DeadlineExceeded {
		res.TimedOut = true
		if cmd.Process != nil {
			killTree(cmd.Process.Pid)
		}
	}

	res.Stdout = decodeOutput(stdoutBuf.Bytes())
	res.Stderr = decodeOutput(stderrBuf.Bytes())

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

Add imports:

```go
import (
	"sync"
)
```

Add stream copy helper:

```go
func copyOutput(wg *sync.WaitGroup, r io.Reader, cap *cappedBuffer, stream Stream, onOutput OutputFunc) {
	defer wg.Done()
	buf := make([]byte, 32*1024)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			chunk := append([]byte(nil), buf[:n]...)
			_, _ = cap.Write(chunk)
			if onOutput != nil {
				onOutput(OutputChunk{Stream: stream, Data: decodeOutput(chunk)})
			}
		}
		if err != nil {
			return
		}
	}
}
```

- [ ] **Step 3: Run runner compile check**

Run:

```cmd
gofmt -w internal/runner
go test ./internal/runner
```

Expected: package compiles on Windows. There may be no runner test files yet.

- [ ] **Step 4: Commit runner streaming support**

Commit:

```cmd
git add internal/runner/runner.go
git commit -m "feat: stream runner output"
```

---

### Task 3: Create Executor Package

**Files:**

```text
Create: D:\Code\bat-agent\internal\executor\executor.go
Create: D:\Code\bat-agent\internal\executor\executor_test.go
```

- [ ] **Step 1: Write failing executor tests**

Create `D:\Code\bat-agent\internal\executor\executor_test.go`:

```go
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
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0644); err != nil {
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

	_, err := exec.RunCollect(context.Background(), `..\evil.bat`)

	if !errors.Is(err, ErrInvalidScriptName) {
		t.Fatalf("expected ErrInvalidScriptName, got %v", err)
	}
}

func TestRunCollectRejectsMissingScript(t *testing.T) {
	exec := New(makeRegistry(t, nil), time.Second)

	_, err := exec.RunCollect(context.Background(), "missing.bat")

	if !errors.Is(err, ErrScriptNotFound) {
		t.Fatalf("expected ErrScriptNotFound, got %v", err)
	}
}

func TestRunStreamReturnsBusyWhenSameScriptIsLocked(t *testing.T) {
	reg := makeRegistry(t, map[string]string{"busy.bat": "@echo off\r\ntimeout /t 2 >nul\r\n"})
	entry, err := reg.Lookup("busy.bat")
	if err != nil {
		t.Fatal(err)
	}
	if !entry.TryLock() {
		t.Fatal("expected lock")
	}
	defer entry.Unlock()

	exec := New(reg, time.Second)
	_, err = exec.RunStream(context.Background(), "busy.bat", nil)

	if !errors.Is(err, ErrScriptBusy) {
		t.Fatalf("expected ErrScriptBusy, got %v", err)
	}
}

func TestStableErrorText(t *testing.T) {
	cases := map[error]string{
		ErrInvalidScriptName: "invalid script name",
		ErrScriptNotFound:   "script not found",
		ErrScriptBusy:       "script is already running",
		ErrRunnerStart:      "runner start failed",
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
		t.Fatalf("unexpected script: %q", res.Script)
	}
	if res.ExitCode != 0 {
		t.Fatalf("unexpected exit code: %d", res.ExitCode)
	}
	if !strings.Contains(res.Stdout, "hello") {
		t.Fatalf("stdout did not contain hello: %q", res.Stdout)
	}
}
```

- [ ] **Step 2: Run executor tests and verify they fail**

Run:

```cmd
go test ./internal/executor
```

Expected: failure because `internal/executor` does not exist.

- [ ] **Step 3: Implement executor**

Create `D:\Code\bat-agent\internal\executor\executor.go`:

```go
package executor

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/liqixin/deploy-agent/internal/registry"
	"github.com/liqixin/deploy-agent/internal/runner"
)

var (
	ErrInvalidScriptName = errors.New("invalid script name")
	ErrScriptNotFound   = errors.New("script not found")
	ErrScriptBusy       = errors.New("script is already running")
	ErrRunnerStart      = errors.New("runner start failed")
	ErrScriptTimedOut   = errors.New("script timed out")
)

type Executor struct {
	reg     *registry.Registry
	timeout time.Duration
}

type Result struct {
	Script     string
	ExitCode   int
	Stdout     string
	Stderr     string
	StartedAt  time.Time
	FinishedAt time.Time
	TimedOut   bool
}

type OutputChunk struct {
	Stream string
	Data   string
}

type OutputFunc func(OutputChunk)

func New(reg *registry.Registry, timeout time.Duration) *Executor {
	return &Executor{reg: reg, timeout: timeout}
}

func (e *Executor) RunCollect(ctx context.Context, script string) (Result, error) {
	return e.RunStream(ctx, script, nil)
}

func (e *Executor) RunStream(ctx context.Context, script string, onOutput OutputFunc) (Result, error) {
	entry, err := e.reg.Lookup(script)
	if err != nil {
		switch {
		case errors.Is(err, registry.ErrInvalid):
			return Result{Script: script, ExitCode: -1}, ErrInvalidScriptName
		case errors.Is(err, registry.ErrNotFound):
			return Result{Script: script, ExitCode: -1}, ErrScriptNotFound
		default:
			return Result{Script: script, ExitCode: -1}, err
		}
	}
	if !entry.TryLock() {
		return Result{Script: entry.Name, ExitCode: -1}, ErrScriptBusy
	}
	defer entry.Unlock()

	res, err := runner.RunStream(ctx, entry.Path, e.timeout, func(chunk runner.OutputChunk) {
		if onOutput != nil {
			onOutput(OutputChunk{Stream: string(chunk.Stream), Data: chunk.Data})
		}
	})
	out := Result{
		Script:     entry.Name,
		ExitCode:   res.ExitCode,
		Stdout:     res.Stdout,
		Stderr:     res.Stderr,
		StartedAt:  res.StartedAt,
		FinishedAt: res.FinishedAt,
		TimedOut:   res.TimedOut,
	}
	if res.TimedOut {
		return out, ErrScriptTimedOut
	}
	if err != nil {
		return out, fmt.Errorf("%w: %v", ErrRunnerStart, err)
	}
	return out, nil
}

func StableError(err error) string {
	switch {
	case errors.Is(err, ErrInvalidScriptName):
		return "invalid script name"
	case errors.Is(err, ErrScriptNotFound):
		return "script not found"
	case errors.Is(err, ErrScriptBusy):
		return "script is already running"
	case errors.Is(err, ErrRunnerStart):
		return "runner start failed"
	case errors.Is(err, ErrScriptTimedOut):
		return "script timed out"
	default:
		if err == nil {
			return ""
		}
		return err.Error()
	}
}
```

- [ ] **Step 4: Run executor tests**

Run:

```cmd
gofmt -w internal/executor
go test ./internal/executor
```

Expected: executor tests pass.

- [ ] **Step 5: Commit executor**

Commit:

```cmd
git add internal/executor
git commit -m "feat: add script executor"
```

---

### Task 4: Migrate HTTP /run To Executor Without API Changes

**Files:**

```text
Modify: D:\Code\bat-agent\internal\httpapi\handler.go
Create: D:\Code\bat-agent\internal\httpapi\handler_test.go
Modify: D:\Code\bat-agent\main.go
```

- [ ] **Step 1: Write HTTP regression tests**

Create `D:\Code\bat-agent\internal\httpapi\handler_test.go`:

```go
package httpapi

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/liqixin/deploy-agent/internal/auth"
	"github.com/liqixin/deploy-agent/internal/executor"
	"github.com/liqixin/deploy-agent/internal/registry"
)

func makeServer(t *testing.T, files map[string]string) http.Handler {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	reg, err := registry.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	exec := executor.New(reg, 5*time.Second)
	api := New(exec)
	return api.Routes(func(h http.Handler) http.Handler {
		return auth.BasicAuth("admin", "change-me-please", h)
	})
}

func authHeader() string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte("admin:change-me-please"))
}

func TestHealthDoesNotRequireAuth(t *testing.T) {
	handler := makeServer(t, nil)
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestRunRequiresAuth(t *testing.T) {
	handler := makeServer(t, map[string]string{"hello.bat": "@echo off\r\necho hello\r\n"})
	req := httptest.NewRequest(http.MethodPost, "/run", bytes.NewBufferString(`{"script":"hello.bat"}`))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestRunReturnsExistingResponseShape(t *testing.T) {
	handler := makeServer(t, map[string]string{"hello.bat": "@echo off\r\necho hello\r\n"})
	req := httptest.NewRequest(http.MethodPost, "/run", bytes.NewBufferString(`{"script":"hello.bat"}`))
	req.Header.Set("Authorization", authHeader())
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"script", "exitCode", "stdout", "stderr", "startedAt", "finishedAt", "durationMs"} {
		if _, ok := body[key]; !ok {
			t.Fatalf("missing key %s in %#v", key, body)
		}
	}
	if _, ok := body["done"]; ok {
		t.Fatalf("HTTP response must not include MQTT done field")
	}
}

func TestRunNonZeroExitCodeStillReturnsHTTP200(t *testing.T) {
	handler := makeServer(t, map[string]string{"fail.bat": "@echo off\r\nexit /b 7\r\n"})
	req := httptest.NewRequest(http.MethodPost, "/run", bytes.NewBufferString(`{"script":"fail.bat"}`))
	req.Header.Set("Authorization", authHeader())
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		ExitCode int `json:"exitCode"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.ExitCode != 7 {
		t.Fatalf("expected exitCode 7, got %d", body.ExitCode)
	}
}
```

- [ ] **Step 2: Run HTTP tests and verify they fail**

Run:

```cmd
go test ./internal/httpapi
```

Expected: failure because `httpapi.New` still expects registry and timeout instead of executor.

- [ ] **Step 3: Change HTTP server to depend on executor**

Modify `D:\Code\bat-agent\internal\httpapi\handler.go`:

```go
type Server struct {
	exec *executor.Executor
}

func New(exec *executor.Executor) *Server {
	return &Server{exec: exec}
}
```

Update imports:

```go
import (
	"errors"

	"github.com/liqixin/deploy-agent/internal/executor"
)
```

Update `handleRun` after JSON decoding:

```go
result, err := s.exec.RunCollect(context.Background(), req.Script)
if err != nil {
	switch {
	case errors.Is(err, executor.ErrInvalidScriptName):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": executor.StableError(err)})
		return
	case errors.Is(err, executor.ErrScriptNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": executor.StableError(err)})
		return
	case errors.Is(err, executor.ErrScriptBusy):
		writeJSON(w, http.StatusConflict, map[string]string{
			"error":  executor.StableError(err),
			"script": result.Script,
		})
		return
	}
}

resp := runResponse{
	Script:     result.Script,
	ExitCode:   result.ExitCode,
	Stdout:     result.Stdout,
	Stderr:     result.Stderr,
	StartedAt:  result.StartedAt,
	FinishedAt: result.FinishedAt,
	DurationMs: result.FinishedAt.Sub(result.StartedAt).Milliseconds(),
	TimedOut:   result.TimedOut,
}

status := http.StatusOK
switch {
case result.TimedOut:
	status = http.StatusGatewayTimeout
	resp.Error = executor.StableError(err)
case err != nil:
	status = http.StatusInternalServerError
	resp.Error = executor.StableError(err)
}
writeJSON(w, status, resp)
```

- [ ] **Step 4: Wire executor in main**

Modify `D:\Code\bat-agent\main.go`:

```go
timeout := time.Duration(cfg.Runner.TimeoutSeconds) * time.Second
exec := executor.New(reg, timeout)
api := httpapi.New(exec)
```

Add import:

```go
"github.com/liqixin/deploy-agent/internal/executor"
```

- [ ] **Step 5: Run HTTP tests and commit**

Run:

```cmd
gofmt -w main.go internal/httpapi
go test ./internal/httpapi
go test ./...
```

Expected: all tests pass.

Commit:

```cmd
git add main.go internal/httpapi
git commit -m "refactor: route http runs through executor"
```

---

### Task 5: Add MQTT Protocol Encoding And Validation

**Files:**

```text
Create: D:\Code\bat-agent\internal\mqttapi\protocol.go
Create: D:\Code\bat-agent\internal\mqttapi\protocol_test.go
```

- [ ] **Step 1: Write failing protocol tests**

Create `D:\Code\bat-agent\internal\mqttapi\protocol_test.go`:

```go
package mqttapi

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestParseCommandRequiresFields(t *testing.T) {
	_, err := ParseCommand([]byte(`{"script":"deploy.bat","replyTo":"replies/1"}`))
	if err == nil || err.Error() != "missing requestId" {
		t.Fatalf("expected missing requestId, got %v", err)
	}

	_, err = ParseCommand([]byte(`{"requestId":"1","script":"deploy.bat"}`))
	if err == nil || err.Error() != "missing replyTo" {
		t.Fatalf("expected missing replyTo, got %v", err)
	}

	_, err = ParseCommand([]byte(`{"requestId":"1","replyTo":"replies/1"}`))
	if err == nil || err.Error() != "invalid script name" {
		t.Fatalf("expected invalid script name, got %v", err)
	}
}

func TestParseCommandAcceptsValidPayload(t *testing.T) {
	cmd, err := ParseCommand([]byte(`{"requestId":"1","script":"deploy.bat","replyTo":"replies/1"}`))
	if err != nil {
		t.Fatalf("ParseCommand returned error: %v", err)
	}
	if cmd.RequestID != "1" || cmd.Script != "deploy.bat" || cmd.ReplyTo != "replies/1" {
		t.Fatalf("unexpected command: %#v", cmd)
	}
}

func TestOutputMessageShape(t *testing.T) {
	msg := OutputMessage{
		RequestID: "1",
		Script:    "deploy.bat",
		Stream:    "stdout",
		Data:      "hello\r\n",
		Done:      false,
	}
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	for _, want := range []string{`"requestId":"1"`, `"script":"deploy.bat"`, `"stream":"stdout"`, `"data":"hello\r\n"`, `"done":false`} {
		if !strings.Contains(got, want) {
			t.Fatalf("message %s missing %s", got, want)
		}
	}
}

func TestFinalMessageShape(t *testing.T) {
	exitCode := 0
	timedOut := false
	start := time.Date(2026, 6, 10, 10, 0, 0, 0, time.FixedZone("CST", 8*3600))
	finish := start.Add(3 * time.Second)
	msg := FinalMessage{
		RequestID:  "1",
		Script:     "deploy.bat",
		ExitCode:   &exitCode,
		TimedOut:   &timedOut,
		StartedAt:  &start,
		FinishedAt: &finish,
		DurationMs: 3000,
		Done:       true,
	}
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	for _, want := range []string{`"requestId":"1"`, `"script":"deploy.bat"`, `"exitCode":0`, `"timedOut":false`, `"durationMs":3000`, `"done":true`} {
		if !strings.Contains(got, want) {
			t.Fatalf("message %s missing %s", got, want)
		}
	}
}
```

- [ ] **Step 2: Run protocol tests and verify they fail**

Run:

```cmd
go test ./internal/mqttapi
```

Expected: failure because `internal/mqttapi` does not exist.

- [ ] **Step 3: Implement protocol types**

Create `D:\Code\bat-agent\internal\mqttapi\protocol.go`:

```go
package mqttapi

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
)

var (
	ErrInvalidJSON      = errors.New("invalid JSON body")
	ErrMissingRequestID = errors.New("missing requestId")
	ErrMissingReplyTo   = errors.New("missing replyTo")
	ErrInvalidScript    = errors.New("invalid script name")
)

type Command struct {
	RequestID string `json:"requestId"`
	Script    string `json:"script"`
	ReplyTo   string `json:"replyTo"`
}

type OutputMessage struct {
	RequestID string `json:"requestId"`
	Script    string `json:"script"`
	Stream    string `json:"stream"`
	Data      string `json:"data"`
	Done      bool   `json:"done"`
}

type FinalMessage struct {
	RequestID  string     `json:"requestId,omitempty"`
	Script     string     `json:"script,omitempty"`
	ExitCode   *int       `json:"exitCode,omitempty"`
	TimedOut   *bool      `json:"timedOut,omitempty"`
	Error      string     `json:"error,omitempty"`
	StartedAt  *time.Time `json:"startedAt,omitempty"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
	DurationMs int64      `json:"durationMs,omitempty"`
	Done       bool       `json:"done"`
}

func ParseCommand(payload []byte) (Command, error) {
	var cmd Command
	if err := json.Unmarshal(payload, &cmd); err != nil {
		return cmd, ErrInvalidJSON
	}
	if strings.TrimSpace(cmd.ReplyTo) == "" {
		return cmd, ErrMissingReplyTo
	}
	if strings.TrimSpace(cmd.RequestID) == "" {
		return cmd, ErrMissingRequestID
	}
	if strings.TrimSpace(cmd.Script) == "" {
		return cmd, ErrInvalidScript
	}
	return cmd, nil
}
```

- [ ] **Step 4: Run protocol tests and commit**

Run:

```cmd
gofmt -w internal/mqttapi
go test ./internal/mqttapi
```

Expected: protocol tests pass.

Commit:

```cmd
git add internal/mqttapi/protocol.go internal/mqttapi/protocol_test.go
git commit -m "feat: add mqtt protocol types"
```

---

### Task 6: Implement MQTT Command Handling With Fake Publisher Tests

**Files:**

```text
Modify: D:\Code\bat-agent\internal\mqttapi\client.go
Modify: D:\Code\bat-agent\internal\mqttapi\client_test.go
Modify: D:\Code\bat-agent\go.mod
Modify: D:\Code\bat-agent\go.sum
```

- [ ] **Step 1: Add MQTT dependency**

Run:

```cmd
go get github.com/eclipse/paho.mqtt.golang@latest
```

Expected: `go.mod` and `go.sum` include the Paho MQTT dependency.

- [ ] **Step 2: Write fake publisher tests for command handling**

Create `D:\Code\bat-agent\internal\mqttapi\client_test.go`:

```go
package mqttapi

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/liqixin/deploy-agent/internal/executor"
	"github.com/liqixin/deploy-agent/internal/registry"
)

type publishedMessage struct {
	topic   string
	payload []byte
}

type fakePublisher struct {
	messages []publishedMessage
}

func (f *fakePublisher) Publish(ctx context.Context, topic string, payload []byte) error {
	f.messages = append(f.messages, publishedMessage{topic: topic, payload: append([]byte(nil), payload...)})
	return nil
}

func testExecutor(t *testing.T, files map[string]string) *executor.Executor {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	reg, err := registry.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	return executor.New(reg, 5*time.Second)
}

func TestHandleCommandPublishesOutputAndFinalMessage(t *testing.T) {
	exec := testExecutor(t, map[string]string{"hello.bat": "@echo off\r\necho hello\r\n"})
	pub := &fakePublisher{}
	h := NewHandler(exec, pub, 1)

	err := h.Handle(context.Background(), []byte(`{"requestId":"1","script":"hello.bat","replyTo":"replies/1"}`))
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if len(pub.messages) < 2 {
		t.Fatalf("expected output and final messages, got %d", len(pub.messages))
	}
	for _, msg := range pub.messages {
		if msg.topic != "replies/1" {
			t.Fatalf("unexpected topic %q", msg.topic)
		}
	}
	var final FinalMessage
	if err := json.Unmarshal(pub.messages[len(pub.messages)-1].payload, &final); err != nil {
		t.Fatal(err)
	}
	if !final.Done {
		t.Fatalf("final message must have done=true")
	}
	if final.ExitCode == nil || *final.ExitCode != 0 {
		t.Fatalf("unexpected final exit code: %#v", final.ExitCode)
	}
}

func TestHandleCommandPublishesMissingRequestID(t *testing.T) {
	exec := testExecutor(t, nil)
	pub := &fakePublisher{}
	h := NewHandler(exec, pub, 1)

	err := h.Handle(context.Background(), []byte(`{"script":"deploy.bat","replyTo":"replies/1"}`))
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if len(pub.messages) != 1 {
		t.Fatalf("expected one error message, got %d", len(pub.messages))
	}
	var final FinalMessage
	if err := json.Unmarshal(pub.messages[0].payload, &final); err != nil {
		t.Fatal(err)
	}
	if final.Error != "missing requestId" || !final.Done {
		t.Fatalf("unexpected final message: %#v", final)
	}
}

func TestHandleCommandWithoutReplyToPublishesNothing(t *testing.T) {
	exec := testExecutor(t, nil)
	pub := &fakePublisher{}
	h := NewHandler(exec, pub, 1)

	err := h.Handle(context.Background(), []byte(`{"requestId":"1","script":"deploy.bat"}`))
	if err == nil {
		t.Fatalf("expected missing replyTo error")
	}
	if len(pub.messages) != 0 {
		t.Fatalf("expected no published messages, got %d", len(pub.messages))
	}
}

func TestHandleCommandPublishesScriptNotFound(t *testing.T) {
	exec := testExecutor(t, nil)
	pub := &fakePublisher{}
	h := NewHandler(exec, pub, 1)

	err := h.Handle(context.Background(), []byte(`{"requestId":"1","script":"missing.bat","replyTo":"replies/1"}`))
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	var final FinalMessage
	if err := json.Unmarshal(pub.messages[len(pub.messages)-1].payload, &final); err != nil {
		t.Fatal(err)
	}
	if final.Error != "script not found" || !final.Done {
		t.Fatalf("unexpected final message: %#v", final)
	}
}
```

- [ ] **Step 3: Run MQTT tests and verify they fail**

Run:

```cmd
go test ./internal/mqttapi
```

Expected: failure because `NewHandler`, `Publisher`, and `Handle` are missing.

- [ ] **Step 4: Implement handler and publisher abstraction**

Create `D:\Code\bat-agent\internal\mqttapi\client.go`:

```go
package mqttapi

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
	"github.com/liqixin/deploy-agent/internal/executor"
)

type Publisher interface {
	Publish(ctx context.Context, topic string, payload []byte) error
}

type Handler struct {
	exec *executor.Executor
	pub  Publisher
	qos  byte
}

func NewHandler(exec *executor.Executor, pub Publisher, qos byte) *Handler {
	return &Handler{exec: exec, pub: pub, qos: qos}
}

func (h *Handler) Handle(ctx context.Context, payload []byte) error {
	cmd, err := ParseCommand(payload)
	if err != nil {
		if errors.Is(err, ErrMissingReplyTo) || errors.Is(err, ErrInvalidJSON) {
			return err
		}
		return h.publishFinal(ctx, CommandReplyFromPayload(payload), FinalMessage{
			RequestID: cmd.RequestID,
			Script:    cmd.Script,
			Error:     err.Error(),
			Done:      true,
		})
	}

	result, runErr := h.exec.RunStream(ctx, cmd.Script, func(chunk executor.OutputChunk) {
		msg := OutputMessage{
			RequestID: cmd.RequestID,
			Script:    cmd.Script,
			Stream:    chunk.Stream,
			Data:      chunk.Data,
			Done:      false,
		}
		if err := h.publishJSON(ctx, cmd.ReplyTo, msg); err != nil {
			log.Printf("mqtt publish output failed: %v", err)
		}
	})

	final := FinalMessage{
		RequestID: cmd.RequestID,
		Script:    cmd.Script,
		Done:      true,
	}
	if !result.StartedAt.IsZero() {
		exitCode := result.ExitCode
		timedOut := result.TimedOut
		final.ExitCode = &exitCode
		final.TimedOut = &timedOut
		final.StartedAt = timePtr(result.StartedAt)
		final.FinishedAt = timePtr(result.FinishedAt)
		final.DurationMs = result.FinishedAt.Sub(result.StartedAt).Milliseconds()
	}
	if runErr != nil {
		final.Error = executor.StableError(runErr)
	}
	return h.publishFinal(ctx, cmd.ReplyTo, final)
}

func CommandReplyFromPayload(payload []byte) string {
	var cmd Command
	_ = json.Unmarshal(payload, &cmd)
	return cmd.ReplyTo
}

func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func (h *Handler) publishFinal(ctx context.Context, replyTo string, msg FinalMessage) error {
	if replyTo == "" {
		return ErrMissingReplyTo
	}
	return h.publishJSON(ctx, replyTo, msg)
}

func (h *Handler) publishJSON(ctx context.Context, topic string, msg any) error {
	payload, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return h.pub.Publish(ctx, topic, payload)
}

type PahoPublisher struct {
	client paho.Client
	qos    byte
}

func NewPahoPublisher(client paho.Client, qos byte) *PahoPublisher {
	return &PahoPublisher{client: client, qos: qos}
}

func (p *PahoPublisher) Publish(ctx context.Context, topic string, payload []byte) error {
	token := p.client.Publish(topic, p.qos, false, payload)
	done := make(chan struct{})
	go func() {
		token.Wait()
		close(done)
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return token.Error()
	}
}
```

- [ ] **Step 5: Run MQTT tests and commit**

Run:

```cmd
gofmt -w internal/mqttapi
go test ./internal/mqttapi
go test ./...
```

Expected: tests pass.

Commit:

```cmd
git add go.mod go.sum internal/mqttapi
git commit -m "feat: handle mqtt run commands"
```

---

### Task 7: Add MQTT Client Startup And Main Service Coordination

**Files:**

```text
Modify: D:\Code\bat-agent\internal\mqttapi\client.go
Modify: D:\Code\bat-agent\main.go
```

- [ ] **Step 1: Add MQTT service constructor**

Extend `D:\Code\bat-agent\internal\mqttapi\client.go`:

```go
type Config struct {
	Broker       string
	ClientID     string
	Username     string
	Password     string
	CommandTopic string
	QoS          byte
}

type Client struct {
	cfg     Config
	exec    *executor.Executor
	client  paho.Client
	handler *Handler
}

func NewClient(cfg Config, exec *executor.Executor) *Client {
	return &Client{cfg: cfg, exec: exec}
}

func (c *Client) Start(ctx context.Context) error {
	opts := paho.NewClientOptions()
	opts.AddBroker(c.cfg.Broker)
	opts.SetClientID(c.cfg.ClientID)
	opts.SetAutoReconnect(true)
	if c.cfg.Username != "" {
		opts.SetUsername(c.cfg.Username)
	}
	if c.cfg.Password != "" {
		opts.SetPassword(c.cfg.Password)
	}

	c.client = paho.NewClient(opts)
	if token := c.client.Connect(); token.Wait() && token.Error() != nil {
		return token.Error()
	}

	pub := NewPahoPublisher(c.client, c.cfg.QoS)
	c.handler = NewHandler(c.exec, pub, c.cfg.QoS)

	token := c.client.Subscribe(c.cfg.CommandTopic, c.cfg.QoS, func(_ paho.Client, msg paho.Message) {
		go func(payload []byte) {
			if err := c.handler.Handle(ctx, payload); err != nil {
				log.Printf("mqtt command rejected: %v", err)
			}
		}(append([]byte(nil), msg.Payload()...))
	})
	if token.Wait() && token.Error() != nil {
		c.client.Disconnect(250)
		return token.Error()
	}

	log.Printf("mqtt connected to %s and subscribed %s", c.cfg.Broker, c.cfg.CommandTopic)

	go func() {
		<-ctx.Done()
		c.client.Disconnect(250)
	}()
	return nil
}
```

- [ ] **Step 2: Refactor main to start enabled services**

Modify `D:\Code\bat-agent\main.go` so startup creates one executor:

```go
timeout := time.Duration(cfg.Runner.TimeoutSeconds) * time.Second
exec := executor.New(reg, timeout)
```

Start MQTT when enabled:

```go
if cfg.Services.MQTT.Enabled {
	mqttClient := mqttapi.NewClient(mqttapi.Config{
		Broker:       cfg.Services.MQTT.Broker,
		ClientID:     cfg.Services.MQTT.ClientID,
		Username:     cfg.Services.MQTT.Username,
		Password:     cfg.Services.MQTT.Password,
		CommandTopic: cfg.Services.MQTT.CommandTopic,
		QoS:          byte(cfg.Services.MQTT.QoS),
	}, exec)
	if err := mqttClient.Start(ctx); err != nil {
		return fmt.Errorf("start mqtt: %w", err)
	}
}
```

Only start HTTP when enabled:

```go
if cfg.Services.HTTP.Enabled {
	api := httpapi.New(exec)
	authWrap := func(h http.Handler) http.Handler {
		return auth.BasicAuth(cfg.Auth.Username, cfg.Auth.Password, h)
	}
	addr := net.JoinHostPort(cfg.Server.Host, strconv.Itoa(cfg.Server.Port))
	srv := &http.Server{
		Addr:              addr,
		Handler:           api.Routes(authWrap),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		log.Printf("deploy-agent http listening on %s (timeout %s)", addr, timeout)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
}
```

Keep the existing signal wait:

```go
select {
case <-ctx.Done():
	log.Printf("shutdown signal received")
case err := <-errCh:
	if err != nil {
		return err
	}
}
return nil
```

Add import:

```go
"github.com/liqixin/deploy-agent/internal/mqttapi"
```

- [ ] **Step 3: Run compile and tests**

Run:

```cmd
gofmt -w main.go internal/mqttapi
go test ./...
```

Expected: all tests pass.

- [ ] **Step 4: Commit service startup**

Commit:

```cmd
git add main.go internal/mqttapi/client.go
git commit -m "feat: start mqtt service from config"
```

---

### Task 8: Update README Protocol Documentation

**Files:**

```text
Modify: D:\Code\bat-agent\README.md
```

- [ ] **Step 1: Add README MQTT configuration section**

Add this section after existing config sample:

```markdown
### HTTP / MQTT 开关

默认只启用 HTTP。需要 MQTT 调度时，在 `config.yaml` 中启用：

```yaml
services:
  http:
    enabled: true
  mqtt:
    enabled: true
    broker: "tcp://127.0.0.1:1883"
    clientId: "deploy-agent"
    username: ""
    password: ""
    commandTopic: "deploy-agent/run"
    qos: 1
```

`broker` 支持 `tcp://` 和 `ssl://`。MQTT 鉴权依赖 broker 的账号、密码、TLS 和 ACL。
```

- [ ] **Step 2: Add MQTT protocol section**

Add:

```markdown
## MQTT API

MQTT 用于实时展示脚本输出。服务订阅固定命令 topic，默认：

```text
deploy-agent/run
```

命令 payload：

```json
{
  "requestId": "abc-123",
  "script": "deploy.bat",
  "replyTo": "deploy-agent/replies/abc-123"
}
```

实时输出消息会发布到 `replyTo`：

```json
{
  "requestId": "abc-123",
  "script": "deploy.bat",
  "stream": "stdout",
  "data": "partial output...\r\n",
  "done": false
}
```

最终消息：

```json
{
  "requestId": "abc-123",
  "script": "deploy.bat",
  "exitCode": 0,
  "timedOut": false,
  "startedAt": "2026-06-10T10:00:00+08:00",
  "finishedAt": "2026-06-10T10:00:03+08:00",
  "durationMs": 3142,
  "done": true
}
```

显示端建议按以下规则判断状态：

| 条件 | 状态 |
|---|---|
| `done == false` | 运行中，追加 `data` 到 stdout/stderr |
| `done == true && timedOut == true` | 超时 |
| `done == true && error != ""` | 调度失败或 runner 错误 |
| `done == true && exitCode == 0` | 执行成功 |
| `done == true && exitCode != 0` | 脚本自身失败 |
```

- [ ] **Step 3: Run docs-adjacent checks**

Run:

```cmd
go test ./...
```

Expected: tests still pass.

- [ ] **Step 4: Commit README**

Commit:

```cmd
git add README.md
git commit -m "docs: document mqtt scheduling"
```

---

### Task 9: Final Verification

**Files:**

```text
Verify entire repository.
```

- [ ] **Step 1: Format all Go code**

Run:

```cmd
gofmt -w .
```

Expected: command exits 0.

- [ ] **Step 2: Run tests**

Run:

```cmd
go test ./...
```

Expected: all packages pass.

- [ ] **Step 3: Run vet**

Run:

```cmd
go vet ./...
```

Expected: command exits 0 with no diagnostics.

- [ ] **Step 4: Build Windows executable**

Run:

```cmd
build.bat
```

Expected:

```text
Embedding manifest...
Building...
Done: deploy-agent.exe
```

If `rsrc` is not on `PATH`, keep the existing fixed `build.bat` behavior that resolves the Go tool bin directory.

- [ ] **Step 5: Manual MQTT smoke test**

Use a local broker such as Mosquitto. Start broker, then run `deploy-agent.exe` with MQTT enabled.

Subscribe to reply topic:

```cmd
mosquitto_sub -h 127.0.0.1 -t deploy-agent/replies/test-1
```

Publish command:

```cmd
mosquitto_pub -h 127.0.0.1 -t deploy-agent/run -m "{\"requestId\":\"test-1\",\"script\":\"hello.bat\",\"replyTo\":\"deploy-agent/replies/test-1\"}"
```

Expected:

```text
At least one message with "done":false for stdout/stderr output.
One final message with "done":true.
The final message includes requestId, script, exitCode, timedOut, startedAt, finishedAt, durationMs.
```

- [ ] **Step 6: Final commit if verification changed docs or generated dependency files**

If final verification required additional source or docs changes:

```cmd
git add .
git commit -m "chore: finalize mqtt scheduling"
```

If no files changed, do not create an empty commit.

---

## Self-Review

Spec coverage:

```text
Config defaults and validation: Task 1
HTTP compatibility: Task 4 and Task 8
MQTT command/reply protocol: Task 5 and Task 6
Realtime stdout/stderr streaming: Task 2, Task 3, Task 6
Shared whitelist/lock/timeout semantics: Task 3 and Task 4
Broker auth through MQTT connection options: Task 6 and Task 7
Configurable HTTP/MQTT enablement: Task 1 and Task 7
Docs and display-program protocol guidance: Task 8
Final verification: Task 9
```

Implementation guardrails:

```text
Do not change HTTP response field names.
Do not add script arguments.
Do not add MQTT payload token.
Do not add task queue or task history.
Do not change registry path traversal protections.
Do not remove per-script locking.
Do not remove output cap or GBK fallback decoding.
```
