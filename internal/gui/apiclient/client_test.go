package apiclient

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
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
	wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("admin:password"))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/run/stream" {
			t.Fatalf("path = %q, want /run/stream", r.URL.Path)
		}
		if r.Header.Get("Authorization") != wantAuth {
			t.Fatalf("Authorization = %q, want %q", r.Header.Get("Authorization"), wantAuth)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("Content-Type = %q, want application/json", r.Header.Get("Content-Type"))
		}
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("ReadAll body returned error: %v", err)
		}
		var body struct {
			Script string `json:"script"`
		}
		if err := json.Unmarshal(data, &body); err != nil {
			t.Fatalf("request body is not JSON: %v", err)
		}
		if body.Script != "a.bat" {
			t.Fatalf("script = %q, want a.bat", body.Script)
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(data, &raw); err != nil {
			t.Fatalf("request body is not JSON object: %v", err)
		}
		if _, ok := raw["preDownload"]; ok {
			t.Fatalf("RunStream request included preDownload key: %s", string(data))
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

func TestRunStreamWithOptionsSendsArgs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("ReadAll body returned error: %v", err)
		}
		var body struct {
			Script string   `json:"script"`
			Args   []string `json:"args"`
		}
		if err := json.Unmarshal(data, &body); err != nil {
			t.Fatalf("request body is not JSON: %v", err)
		}
		if body.Script != "deploy.bat" {
			t.Fatalf("script = %q, want deploy.bat", body.Script)
		}
		if strings.Join(body.Args, ",") != "ProjectA,app.zip" {
			t.Fatalf("args = %#v", body.Args)
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(data, &raw); err != nil {
			t.Fatalf("request body is not JSON object: %v", err)
		}
		if _, ok := raw["preDownload"]; ok {
			t.Fatalf("RunStream request included preDownload key: %s", string(data))
		}
		w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
		fmt.Fprintln(w, `{"type":"final","script":"deploy.bat","exitCode":0,"timedOut":false}`)
	}))
	defer server.Close()

	client := New(server.URL, "admin", "password")
	err := client.RunStreamWithOptions(context.Background(), "deploy.bat", RunStreamOptions{
		Args: []string{"ProjectA", "app.zip"},
	}, nil)
	if err != nil {
		t.Fatalf("RunStreamWithOptions returned error: %v", err)
	}
}

func TestRunStreamAllowsNilEventHandler(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
		fmt.Fprintln(w, `{"type":"output","script":"a.bat","stream":"stdout","data":"hello\r\n"}`)
		fmt.Fprintln(w, `{"type":"final","script":"a.bat","exitCode":0,"timedOut":false}`)
	}))
	defer server.Close()

	client := New(server.URL, "admin", "password")
	if err := client.RunStream(context.Background(), "a.bat", nil); err != nil {
		t.Fatalf("RunStream returned error: %v", err)
	}
}

func TestRunStreamErrorsWhenFinalEventMissing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
		fmt.Fprintln(w, `{"type":"output","script":"a.bat","stream":"stdout","data":"hello\r\n"}`)
	}))
	defer server.Close()

	client := New(server.URL, "admin", "password")
	err := client.RunStream(context.Background(), "a.bat", func(event StreamEvent) {})
	if err == nil {
		t.Fatal("RunStream returned nil error, want truncated stream error")
	}
	if !strings.Contains(err.Error(), "final") {
		t.Fatalf("RunStream error = %q, want mention of missing final event", err.Error())
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

func TestRunStreamMapsPlainTextHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer server.Close()

	client := New(server.URL, "admin", "password")
	err := client.RunStream(context.Background(), "a.bat", func(event StreamEvent) {})
	if err == nil {
		t.Fatal("RunStream returned nil error, want HTTPError")
	}
	httpErr, ok := err.(HTTPError)
	if !ok {
		t.Fatalf("error type = %T, want HTTPError", err)
	}
	if httpErr.StatusCode != http.StatusUnauthorized || httpErr.Message != "unauthorized" {
		t.Fatalf("HTTPError = %#v", httpErr)
	}
}

func TestReleaseMethodsUseAuthAndRefreshEndpoint(t *testing.T) {
	wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("admin:password"))
	manifest := `{"schemaVersion":1,"product":"deploy-agent","latestVersion":"2026.08.19","generatedAt":"2026-08-19T10:00:00Z","releases":[{"version":"2026.08.19","publishedAt":"2026-08-19T09:30:00Z","resources":[{"id":"api","kind":"component","name":"api.zip","size":12,"sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]}]}`
	refreshCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != wantAuth {
			t.Fatalf("Authorization = %q, want %q", r.Header.Get("Authorization"), wantAuth)
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/releases":
		case r.Method == http.MethodPost && r.URL.Path == "/releases/refresh":
			refreshCalls++
			data, _ := io.ReadAll(r.Body)
			if len(data) != 0 {
				t.Fatalf("refresh request unexpectedly uploaded a manifest: %q", data)
			}
		default:
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(manifest))
	}))
	defer server.Close()

	client := New(server.URL, "admin", "password")
	got, err := client.Releases(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.LatestVersion != "2026.08.19" || len(got.Releases) != 1 || got.Releases[0].Resources[0].ID != "api" {
		t.Fatalf("unexpected manifest: %#v", got)
	}
	refreshed, err := client.RefreshReleases(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.LatestVersion != got.LatestVersion || refreshCalls != 1 {
		t.Fatalf("unexpected refresh result %#v calls=%d", refreshed, refreshCalls)
	}
}

func TestDownloadMethodsUseContractAndDecodeProgress(t *testing.T) {
	wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("admin:password"))
	infoJSON := `{"taskId":"task-1","state":"downloading","version":"2026.08.19","resourceId":"api","name":"api.zip","kind":"component","phase":"download","bytesDone":25,"totalBytes":100,"percent":25,"speedBytesPerSecond":500,"destination":"D:\\tools\\download\\api.zip","error":""}`
	cancelled := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != wantAuth {
			t.Fatalf("Authorization = %q, want %q", r.Header.Get("Authorization"), wantAuth)
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/downloads":
			var body struct {
				Version    string `json:"version"`
				ResourceID string `json:"resourceId"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.Version != "2026.08.19" || body.ResourceID != "api" {
				t.Fatalf("unexpected start body: %#v", body)
			}
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(infoJSON))
		case r.Method == http.MethodGet && r.URL.Path == "/downloads/task-1":
			_, _ = w.Write([]byte(infoJSON))
		case r.Method == http.MethodPost && r.URL.Path == "/downloads/task-1/cancel":
			cancelled = true
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"status":"cancellation-requested"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := New(server.URL, "admin", "password")
	started, err := client.StartDownload(context.Background(), "2026.08.19", "api")
	if err != nil {
		t.Fatal(err)
	}
	if started.TaskID != "task-1" || started.State != "downloading" || started.Percent != 25 {
		t.Fatalf("unexpected start info: %#v", started)
	}
	status, err := client.DownloadStatus(context.Background(), "task-1")
	if err != nil {
		t.Fatal(err)
	}
	if status.BytesDone != 25 || status.TotalBytes != 100 || status.SpeedBytesPerSecond != 500 || status.Destination == "" {
		t.Fatalf("unexpected status: %#v", status)
	}
	if err := client.CancelDownload(context.Background(), "task-1"); err != nil {
		t.Fatal(err)
	}
	if !cancelled {
		t.Fatal("cancel endpoint was not called")
	}
}
