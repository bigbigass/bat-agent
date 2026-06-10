# GUI Management Console Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a Windows desktop GUI for managing `deploy-agent`, including local service start/stop, remote connections, script listing, and real-time script output.

**Architecture:** Keep script execution inside `deploy-agent`; the GUI is a separate Fyne application that talks to the service over HTTP. Add `POST /run/stream` as an authenticated NDJSON streaming API, then build focused GUI support packages for config persistence, HTTP access, local process management, and UI wiring.

**Tech Stack:** Go 1.19+, standard library `net/http` / `encoding/json` / `os/exec`, existing `internal/executor`, Fyne v2, Windows batch build scripts.

---

## Reference Spec

Implement the approved design:

```text
D:\Code\bat-agent\docs\superpowers\specs\2026-06-10-gui-management-console-design.md
```

Preserve these existing behaviors:

```text
/health remains unauthenticated.
/scripts and /run remain behind Basic Auth.
/run response shape and status-code behavior stay stable.
MQTT behavior stays stable.
Only whitelisted .bat and .cmd file names can run.
The same script cannot run concurrently.
Runner timeout cleanup keeps killing the process tree.
Output capture keeps UTF-8 passthrough and GBK fallback.
```

## Scope Check

This plan covers two coupled pieces:

```text
Backend HTTP streaming API
Fyne GUI client
```

They can be implemented in one plan because the GUI's real-time output depends on `/run/stream`, and each task below produces independently testable software. The backend tasks land first so the GUI can be built against a stable local protocol.

## File Structure

### New Files

```text
D:\Code\bat-agent\cmd\deploy-agent-gui\main.go
D:\Code\bat-agent\internal\gui\apiclient\client.go
D:\Code\bat-agent\internal\gui\apiclient\client_test.go
D:\Code\bat-agent\internal\gui\guiconfig\config.go
D:\Code\bat-agent\internal\gui\guiconfig\config_test.go
D:\Code\bat-agent\internal\gui\localservice\service.go
D:\Code\bat-agent\internal\gui\localservice\service_test.go
```

### Modified Files

```text
D:\Code\bat-agent\README.md
D:\Code\bat-agent\build.bat
D:\Code\bat-agent\go.mod
D:\Code\bat-agent\go.sum
D:\Code\bat-agent\internal\httpapi\handler.go
D:\Code\bat-agent\internal\httpapi\handler_test.go
```

### Responsibility Map

```text
internal/httpapi
  Adds /run/stream and NDJSON response messages while preserving existing routes.

internal/gui/guiconfig
  Reads and writes the GUI's local JSON config under the user config directory.

internal/gui/apiclient
  Wraps deploy-agent HTTP calls: health, scripts, and NDJSON run streaming.

internal/gui/localservice
  Finds and manages deploy-agent.exe next to deploy-agent-gui.exe.

cmd/deploy-agent-gui
  Owns Fyne widgets, state transitions, and user actions.
```

---

### Task 1: Add Failing HTTP Streaming API Tests

**Files:**

```text
Modify: D:\Code\bat-agent\internal\httpapi\handler_test.go
```

- [ ] **Step 1: Add stream request and NDJSON helpers**

Append these helpers to `D:\Code\bat-agent\internal\httpapi\handler_test.go`:

```go
func postRunStream(script string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/run/stream", bytes.NewBufferString(`{"script":`+quoteJSON(script)+`}`))
	req.Header.Set("Authorization", authHeader())
	req.Header.Set("Content-Type", "application/json")
	return req
}

func decodeNDJSON(t *testing.T, body string) []map[string]json.RawMessage {
	t.Helper()

	lines := strings.Split(strings.TrimSpace(body), "\n")
	out := make([]map[string]json.RawMessage, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var msg map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			t.Fatalf("invalid NDJSON line %q: %v", line, err)
		}
		out = append(out, msg)
	}
	return out
}

func rawString(t *testing.T, msg map[string]json.RawMessage, key string) string {
	t.Helper()

	var value string
	if err := json.Unmarshal(msg[key], &value); err != nil {
		t.Fatalf("message key %q is not a string: %v", key, err)
	}
	return value
}

func rawInt(t *testing.T, msg map[string]json.RawMessage, key string) int {
	t.Helper()

	var value int
	if err := json.Unmarshal(msg[key], &value); err != nil {
		t.Fatalf("message key %q is not an int: %v", key, err)
	}
	return value
}

func rawBool(t *testing.T, msg map[string]json.RawMessage, key string) bool {
	t.Helper()

	var value bool
	if err := json.Unmarshal(msg[key], &value); err != nil {
		t.Fatalf("message key %q is not a bool: %v", key, err)
	}
	return value
}
```

- [ ] **Step 2: Add auth and preflight error tests**

Append these tests to `D:\Code\bat-agent\internal\httpapi\handler_test.go`:

