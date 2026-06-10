package httpapi

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/liqixin/deploy-agent/internal/auth"
	"github.com/liqixin/deploy-agent/internal/executor"
	"github.com/liqixin/deploy-agent/internal/registry"
)

type testServer struct {
	handler http.Handler
	reg     *registry.Registry
}

func makeServer(t *testing.T, files map[string]string) testServer {
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
	exec := executor.New(reg, 5*time.Second)
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
