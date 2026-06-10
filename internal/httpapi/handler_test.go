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

func quoteJSON(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
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
