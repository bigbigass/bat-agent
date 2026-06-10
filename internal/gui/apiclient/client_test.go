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