```go
func TestRunStreamRequiresAuth(t *testing.T) {
	server := makeServer(t, map[string]string{"hello.bat": "@echo off\r\necho hello\r\n"})
	req := httptest.NewRequest(http.MethodPost, "/run/stream", bytes.NewBufferString(`{"script":"hello.bat"}`))
	rec := httptest.NewRecorder()

	server.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestRunStreamInvalidJSONReturns400(t *testing.T) {
	server := makeServer(t, nil)
	req := httptest.NewRequest(http.MethodPost, "/run/stream", bytes.NewBufferString(`{`))
	req.Header.Set("Authorization", authHeader())
	rec := httptest.NewRecorder()

	server.handler.ServeHTTP(rec, req)

	assertErrorResponse(t, rec, http.StatusBadRequest, "invalid JSON body")
}

func TestRunStreamInvalidScriptReturns400(t *testing.T) {
	server := makeServer(t, nil)
	rec := httptest.NewRecorder()

	server.handler.ServeHTTP(rec, postRunStream(`..\evil.bat`))

	assertErrorResponse(t, rec, http.StatusBadRequest, "invalid script name")
}

func TestRunStreamMissingScriptReturns404(t *testing.T) {
	server := makeServer(t, nil)
	rec := httptest.NewRecorder()

	server.handler.ServeHTTP(rec, postRunStream("missing.bat"))

	assertErrorResponse(t, rec, http.StatusNotFound, "script not found")
}

func TestRunStreamBusyReturns409(t *testing.T) {
	server := makeServer(t, map[string]string{"busy.bat": "@echo off\r\necho busy\r\n"})
	entry, err := server.reg.Lookup("busy.bat")
	if err != nil {
		t.Fatal(err)
	}
	if !entry.TryLock() {
		t.Fatal("expected test to acquire script lock")
	}
	defer entry.Unlock()

	rec := httptest.NewRecorder()
	server.handler.ServeHTTP(rec, postRunStream("busy.bat"))

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Error  string `json:"error"`
		Script string `json:"script"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error != "script is already running" {
		t.Fatalf("error = %q, want script is already running", body.Error)
	}
	if body.Script != "busy.bat" {
		t.Fatalf("script = %q, want busy.bat", body.Script)
	}
}
```

- [ ] **Step 3: Add successful and non-zero streaming tests**

Append:

```go
func TestRunStreamReturnsOutputAndFinalMessages(t *testing.T) {
	server := makeServer(t, map[string]string{"hello.bat": "@echo off\r\necho hello\r\n"})
	rec := httptest.NewRecorder()

	server.handler.ServeHTTP(rec, postRunStream("hello.bat"))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/x-ndjson") {
		t.Fatalf("Content-Type = %q, want application/x-ndjson", got)
	}

	messages := decodeNDJSON(t, rec.Body.String())
	if len(messages) < 2 {
		t.Fatalf("expected output and final messages, got %d body=%s", len(messages), rec.Body.String())
	}

	first := messages[0]
	if rawString(t, first, "type") != "output" {
		t.Fatalf("first type = %q, want output", rawString(t, first, "type"))
	}
	if rawString(t, first, "script") != "hello.bat" {
		t.Fatalf("first script = %q, want hello.bat", rawString(t, first, "script"))
	}
	if rawString(t, first, "stream") != "stdout" {
		t.Fatalf("first stream = %q, want stdout", rawString(t, first, "stream"))
	}
	if !strings.Contains(rawString(t, first, "data"), "hello") {
		t.Fatalf("first data = %q, want it to contain hello", rawString(t, first, "data"))
	}

	final := messages[len(messages)-1]
	if rawString(t, final, "type") != "final" {
		t.Fatalf("final type = %q, want final", rawString(t, final, "type"))
	}
	if rawInt(t, final, "exitCode") != 0 {
		t.Fatalf("exitCode = %d, want 0", rawInt(t, final, "exitCode"))
	}
	if rawBool(t, final, "timedOut") {
		t.Fatal("timedOut = true, want false")
	}
	for _, key := range []string{"startedAt", "finishedAt", "durationMs"} {
		if _, ok := final[key]; !ok {
			t.Fatalf("final missing key %s in %#v", key, final)
		}
	}
	if _, ok := final["error"]; ok {
		t.Fatalf("successful final message must not include error: %#v", final)
	}
}

