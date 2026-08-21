package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/liqixin/deploy-agent/internal/auth"
	"github.com/liqixin/deploy-agent/internal/downloadtask"
	"github.com/liqixin/deploy-agent/internal/executor"
	"github.com/liqixin/deploy-agent/internal/registry"
	"github.com/liqixin/deploy-agent/internal/releasecatalog"
)

type testServer struct {
	handler http.Handler
	reg     *registry.Registry
}

type flushRecorder struct {
	header  http.Header
	flushed bool
}

func (f *flushRecorder) Header() http.Header {
	if f.header == nil {
		f.header = http.Header{}
	}
	return f.header
}

func (f *flushRecorder) Write(b []byte) (int, error) {
	return len(b), nil
}

func (f *flushRecorder) WriteHeader(int) {}

func (f *flushRecorder) Flush() {
	f.flushed = true
}

func makeServer(t *testing.T, files map[string]string) testServer {
	t.Helper()
	return makeServerWithTimeout(t, files, 5*time.Second)
}

func makeServerWithTimeout(t *testing.T, files map[string]string, timeout time.Duration) testServer {
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
	exec := executor.New(reg, timeout)
	api := New(exec)
	return testServer{
		handler: api.Routes(func(h http.Handler) http.Handler {
			return auth.BasicAuth("admin", "change-me-please", h)
		}),
		reg: reg,
	}
}

func makeServerWithPreDownload(t *testing.T, files map[string]string, timeout time.Duration, downloadBody string, downloadTimeout time.Duration) testServer {
	t.Helper()

	dir := t.TempDir()
	download := filepath.Join(dir, "download.bat")
	if err := os.WriteFile(download, []byte(downloadBody), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	reg, err := registry.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	exec := executor.New(reg, timeout, executor.WithPreDownloadConfig(executor.PreDownloadConfig{
		ScriptPath: download,
		Timeout:    downloadTimeout,
	}))
	api := New(exec)
	return testServer{
		handler: api.Routes(func(h http.Handler) http.Handler {
			return auth.BasicAuth("admin", "change-me-please", h)
		}),
		reg: reg,
	}
}

func authHeader() string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte("admin:change-me-please"))
}

func postRun(script string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/run", bytes.NewBufferString(`{"script":`+quoteJSON(script)+`}`))
	req.Header.Set("Authorization", authHeader())
	req.Header.Set("Content-Type", "application/json")
	return req
}

func postRunBody(body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/run", bytes.NewBufferString(body))
	req.Header.Set("Authorization", authHeader())
	req.Header.Set("Content-Type", "application/json")
	return req
}

func postRunStream(script string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/run/stream", bytes.NewBufferString(`{"script":`+quoteJSON(script)+`}`))
	req.Header.Set("Authorization", authHeader())
	req.Header.Set("Content-Type", "application/json")
	return req
}

