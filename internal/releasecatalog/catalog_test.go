package releasecatalog

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const valid = `{"schemaVersion":1,"product":"deploy-agent","latestVersion":"1.2.0","generatedAt":"2026-01-01T00:00:00Z","releases":[{"version":"1.2.0","publishedAt":"2026-01-01T00:00:00Z","resources":[{"id":"win-x64","kind":"bundle","name":"deploy-agent.exe","url":"https://example.com/deploy-agent.exe","size":12,"sha256":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}]}]}`

func TestParseValid(t *testing.T) {
	m, err := Parse([]byte(valid))
	if err != nil {
		t.Fatal(err)
	}
	if m.LatestVersion != "1.2.0" || len(m.Releases[0].Resources) != 1 {
		t.Fatalf("unexpected manifest: %#v", m)
	}
}

func TestParseRejects(t *testing.T) {
	tests := []struct{ name, old, new string }{
		{"missing latest", `"latestVersion":"1.2.0"`, `"latestVersion":""`},
		{"latest absent", `"latestVersion":"1.2.0"`, `"latestVersion":"9.0.0"`},
		{"path name", `"name":"deploy-agent.exe"`, `"name":"dir/deploy-agent.exe"`},
		{"non https", `"url":"https://example.com/deploy-agent.exe"`, `"url":"http://example.com/deploy-agent.exe"`},
		{"bad kind", `"kind":"bundle"`, `"kind":"installer"`},
		{"zero size", `"size":12`, `"size":0`},
		{"negative size", `"size":12`, `"size":-1`},
		{"bad hash", `"sha256":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"`, `"sha256":"xyz"`},
		{"unknown field", `"product":"deploy-agent"`, `"product":"deploy-agent","extra":true`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := []byte(strings.Replace(valid, tt.old, tt.new, 1))
			if _, err := Parse(data); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestParseResourceIDsAreUniqueWithinRelease(t *testing.T) {
	duplicate := strings.Replace(valid, `}]}`, `},{"id":"win-x64","kind":"component","name":"api.zip","url":"https://example.com/api.zip","size":1,"sha256":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}]}`, 1)
	if _, err := Parse([]byte(duplicate)); err == nil {
		t.Fatal("expected duplicate resource ID error")
	}

	secondRelease := strings.Replace(valid, `]}`, `]},{"version":"1.1.0","publishedAt":"2025-12-01T00:00:00Z","resources":[{"id":"win-x64","kind":"bundle","name":"old.zip","url":"https://example.com/old.zip","size":1,"sha256":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}]}`, 1)
	if _, err := Parse([]byte(secondRelease)); err != nil {
		t.Fatalf("same resource ID in another release should be valid: %v", err)
	}
}

func TestRefreshFailureKeepsLastManifest(t *testing.T) {
	body := valid
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()

	catalog, err := New(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	body = `{"schemaVersion":99}`
	if err := catalog.Refresh(context.Background()); err == nil {
		t.Fatal("expected refresh error")
	}
	manifest, err := catalog.Manifest()
	if err != nil {
		t.Fatal(err)
	}
	if manifest.LatestVersion != "1.2.0" {
		t.Fatalf("cached manifest was lost: %#v", manifest)
	}
	if manifest.Releases[0].Resources[0].URL != "" {
		t.Fatal("public manifest exposed signed resource URL")
	}
	resource, latest, err := catalog.Resolve("1.2.0", "win-x64")
	if err != nil || !latest || resource.URL == "" {
		t.Fatalf("internal resource resolution failed: latest=%v resource=%#v err=%v", latest, resource, err)
	}
}