func TestRunStreamNonZeroExitCodeFinalHasNoError(t *testing.T) {
	server := makeServer(t, map[string]string{"fail.bat": "@echo off\r\nexit /b 7\r\n"})
	rec := httptest.NewRecorder()

	server.handler.ServeHTTP(rec, postRunStream("fail.bat"))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	messages := decodeNDJSON(t, rec.Body.String())
	final := messages[len(messages)-1]
	if rawString(t, final, "type") != "final" {
		t.Fatalf("final type = %q, want final", rawString(t, final, "type"))
	}
	if rawInt(t, final, "exitCode") != 7 {
		t.Fatalf("exitCode = %d, want 7", rawInt(t, final, "exitCode"))
	}
	if _, ok := final["error"]; ok {
		t.Fatalf("non-zero script exit must not include error: %#v", final)
	}
}
```

- [ ] **Step 4: Run the new tests and verify failure**

Run:

```cmd
go test ./internal/httpapi
```

Expected: tests fail with `expected 401, got 404` or equivalent because `/run/stream` is not registered yet.

- [ ] **Step 5: Commit failing tests only after the implementation task passes**

Do not commit now. These tests are committed with Task 2 after they pass.

---

### Task 2: Implement POST /run/stream

**Files:**

```text
Modify: D:\Code\bat-agent\internal\httpapi\handler.go
Modify: D:\Code\bat-agent\internal\httpapi\handler_test.go
```

- [ ] **Step 1: Register the route**

Modify `Routes` in `D:\Code\bat-agent\internal\httpapi\handler.go`:

```go
func (s *Server) Routes(authWrap func(http.Handler) http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.handleHealth)
	mux.Handle("/scripts", authWrap(http.HandlerFunc(s.handleScripts)))
	mux.Handle("/run", authWrap(http.HandlerFunc(s.handleRun)))
	mux.Handle("/run/stream", authWrap(http.HandlerFunc(s.handleRunStream)))
	return accessLog(mux)
}
```

- [ ] **Step 2: Add stream response types**

Add these types after `runResponse`:

```go
type streamOutputResponse struct {
	Type   string `json:"type"`
	Script string `json:"script"`
	Stream string `json:"stream"`
	Data   string `json:"data"`
}

type streamFinalResponse struct {
	Type       string    `json:"type"`
	Script     string    `json:"script"`
	ExitCode   int       `json:"exitCode"`
	TimedOut   bool      `json:"timedOut"`
	StartedAt  time.Time `json:"startedAt"`
	FinishedAt time.Time `json:"finishedAt"`
	DurationMs int64     `json:"durationMs"`
	Error      string    `json:"error,omitempty"`
}

type streamRunResult struct {
	result executor.Result
	err    error
}
```

- [ ] **Step 3: Add the streaming handler**

Add this handler to `D:\Code\bat-agent\internal\httpapi\handler.go`:

```go
func (s *Server) handleRunStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req runRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}

	outputs := make(chan streamOutputResponse, 64)
	done := make(chan streamRunResult, 1)
	go func() {
		result, err := s.exec.RunStream(context.Background(), req.Script, func(chunk executor.OutputChunk) {
			outputs <- streamOutputResponse{
				Type:   "output",
				Script: req.Script,
				Stream: chunk.Stream,
				Data:   chunk.Data,
			}
		})
		done <- streamRunResult{result: result, err: err}
		close(outputs)
	}()

	var enc *json.Encoder
	streaming := false
	startStream := func() {
		if streaming {
			return
		}
		w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		enc = json.NewEncoder(w)
		streaming = true
	}

	for {
		select {
		case out, ok := <-outputs:
			if !ok {
				outputs = nil
				continue
			}
			startStream()
			if err := enc.Encode(out); err != nil {
				go drainStream(outputs, done)
				return
			}
			flush(w)
		case run := <-done:
			if !streaming && writePreflightStreamError(w, run.result, run.err) {
				return
			}
			startStream()
			for out := range outputs {
				if err := enc.Encode(out); err != nil {
					go drainOutputs(outputs)
					return
				}
				flush(w)
			}
			_ = enc.Encode(streamFinalFromResult(run.result, run.err))
			flush(w)
			return
		}
	}
}
```

- [ ] **Step 4: Add stream helper functions**

Add these helpers:

```go
func drainStream(outputs <-chan streamOutputResponse, done <-chan streamRunResult) {
	for outputs != nil || done != nil {
		select {
		case _, ok := <-outputs:
			if !ok {
				outputs = nil
			}
		case <-done:
			done = nil
		}
	}
}

func drainOutputs(outputs <-chan streamOutputResponse) {
	for range outputs {
	}
}

func writePreflightStreamError(w http.ResponseWriter, result executor.Result, err error) bool {
	switch {
	case errors.Is(err, executor.ErrInvalidScriptName):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": executor.StableError(err)})
		return true
	case errors.Is(err, executor.ErrScriptNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": executor.StableError(err)})
		return true
	case errors.Is(err, executor.ErrScriptBusy):
		writeJSON(w, http.StatusConflict, map[string]string{
			"error":  executor.StableError(err),
			"script": result.Script,
		})
		return true
	default:
		return false
	}
}

func streamFinalFromResult(result executor.Result, err error) streamFinalResponse {
	resp := streamFinalResponse{
		Type:       "final",
		Script:     result.Script,
		ExitCode:   result.ExitCode,
		TimedOut:   result.TimedOut,
		StartedAt:  result.StartedAt,
		FinishedAt: result.FinishedAt,
		DurationMs: result.FinishedAt.Sub(result.StartedAt).Milliseconds(),
	}
	if err != nil {
		resp.Error = executor.StableError(err)
	}
	return resp
}

func flush(w http.ResponseWriter) {
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}
```

- [ ] **Step 5: Run HTTP tests**

Run:

```cmd
gofmt -w internal/httpapi
go test ./internal/httpapi
```

Expected: all `internal/httpapi` tests pass.

- [ ] **Step 6: Commit backend streaming API**

Commit:

```cmd
git add internal/httpapi/handler.go internal/httpapi/handler_test.go
git commit -m "feat: add streaming run endpoint"
```

---

### Task 3: Add GUI Config Persistence

**Files:**

```text
Create: D:\Code\bat-agent\internal\gui\guiconfig\config.go
Create: D:\Code\bat-agent\internal\gui\guiconfig\config_test.go
```

- [ ] **Step 1: Write config tests**

Create `D:\Code\bat-agent\internal\gui\guiconfig\config_test.go`:

```go
package guiconfig

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultConfig(t *testing.T) {
	cfg := Default()

	if cfg.Mode != ModeLocal {
		t.Fatalf("Mode = %q, want %q", cfg.Mode, ModeLocal)
	}
	if cfg.BaseURL != "http://127.0.0.1:8080" {
		t.Fatalf("BaseURL = %q, want local default", cfg.BaseURL)
	}
	if cfg.Username != "admin" {
		t.Fatalf("Username = %q, want admin", cfg.Username)
	}
}

func TestSaveAndLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	want := Config{
		Mode:     ModeRemote,
		BaseURL:  "http://10.0.0.5:8080",
		Username: "alice",
		Password: "secret-password",
	}

	if err := Save(path, want); err != nil {
		t.Fatalf("Save returned error: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if got != want {
		t.Fatalf("Load = %#v, want %#v", got, want)
	}
}

func TestLoadMissingFileReturnsDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.json")

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if got != Default() {
		t.Fatalf("Load missing = %#v, want default %#v", got, Default())
	}
}

func TestPathUsesUserConfigDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("APPDATA", dir)

	got, err := Path()
	if err != nil {
		t.Fatalf("Path returned error: %v", err)
	}
	want := filepath.Join(dir, "deploy-agent-gui", "config.json")
	if got != want {
		t.Fatalf("Path = %q, want %q", got, want)
	}
}

func TestSaveCreatesParentDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.json")

	if err := Save(path, Default()); err != nil {
		t.Fatalf("Save returned error: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected config file to exist: %v", err)
	}
}
```

- [ ] **Step 2: Run tests and verify failure**

Run:

```cmd
go test ./internal/gui/guiconfig
```

Expected: failure because the package does not exist.

- [ ] **Step 3: Implement config package**

Create `D:\Code\bat-agent\internal\gui\guiconfig\config.go`:

```go
package guiconfig

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

const (
	ModeLocal  = "local"
	ModeRemote = "remote"
)

type Config struct {
	Mode     string `json:"mode"`
	BaseURL  string `json:"baseUrl"`
	Username string `json:"username"`
	Password string `json:"password"`
}

func Default() Config {
	return Config{
		Mode:     ModeLocal,
		BaseURL:  "http://127.0.0.1:8080",
		Username: "admin",
	}
}

func Path() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "deploy-agent-gui", "config.json"), nil
}

func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Default(), nil
	}
	if err != nil {
		return Config{}, err
	}

	cfg := Default()
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, err
	}
	if cfg.Mode == "" {
		cfg.Mode = ModeLocal
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = "http://127.0.0.1:8080"
	}
	return cfg, nil
}

func Save(path string, cfg Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o600)
}
```

- [ ] **Step 4: Run config tests**

Run:

```cmd
gofmt -w internal/gui/guiconfig
go test ./internal/gui/guiconfig
```

Expected: tests pass.

- [ ] **Step 5: Commit GUI config**

Commit:

```cmd
git add internal/gui/guiconfig
git commit -m "feat: add gui config persistence"
```

---

### Task 4: Add GUI HTTP Client And NDJSON Parser

**Files:**

```text
Create: D:\Code\bat-agent\internal\gui\apiclient\client.go
Create: D:\Code\bat-agent\internal\gui\apiclient\client_test.go
```

- [ ] **Step 1: Write client tests**

Create `D:\Code\bat-agent\internal\gui\apiclient\client_test.go`:

```go
package apiclient

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHealthCallsHealthEndpoint(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()

	client := New(server.URL, "admin", "password")
	if err := client.Health(context.Background()); err != nil {
		t.Fatalf("Health returned error: %v", err)
	}
	if gotPath != "/health" {
		t.Fatalf("path = %q, want /health", gotPath)
	}
}