func quoteJSON(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
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

func TestHealthDoesNotRequireAuth(t *testing.T) {
	server := makeServer(t, nil)
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()

	server.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestRunRequiresAuth(t *testing.T) {
	server := makeServer(t, map[string]string{"hello.bat": "@echo off\r\necho hello\r\n"})
	req := httptest.NewRequest(http.MethodPost, "/run", bytes.NewBufferString(`{"script":"hello.bat"}`))
	rec := httptest.NewRecorder()

	server.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestScriptsRequiresAuth(t *testing.T) {
	server := makeServer(t, map[string]string{"hello.bat": "@echo off\r\necho hello\r\n"})
	req := httptest.NewRequest(http.MethodGet, "/scripts", nil)
	rec := httptest.NewRecorder()

	server.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestRunReturnsExistingResponseShape(t *testing.T) {
	server := makeServer(t, map[string]string{"hello.bat": "@echo off\r\necho hello\r\n"})
	rec := httptest.NewRecorder()

	server.handler.ServeHTTP(rec, postRun("hello.bat"))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"script", "exitCode", "stdout", "stderr", "startedAt", "finishedAt", "durationMs"} {
		if _, ok := body[key]; !ok {
			t.Fatalf("missing key %s in %#v", key, body)
		}
	}
	for _, key := range []string{"done", "error", "timedOut"} {
		if _, ok := body[key]; ok {
			t.Fatalf("unexpected key %s in %#v", key, body)
		}
	}
	var stdout string
	if err := json.Unmarshal(body["stdout"], &stdout); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "hello") {
		t.Fatalf("stdout = %q, want it to contain hello", stdout)
	}
}

func TestRunNonZeroExitCodeStillReturnsHTTP200(t *testing.T) {
	server := makeServer(t, map[string]string{"fail.bat": "@echo off\r\nexit /b 7\r\n"})
	rec := httptest.NewRecorder()

	server.handler.ServeHTTP(rec, postRun("fail.bat"))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	var exitCode int
	if err := json.Unmarshal(body["exitCode"], &exitCode); err != nil {
		t.Fatal(err)
	}
	if exitCode != 7 {
		t.Fatalf("expected exitCode 7, got %d", exitCode)
	}
	if _, ok := body["error"]; ok {
		t.Fatalf("ordinary script exit must not include error field: %#v", body)
	}
}

func TestRunScriptTimeoutReturnsStableError(t *testing.T) {
	server := makeServerWithTimeout(t, map[string]string{"slow.bat": "@echo off\r\nping -n 3 127.0.0.1 >nul\r\n"}, 20*time.Millisecond)
	rec := httptest.NewRecorder()

	server.handler.ServeHTTP(rec, postRun("slow.bat"))

	if rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("expected 504, got %d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		TimedOut bool   `json:"timedOut"`
		Error    string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.TimedOut {
		t.Fatal("timedOut = false, want true")
	}
	if body.Error != "script timed out" {
		t.Fatalf("error = %q, want script timed out", body.Error)
	}
}

func TestRunPreDownloadTimeoutReturnsStableError(t *testing.T) {
	server := makeServerWithPreDownload(
		t,
		map[string]string{"deploy.bat": "@echo off\r\necho target\r\n"},
		5*time.Second,
		"@echo off\r\nping -n 3 127.0.0.1 >nul\r\n",
		20*time.Millisecond,
	)
	rec := httptest.NewRecorder()

	server.handler.ServeHTTP(rec, postRunBody(`{"script":"deploy.bat","preDownload":{"enabled":true,"project":"ProjectA","artifact":"app.zip"}}`))

	if rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("expected 504, got %d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		TimedOut bool   `json:"timedOut"`
		Error    string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.TimedOut {
		t.Fatal("timedOut = false, want true")
	}
	if body.Error != "pre-run download timed out" {
		t.Fatalf("error = %q, want pre-run download timed out", body.Error)
	}
}

func TestRunMissingScriptReturns404(t *testing.T) {
	server := makeServer(t, nil)
	rec := httptest.NewRecorder()

	server.handler.ServeHTTP(rec, postRun("missing.bat"))

	assertErrorResponse(t, rec, http.StatusNotFound, "script not found")
}

func TestRunInvalidScriptReturns400(t *testing.T) {
	server := makeServer(t, nil)
	rec := httptest.NewRecorder()

	server.handler.ServeHTTP(rec, postRun(`..\evil.bat`))

	assertErrorResponse(t, rec, http.StatusBadRequest, "invalid script name")
}

func TestRunRejectsInvalidPreDownloadRequest(t *testing.T) {
	server := makeServer(t, map[string]string{"deploy.bat": "@echo off\r\necho deploy\r\n"})
	rec := httptest.NewRecorder()

	server.handler.ServeHTTP(rec, postRunBody(`{"script":"deploy.bat","preDownload":{"enabled":true,"project":"..","artifact":"app.zip"}}`))

	assertErrorResponse(t, rec, http.StatusBadRequest, "invalid pre-run download request")
}

func TestRunBusyReturns409(t *testing.T) {
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

	server.handler.ServeHTTP(rec, postRun("busy.bat"))

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

func TestRunStreamPassesArgsToSelectedScript(t *testing.T) {
	server := makeServer(t, map[string]string{"deploy.bat": "@echo off\r\necho project=%~1\r\necho artifact=%~2\r\n"})
	req := httptest.NewRequest(http.MethodPost, "/run/stream", bytes.NewBufferString(`{"script":"deploy.bat","args":["ProjectA","app.zip"]}`))
	req.Header.Set("Authorization", authHeader())
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "project=ProjectA") {
		t.Fatalf("body = %s, want project argument output", body)
	}
	if !strings.Contains(body, "artifact=app.zip") {
		t.Fatalf("body = %s, want artifact argument output", body)
	}
}

func TestRunStreamNoOutputStillReturnsFinal(t *testing.T) {
	server := makeServer(t, map[string]string{"quiet.bat": "@echo off\r\nexit /b 0\r\n"})
	rec := httptest.NewRecorder()
	done := make(chan struct{})

	go func() {
		server.handler.ServeHTTP(rec, postRunStream("quiet.bat"))
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stream did not finish for script without output")
	}

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	messages := decodeNDJSON(t, rec.Body.String())
	if len(messages) != 1 {
		t.Fatalf("expected only final message, got %d body=%s", len(messages), rec.Body.String())
	}
	final := messages[0]
	if rawString(t, final, "type") != "final" {
		t.Fatalf("final type = %q, want final", rawString(t, final, "type"))
	}
	if rawInt(t, final, "exitCode") != 0 {
		t.Fatalf("exitCode = %d, want 0", rawInt(t, final, "exitCode"))
	}
}

func TestWriteStreamOutputsReturnsForNilChannel(t *testing.T) {
	rec := httptest.NewRecorder()
	done := make(chan bool, 1)

	go func() {
		done <- writeStreamOutputs(rec, json.NewEncoder(rec), nil)
	}()

	select {
	case ok := <-done:
		if !ok {
			t.Fatal("writeStreamOutputs returned false, want true")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("writeStreamOutputs blocked on nil channel")
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

func TestRunStreamTimeoutReturnsFinalOverHTTP200(t *testing.T) {
	server := makeServerWithTimeout(t, map[string]string{"slow.bat": "@echo off\r\nping -n 3 127.0.0.1 >nul\r\n"}, 20*time.Millisecond)
	rec := httptest.NewRecorder()

	server.handler.ServeHTTP(rec, postRunStream("slow.bat"))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	messages := decodeNDJSON(t, rec.Body.String())
	if len(messages) == 0 {
		t.Fatalf("expected final message, got body=%s", rec.Body.String())
	}
	final := messages[len(messages)-1]
	if rawString(t, final, "type") != "final" {
		t.Fatalf("final type = %q, want final", rawString(t, final, "type"))
	}
	if rawInt(t, final, "exitCode") != -1 {
		t.Fatalf("exitCode = %d, want -1", rawInt(t, final, "exitCode"))
	}
	if !rawBool(t, final, "timedOut") {
		t.Fatal("timedOut = false, want true")
	}
	if rawString(t, final, "error") != "script timed out" {
		t.Fatalf("error = %q, want script timed out", rawString(t, final, "error"))
	}
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

func TestStatusWriterFlushPassesThrough(t *testing.T) {
	fw := &flushRecorder{}
	flush(&statusWriter{ResponseWriter: fw})

	if !fw.flushed {
		t.Fatal("flush did not call underlying flusher")
	}
}

type fakeReleaseCatalog struct {
	manifest     *releasecatalog.Manifest
	manifestErr  error
	refreshErr   error
	refreshCalls int
}

func (f *fakeReleaseCatalog) Manifest() (*releasecatalog.Manifest, error) {
	return f.manifest, f.manifestErr
}

func (f *fakeReleaseCatalog) Refresh(context.Context) error {
	f.refreshCalls++
	return f.refreshErr
}

type fakeDownloadService struct {
	startVersion  string
	startResource string
	startInfo     downloadtask.Info
	startErr      error
	getInfo       downloadtask.Info
	getErr        error
	cancelID      string
	cancelErr     error
}

func (f *fakeDownloadService) Start(version, resourceID string) (downloadtask.Info, error) {
	f.startVersion = version
	f.startResource = resourceID
	return f.startInfo, f.startErr
}

func (f *fakeDownloadService) Get(string) (downloadtask.Info, error) {
	return f.getInfo, f.getErr
}

func (f *fakeDownloadService) Cancel(id string) error {
	f.cancelID = id
	return f.cancelErr
}

func makeFeatureHandler(t *testing.T, releases ReleaseCatalog, downloads DownloadService) http.Handler {
	t.Helper()
	reg, err := registry.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	api := New(executor.New(reg, time.Second), releases, downloads)
	return api.Routes(func(h http.Handler) http.Handler {
		return auth.BasicAuth("admin", "change-me-please", h)
	})
}

func TestReleaseAndDownloadEndpointsRequireAuth(t *testing.T) {
	handler := makeFeatureHandler(t, &fakeReleaseCatalog{}, &fakeDownloadService{})
	for _, tc := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/releases"},
		{http.MethodPost, "/releases/refresh"},
		{http.MethodPost, "/downloads"},
		{http.MethodGet, "/downloads/task-id"},
		{http.MethodPost, "/downloads/task-id/cancel"},
	} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s returned %d, want 401", tc.method, tc.path, rec.Code)
		}
	}
}

func TestReleasesHideSignedURLsAndRefreshRemotely(t *testing.T) {
	catalog := &fakeReleaseCatalog{manifest: &releasecatalog.Manifest{
		SchemaVersion: 1,
		Product:       "deploy-agent",
		LatestVersion: "2026.08.19",
		GeneratedAt:   time.Date(2026, 8, 19, 10, 0, 0, 0, time.UTC),
		Releases: []releasecatalog.Release{{
			Version:     "2026.08.19",
			PublishedAt: time.Date(2026, 8, 19, 9, 30, 0, 0, time.UTC),
			Resources: []releasecatalog.Resource{{
				ID: "api", Kind: "component", Name: "api.zip", URL: "https://signed.example/secret", Size: 12, SHA256: strings.Repeat("a", 64),
			}},
		}},
	}}
	handler := makeFeatureHandler(t, catalog, &fakeDownloadService{})

	for _, tc := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/releases"},
		{http.MethodPost, "/releases/refresh"},
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		req.Header.Set("Authorization", authHeader())
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s %s returned %d: %s", tc.method, tc.path, rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "signed.example") || strings.Contains(rec.Body.String(), `"url"`) {
			t.Fatalf("signed URL leaked in response: %s", rec.Body.String())
		}
	}
	if catalog.refreshCalls != 1 {
		t.Fatalf("refresh calls = %d, want 1", catalog.refreshCalls)
	}
}

func TestReleaseRefreshFailureKeepsCachedManifestAvailable(t *testing.T) {
	catalog := &fakeReleaseCatalog{
		manifest:   &releasecatalog.Manifest{LatestVersion: "1.0.0"},
		refreshErr: errors.New("upstream unavailable"),
	}
	handler := makeFeatureHandler(t, catalog, nil)
	refreshReq := httptest.NewRequest(http.MethodPost, "/releases/refresh", nil)
	refreshReq.Header.Set("Authorization", authHeader())
	refreshRec := httptest.NewRecorder()
	handler.ServeHTTP(refreshRec, refreshReq)
	if refreshRec.Code != http.StatusBadGateway {
		t.Fatalf("refresh returned %d: %s", refreshRec.Code, refreshRec.Body.String())
	}
	getReq := httptest.NewRequest(http.MethodGet, "/releases", nil)
	getReq.Header.Set("Authorization", authHeader())
	getRec := httptest.NewRecorder()
	handler.ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusOK || !strings.Contains(getRec.Body.String(), `"latestVersion": "1.0.0"`) {
		t.Fatalf("cached release response = %d: %s", getRec.Code, getRec.Body.String())
	}
}

func TestDownloadEndpointLifecycleAndShape(t *testing.T) {
	info := downloadtask.Info{
		TaskID: "task-1", State: downloadtask.StateDownloading, Version: "2026.08.19", ResourceID: "api",
		Phase: "download", BytesDone: 25, TotalBytes: 100, Percent: 25, SpeedBytesPerSecond: 500,
		Destination: `D:\tools\download\2026.08.19\api.zip`,
	}
	downloads := &fakeDownloadService{startInfo: info, getInfo: info}
	handler := makeFeatureHandler(t, &fakeReleaseCatalog{}, downloads)

	startReq := httptest.NewRequest(http.MethodPost, "/downloads", bytes.NewBufferString(`{"version":"2026.08.19","resourceId":"api"}`))
	startReq.Header.Set("Authorization", authHeader())
	startRec := httptest.NewRecorder()
	handler.ServeHTTP(startRec, startReq)
	if startRec.Code != http.StatusAccepted {
		t.Fatalf("start returned %d: %s", startRec.Code, startRec.Body.String())
	}
	if downloads.startVersion != "2026.08.19" || downloads.startResource != "api" {
		t.Fatalf("start arguments = %q/%q", downloads.startVersion, downloads.startResource)
	}
	var got downloadtask.Info
	if err := json.Unmarshal(startRec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.TaskID != "task-1" || got.State != downloadtask.StateDownloading || got.BytesDone != 25 || got.Destination == "" {
		t.Fatalf("unexpected start response: %#v", got)
	}

	statusReq := httptest.NewRequest(http.MethodGet, "/downloads/task-1", nil)
	statusReq.Header.Set("Authorization", authHeader())
	statusRec := httptest.NewRecorder()
	handler.ServeHTTP(statusRec, statusReq)
	if statusRec.Code != http.StatusOK {
		t.Fatalf("status returned %d: %s", statusRec.Code, statusRec.Body.String())
	}

	cancelReq := httptest.NewRequest(http.MethodPost, "/downloads/task-1/cancel", nil)
	cancelReq.Header.Set("Authorization", authHeader())
	cancelRec := httptest.NewRecorder()
	handler.ServeHTTP(cancelRec, cancelReq)
	if cancelRec.Code != http.StatusAccepted || downloads.cancelID != "task-1" {
		t.Fatalf("cancel returned %d id=%q body=%s", cancelRec.Code, downloads.cancelID, cancelRec.Body.String())
	}
}

func TestDownloadConflictAndUnavailableStatusCodes(t *testing.T) {
	downloads := &fakeDownloadService{startErr: downloadtask.ErrConflict, getErr: downloadtask.ErrNotFound}
	handler := makeFeatureHandler(t, nil, downloads)

	startReq := httptest.NewRequest(http.MethodPost, "/downloads", bytes.NewBufferString(`{"version":"1.0.0","resourceId":"api"}`))
	startReq.Header.Set("Authorization", authHeader())
	startRec := httptest.NewRecorder()
	handler.ServeHTTP(startRec, startReq)
	if startRec.Code != http.StatusConflict {
		t.Fatalf("conflict returned %d: %s", startRec.Code, startRec.Body.String())
	}

	getReq := httptest.NewRequest(http.MethodGet, "/downloads/missing", nil)
	getReq.Header.Set("Authorization", authHeader())
	getRec := httptest.NewRecorder()
	handler.ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusNotFound {
		t.Fatalf("missing task returned %d: %s", getRec.Code, getRec.Body.String())
	}

	releasesReq := httptest.NewRequest(http.MethodGet, "/releases", nil)
	releasesReq.Header.Set("Authorization", authHeader())
	releasesRec := httptest.NewRecorder()
	handler.ServeHTTP(releasesRec, releasesReq)
	if releasesRec.Code != http.StatusServiceUnavailable {
		t.Fatalf("disabled releases returned %d: %s", releasesRec.Code, releasesRec.Body.String())
	}
}

func assertErrorResponse(t *testing.T, rec *httptest.ResponseRecorder, wantCode int, wantError string) {
	t.Helper()

	if rec.Code != wantCode {
		t.Fatalf("expected %d, got %d body=%s", wantCode, rec.Code, rec.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["error"] != wantError {
		t.Fatalf("error = %q, want %q", body["error"], wantError)
	}
}