func TestScriptsUsesBasicAuth(t *testing.T) {
	wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("admin:password"))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != wantAuth {
			t.Fatalf("Authorization = %q, want %q", r.Header.Get("Authorization"), wantAuth)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"scripts":["a.bat","b.cmd"]}`))
	}))
	defer server.Close()

	client := New(server.URL, "admin", "password")
	scripts, err := client.Scripts(context.Background())
	if err != nil {
		t.Fatalf("Scripts returned error: %v", err)
	}
	if strings.Join(scripts, ",") != "a.bat,b.cmd" {
		t.Fatalf("scripts = %#v", scripts)
	}
}

func TestRunStreamReadsOutputAndFinal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/run/stream" {
			t.Fatalf("path = %q, want /run/stream", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
		fmt.Fprintln(w, `{"type":"output","script":"a.bat","stream":"stdout","data":"hello\r\n"}`)
		fmt.Fprintln(w, `{"type":"final","script":"a.bat","exitCode":0,"timedOut":false,"startedAt":"2026-06-10T10:00:00+08:00","finishedAt":"2026-06-10T10:00:01+08:00","durationMs":1000}`)
	}))
	defer server.Close()

	client := New(server.URL, "admin", "password")
	var events []StreamEvent
	err := client.RunStream(context.Background(), "a.bat", func(event StreamEvent) {
		events = append(events, event)
	})
	if err != nil {
		t.Fatalf("RunStream returned error: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("events len = %d, want 2", len(events))
	}
	if events[0].Type != EventOutput || events[0].Stream != "stdout" || !strings.Contains(events[0].Data, "hello") {
		t.Fatalf("unexpected output event: %#v", events[0])
	}
	if events[1].Type != EventFinal || events[1].ExitCode != 0 || events[1].TimedOut {
		t.Fatalf("unexpected final event: %#v", events[1])
	}
}

func TestRunStreamMapsHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"script not found"}`))
	}))
	defer server.Close()

	client := New(server.URL, "admin", "password")
	err := client.RunStream(context.Background(), "missing.bat", func(event StreamEvent) {})
	if err == nil {
		t.Fatal("RunStream returned nil error, want HTTPError")
	}
	httpErr, ok := err.(HTTPError)
	if !ok {
		t.Fatalf("error type = %T, want HTTPError", err)
	}
	if httpErr.StatusCode != http.StatusNotFound || httpErr.Message != "script not found" {
		t.Fatalf("HTTPError = %#v", httpErr)
	}
}
```

- [ ] **Step 2: Run tests and verify failure**

Run:

```cmd
go test ./internal/gui/apiclient
```

Expected: failure because the package does not exist.

- [ ] **Step 3: Implement client package**

Create `D:\Code\bat-agent\internal\gui\apiclient\client.go`:

```go
package apiclient

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const (
	EventOutput = "output"
	EventFinal  = "final"
)

type Client struct {
	baseURL    string
	username   string
	password   string
	httpClient *http.Client
}

type StreamEvent struct {
	Type       string    `json:"type"`
	Script     string    `json:"script"`
	Stream     string    `json:"stream,omitempty"`
	Data       string    `json:"data,omitempty"`
	ExitCode   int       `json:"exitCode,omitempty"`
	TimedOut   bool      `json:"timedOut,omitempty"`
	StartedAt  time.Time `json:"startedAt,omitempty"`
	FinishedAt time.Time `json:"finishedAt,omitempty"`
	DurationMs int64     `json:"durationMs,omitempty"`
	Error      string    `json:"error,omitempty"`
}

type HTTPError struct {
	StatusCode int
	Message    string
}

func (e HTTPError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("HTTP %d", e.StatusCode)
	}
	return fmt.Sprintf("HTTP %d: %s", e.StatusCode, e.Message)
}

func New(baseURL, username, password string) *Client {
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		username:   username,
		password:   password,
		httpClient: &http.Client{},
	}
}

func (c *Client) Health(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/health", nil)
	if err != nil {
		return err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return HTTPError{StatusCode: resp.StatusCode}
	}
	return nil
}

func (c *Client) Scripts(ctx context.Context) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/scripts", nil)
	if err != nil {
		return nil, err
	}
	c.setAuth(req)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, decodeHTTPError(resp)
	}
	var body struct {
		Scripts []string `json:"scripts"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	return body.Scripts, nil
}

func (c *Client) RunStream(ctx context.Context, script string, onEvent func(StreamEvent)) error {
	body, err := json.Marshal(map[string]string{"script": script})
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
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var event StreamEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			return err
		}
		onEvent(event)
	}
	return scanner.Err()
}

func (c *Client) setAuth(req *http.Request) {
	req.SetBasicAuth(c.username, c.password)
}

func decodeHTTPError(resp *http.Response) error {
	var body struct {
		Error string `json:"error"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	return HTTPError{StatusCode: resp.StatusCode, Message: body.Error}
}
```

- [ ] **Step 4: Run client tests**

Run:

```cmd
gofmt -w internal/gui/apiclient
go test ./internal/gui/apiclient
```

Expected: tests pass.

- [ ] **Step 5: Commit API client**

Commit:

```cmd
git add internal/gui/apiclient
git commit -m "feat: add gui api client"
```

---

### Task 5: Add Local Service Process Manager

**Files:**

```text
Create: D:\Code\bat-agent\internal\gui\localservice\service.go
Create: D:\Code\bat-agent\internal\gui\localservice\service_test.go
```

- [ ] **Step 1: Write local service tests**

Create `D:\Code\bat-agent\internal\gui\localservice\service_test.go`:

```go
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
}
```

- [ ] **Step 2: Run tests and verify failure**

Run:

```cmd
go test ./internal/gui/localservice
```

Expected: failure because the package does not exist.

- [ ] **Step 3: Implement local service manager**

Create `D:\Code\bat-agent\internal\gui\localservice\service.go`:

```go
package localservice

import (
	"context"
	"os/exec"
	"path/filepath"
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

func (m *Manager) Stop() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.cmd == nil || m.cmd.Process == nil {
		return nil
	}
	err := m.cmd.Process.Kill()
	m.cmd = nil
	return err
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
```

- [ ] **Step 4: Run local service tests**

Run:

```cmd
gofmt -w internal/gui/localservice
go test ./internal/gui/localservice
```

Expected: tests pass.

- [ ] **Step 5: Commit local service manager**

Commit:

```cmd
git add internal/gui/localservice
git commit -m "feat: add local service manager"
```

---

### Task 6: Add Fyne GUI Entry Point

**Files:**

```text
Create: D:\Code\bat-agent\cmd\deploy-agent-gui\main.go
Modify: D:\Code\bat-agent\go.mod
Modify: D:\Code\bat-agent\go.sum
```

- [ ] **Step 1: Add Fyne dependency**

Run:

```cmd
go get fyne.io/fyne/v2@latest
```

Expected: `go.mod` and `go.sum` include Fyne v2 and its transitive dependencies.

- [ ] **Step 2: Create GUI main**

Create `D:\Code\bat-agent\cmd\deploy-agent-gui\main.go`:

```go
package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/data/binding"
	"fyne.io/fyne/v2/widget"
	"github.com/liqixin/deploy-agent/internal/gui/apiclient"
	"github.com/liqixin/deploy-agent/internal/gui/guiconfig"
	"github.com/liqixin/deploy-agent/internal/gui/localservice"
)

type guiState struct {
	configPath string
	config     guiconfig.Config
	client     *apiclient.Client
	service    *localservice.Manager

	statusText binding.String
	outputText binding.String
	history    binding.StringList

	scriptSelect *widget.Select
	runButton    *widget.Button
}

func main() {
	a := app.New()
	w := a.NewWindow("deploy-agent 管理控制台")
	w.Resize(fyne.NewSize(980, 680))

	cfgPath, err := guiconfig.Path()
	if err != nil {
		cfgPath = "deploy-agent-gui.json"
	}
	cfg, err := guiconfig.Load(cfgPath)
	if err != nil {
		cfg = guiconfig.Default()
	}

	exe, _ := os.Executable()
	state := &guiState{
		configPath: cfgPath,
		config:     cfg,
		service:    localservice.New(localservice.AgentPath(exe)),
		statusText: binding.NewString(),
		outputText: binding.NewString(),
		history:    binding.NewStringList(),
	}
	state.setStatus("未连接")

	content := state.buildUI()
	w.SetContent(content)
	w.ShowAndRun()
}

func (s *guiState) buildUI() fyne.CanvasObject {
	mode := widget.NewSelect([]string{guiconfig.ModeLocal, guiconfig.ModeRemote}, func(value string) {
		s.config.Mode = value
	})
	mode.Selected = s.config.Mode

	baseURL := widget.NewEntry()
	baseURL.SetText(s.config.BaseURL)
	baseURL.OnChanged = func(value string) {
		s.config.BaseURL = value
	}

	username := widget.NewEntry()
	username.SetText(s.config.Username)
	username.OnChanged = func(value string) {
		s.config.Username = value
	}

	password := widget.NewPasswordEntry()
	password.SetText(s.config.Password)
	password.OnChanged = func(value string) {
		s.config.Password = value
	}

	connect := widget.NewButton("连接", func() {
		s.connect()
	})
	save := widget.NewButton("保存配置", func() {
		if err := guiconfig.Save(s.configPath, s.config); err != nil {
			s.setStatus("保存配置失败: " + err.Error())
			return
		}
		s.setStatus("配置已保存")
	})
	startLocal := widget.NewButton("启动服务", func() {
		s.startLocalService()
	})
	stopLocal := widget.NewButton("停止服务", func() {
		s.stopLocalService()
	})

	s.scriptSelect = widget.NewSelect([]string{}, func(value string) {})
	refresh := widget.NewButton("刷新脚本", func() {
		s.refreshScripts()
	})
	s.runButton = widget.NewButton("执行脚本", func() {
		s.runSelectedScript()
	})
	s.runButton.Disable()

	status := widget.NewLabelWithData(s.statusText)
	output := widget.NewMultiLineEntry()
	output.Bind(s.outputText)
	output.Wrapping = fyne.TextWrapOff
	output.Disable()

	history := widget.NewListWithData(
		s.history,
		func() fyne.CanvasObject {
			return widget.NewLabel("")
		},
		func(item binding.DataItem, obj fyne.CanvasObject) {
			text, _ := item.(binding.String).Get()
			obj.(*widget.Label).SetText(text)
		},
	)

	connectionForm := widget.NewForm(
		widget.NewFormItem("模式", mode),
		widget.NewFormItem("服务地址", baseURL),
		widget.NewFormItem("用户名", username),
		widget.NewFormItem("密码", password),
	)
	top := container.NewBorder(nil, nil, nil, container.NewHBox(connect, save, startLocal, stopLocal), connectionForm)
	scripts := container.NewBorder(widget.NewLabel("脚本"), container.NewHBox(refresh, s.runButton), nil, nil, s.scriptSelect)
	runInfo := container.NewBorder(status, nil, nil, nil, output)
	mainSplit := container.NewHSplit(scripts, runInfo)
	mainSplit.Offset = 0.25

	return container.NewBorder(top, container.NewVBox(widget.NewLabel("最近执行"), history), nil, nil, mainSplit)
}

func (s *guiState) connect() {
	s.client = apiclient.New(s.config.BaseURL, s.config.Username, s.config.Password)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.client.Health(ctx); err != nil {
		s.setStatus("连接失败: " + err.Error())
		return
	}
	s.setStatus("已连接")
	s.refreshScripts()
}

func (s *guiState) refreshScripts() {
	if s.client == nil {
		s.setStatus("请先连接服务")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	scripts, err := s.client.Scripts(ctx)
	if err != nil {
		s.setStatus("刷新脚本失败: " + err.Error())
		return
	}
	s.scriptSelect.Options = scripts
	if len(scripts) > 0 && s.scriptSelect.Selected == "" {
		s.scriptSelect.SetSelected(scripts[0])
	}
	s.scriptSelect.Refresh()
	s.runButton.Enable()
	s.setStatus(fmt.Sprintf("已加载 %d 个脚本", len(scripts)))
}

func (s *guiState) runSelectedScript() {
	if s.client == nil {
		s.setStatus("请先连接服务")
		return
	}
	script := s.scriptSelect.Selected
	if script == "" {
		s.setStatus("请选择脚本")
		return
	}
	s.runButton.Disable()
	_ = s.outputText.Set("")
	s.setStatus("运行中: " + script)

	go func() {
		ctx := context.Background()
		err := s.client.RunStream(ctx, script, func(event apiclient.StreamEvent) {
			fyne.Do(func() {
				s.handleEvent(event)
			})
		})
		if err != nil {
			fyne.Do(func() {
				s.appendOutput("[error] " + err.Error() + "\r\n")
				s.setStatus("请求失败: " + err.Error())
				s.addHistory(script + " 请求失败")
			})
		}
		fyne.Do(func() {
			s.runButton.Enable()
		})
	}()
}

func (s *guiState) handleEvent(event apiclient.StreamEvent) {
	if event.Type == apiclient.EventOutput {
		prefix := "[stdout] "
		if event.Stream == "stderr" {
			prefix = "[stderr] "
		}
		s.appendOutput(prefix + event.Data)
		return
	}
	if event.Type == apiclient.EventFinal {
		status := "成功"
		if event.TimedOut {
			status = "超时"
		} else if event.Error != "" {
			status = "请求失败"
		} else if event.ExitCode != 0 {
			status = "脚本失败"
		}
		s.setStatus(fmt.Sprintf("%s: %s exitCode=%d durationMs=%d", status, event.Script, event.ExitCode, event.DurationMs))
		s.addHistory(fmt.Sprintf("%s %s exitCode=%d durationMs=%d", event.Script, status, event.ExitCode, event.DurationMs))
	}
}

func (s *guiState) startLocalService() {
	if s.config.Mode != guiconfig.ModeLocal {
		s.setStatus("远程模式不管理本机服务")
		return
	}
	if err := s.service.Start(context.Background()); err != nil {
		s.setStatus("启动服务失败: " + err.Error())
		return
	}
	s.setStatus(fmt.Sprintf("服务已启动 PID=%d", s.service.PID()))
}

func (s *guiState) stopLocalService() {
	if err := s.service.Stop(); err != nil {
		s.setStatus("停止服务失败: " + err.Error())
		return
	}
	s.setStatus("服务已停止")
}

func (s *guiState) setStatus(value string) {
	_ = s.statusText.Set(value)
}

func (s *guiState) appendOutput(value string) {
	current, _ := s.outputText.Get()
	_ = s.outputText.Set(current + value)
}

func (s *guiState) addHistory(value string) {
	items, _ := s.history.Get()
	items = append([]string{strings.TrimSpace(value)}, items...)
	if len(items) > 20 {
		items = items[:20]
	}
	_ = s.history.Set(items)
}
```

- [ ] **Step 3: Compile GUI**

Run:

```cmd
gofmt -w cmd/deploy-agent-gui
go test ./internal/gui/...
go test ./cmd/deploy-agent-gui
```

Expected: packages compile and GUI support tests pass.

- [ ] **Step 4: Commit GUI entry**

Commit:

```cmd
git add go.mod go.sum cmd/deploy-agent-gui
git commit -m "feat: add fyne gui application"
```

---

### Task 7: Update Build Script For Both Executables

**Files:**

```text
Modify: D:\Code\bat-agent\build.bat
```

- [ ] **Step 1: Modify build script**

Update `D:\Code\bat-agent\build.bat` so the build section becomes:

```bat
echo Building deploy-agent.exe...
set GOOS=windows
set CGO_ENABLED=0
go build -ldflags "-s -w" -o deploy-agent.exe . || goto :error

echo Building deploy-agent-gui.exe...
set CGO_ENABLED=1
go build -ldflags "-s -w" -o deploy-agent-gui.exe .\cmd\deploy-agent-gui || goto :error

echo Done: deploy-agent.exe deploy-agent-gui.exe
exit /b 0
```

Keep the existing `rsrc` install and manifest embedding logic above this section unchanged.

- [ ] **Step 2: Run build script**

Run:

```cmd
build.bat
```

Expected:

```text
Embedding manifest...
Building deploy-agent.exe...
Building deploy-agent-gui.exe...
Done: deploy-agent.exe deploy-agent-gui.exe
```

- [ ] **Step 3: Commit build update**

Commit:

```cmd
git add build.bat
git commit -m "build: build gui executable"
```

---

### Task 8: Update README For GUI And Streaming API

**Files:**

```text
Modify: D:\Code\bat-agent\README.md
```

- [ ] **Step 1: Document /run/stream**

Add this section after `POST /run`:

```markdown
### `POST /run/stream`

需要 Basic Auth。请求体与 `/run` 一致：

```json
{"script":"deploy.bat"}
```

响应是 NDJSON，每一行一个 JSON：

```json
{"type":"output","script":"deploy.bat","stream":"stdout","data":"开始部署...\r\n"}
{"type":"output","script":"deploy.bat","stream":"stderr","data":"warning...\r\n"}
{"type":"final","script":"deploy.bat","exitCode":0,"timedOut":false,"startedAt":"2026-06-10T10:00:00+08:00","finishedAt":"2026-06-10T10:00:03+08:00","durationMs":3142}
```

一旦进入流式响应，HTTP 状态码保持 `200`。脚本超时、非零退出码或 runner 错误通过最后一条 `type: "final"` 判断。调度前错误仍返回普通 JSON 错误和对应 HTTP 状态码。
```

- [ ] **Step 2: Document GUI**

Add this section before `## 安全说明`:

```markdown
## GUI 管理端

`deploy-agent-gui.exe` 是独立 Windows 桌面程序，用于连接和管理 `deploy-agent`。

本机模式：

- 将 `deploy-agent-gui.exe` 与 `deploy-agent.exe` 放在同一目录。
- GUI 会启动或停止同目录的 `deploy-agent.exe`。
- 如果服务不是由 GUI 启动，第一版不会强制结束未知进程。

远程模式：

- 填写远程服务地址、HTTP Basic Auth 用户名和密码。
- GUI 通过 `/scripts` 列出脚本，通过 `/run/stream` 执行脚本并实时显示输出。

GUI 会把服务地址、用户名和密码保存到本地配置文件，默认在用户配置目录下的 `deploy-agent-gui/config.json`。这是便捷存储，不是强安全存储；请保护该文件权限，不要提交或分享它。

第一版限制：

- 不支持脚本参数。
- 不支持中止正在运行的脚本。
- 不持久化执行历史。
- 不提供系统托盘。
```

- [ ] **Step 3: Run docs-adjacent tests**

Run:

```cmd
go test ./...
```

Expected: tests pass.

- [ ] **Step 4: Commit README**

Commit:

```cmd
git add README.md
git commit -m "docs: document gui console"
```

---

### Task 9: Final Verification

**Files:**

```text
Verify entire repository.
```

- [ ] **Step 1: Format code**

Run:

```cmd
gofmt -w .
```

Expected: exits 0.

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

Expected: exits 0 with no diagnostics.

- [ ] **Step 4: Build both executables**

Run:

```cmd
build.bat
```

Expected:

```text
Done: deploy-agent.exe deploy-agent-gui.exe
```

- [ ] **Step 5: Manual GUI smoke test on Windows**

Run:

```cmd
deploy-agent-gui.exe
```

Expected manual result:

```text
The GUI opens.
Local mode shows the default URL http://127.0.0.1:8080.
Saving config creates %AppData%\deploy-agent-gui\config.json.
Starting local service runs the same-directory deploy-agent.exe.
Connecting succeeds after deploy-agent is healthy.
Refreshing scripts shows .bat and .cmd files from the configured script directory.
Running a script appends stdout/stderr text and ends with a final status.
```

- [ ] **Step 6: Commit final adjustments if verification changed tracked files**

If verification required source or docs changes:

```cmd
git add .
git commit -m "chore: finalize gui console"
```

If no tracked files changed, do not create an empty commit.

---

## Self-Review

Spec coverage:

```text
Independent Windows GUI: Tasks 3 through 7.
Go + Fyne: Task 6.
Remote connection: Task 4 and Task 6.
Local same-directory deploy-agent.exe management: Task 5 and Task 6.
Saved address, username, password: Task 3 and Task 6.
Script listing and refresh: Task 4 and Task 6.
Script execution with real-time stdout/stderr: Tasks 1, 2, 4, and 6.
New HTTP streaming API: Tasks 1 and 2.
Existing HTTP and MQTT behavior preserved: Tasks 1, 2, 8, and 9.
README updates: Task 8.
Verification: Task 9.
```

Implementation guardrails:

```text
Do not change /run response fields.
Do not remove Basic Auth from /scripts, /run, or /run/stream.
Do not add script parameters.
Do not add script cancellation controls.
Do not make GUI execute scripts directly.
Do not persist run history.
Do not commit local GUI config files.
Do not commit generated deploy-agent.exe, deploy-agent-gui.exe, or resource.syso.
```
