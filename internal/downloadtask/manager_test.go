package downloadtask

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liqixin/deploy-agent/internal/releasecatalog"
)

type fakeCatalog struct {
	mu               sync.Mutex
	resource         releasecatalog.Resource
	refreshed        *releasecatalog.Resource
	latest           bool
	refreshErr       error
	refreshCallCount int
}

func (f *fakeCatalog) Resolve(_, _ string) (releasecatalog.Resource, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.resource, f.latest, nil
}

func (f *fakeCatalog) Refresh(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refreshCallCount++
	if f.refreshErr != nil {
		return f.refreshErr
	}
	if f.refreshed != nil {
		f.resource = *f.refreshed
	}
	return nil
}

func TestDownloadSuccessProgressAndConflict(t *testing.T) {
	data := []byte(strings.Repeat("release-data-", 32*1024))
	server := slowTLSServer(data, 16*1024, 2*time.Millisecond)
	defer server.Close()
	catalog := &fakeCatalog{resource: testResource(server.URL, data), latest: true}
	manager, err := New(context.Background(), t.TempDir(), catalog, server.Client())
	if err != nil {
		t.Fatal(err)
	}

	started, err := manager.Start("2026.08.19", "api")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Start("2026.08.19", "api"); !errors.Is(err, ErrConflict) {
		t.Fatalf("second active download error = %v, want conflict", err)
	}

	sawProgress := false
	completed := waitForTask(t, manager, started.TaskID, func(info Info) bool {
		if info.State == StateDownloading && info.BytesDone > 0 && info.BytesDone < info.TotalBytes {
			sawProgress = info.Percent > 0 && info.SpeedBytesPerSecond > 0
		}
		return info.State == StateCompleted
	})
	if !sawProgress {
		t.Fatal("did not observe real download progress")
	}
	if completed.Percent != 100 || completed.BytesDone != int64(len(data)) {
		t.Fatalf("unexpected completed progress: %#v", completed)
	}
	assertFileContents(t, completed.Destination, data)
	assertFileContents(t, filepath.Join(filepath.Dir(filepath.Dir(completed.Destination)), "latest", completed.Name), data)
	if _, err := manager.Get("missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get missing error = %v", err)
	}
}

func TestDownloadChecksumFailureRemovesTemporaryFile(t *testing.T) {
	data := []byte("corrupt me")
	server := slowTLSServer(data, len(data), 0)
	defer server.Close()
	resource := testResource(server.URL, data)
	resource.SHA256 = strings.Repeat("0", 64)
	manager, err := New(context.Background(), t.TempDir(), &fakeCatalog{resource: resource}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	started, err := manager.Start("1.0.0", "api")
	if err != nil {
		t.Fatal(err)
	}
	failed := waitForTask(t, manager, started.TaskID, func(info Info) bool { return info.State == StateFailed })
	if !strings.Contains(failed.Error, "SHA-256 mismatch") {
		t.Fatalf("unexpected error: %q", failed.Error)
	}
	assertNotExists(t, failed.Destination)
	assertNotExists(t, failed.Destination+".part")
}

func TestCancelRemovesTemporaryFile(t *testing.T) {
	data := []byte(strings.Repeat("x", 2*1024*1024))
	server := slowTLSServer(data, 8*1024, 5*time.Millisecond)
	defer server.Close()
	manager, err := New(context.Background(), t.TempDir(), &fakeCatalog{resource: testResource(server.URL, data)}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	started, err := manager.Start("1.0.0", "api")
	if err != nil {
		t.Fatal(err)
	}
	waitForTask(t, manager, started.TaskID, func(info Info) bool { return info.BytesDone > 0 })
	if err := manager.Cancel(started.TaskID); err != nil {
		t.Fatal(err)
	}
	cancelled := waitForTask(t, manager, started.TaskID, func(info Info) bool { return info.State == StateCancelled })
	assertNotExists(t, cancelled.Destination)
	assertNotExists(t, cancelled.Destination+".part")
}

func TestExpiredURLRefreshesManifestOnce(t *testing.T) {
	data := []byte("fresh signed artifact")
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/expired" {
			http.Error(w, "expired", http.StatusForbidden)
			return
		}
		_, _ = w.Write(data)
	}))
	defer server.Close()
	oldResource := testResource(server.URL+"/expired", data)
	newResource := testResource(server.URL+"/fresh", data)
	catalog := &fakeCatalog{resource: oldResource, refreshed: &newResource}
	manager, err := New(context.Background(), t.TempDir(), catalog, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	started, err := manager.Start("1.0.0", "api")
	if err != nil {
		t.Fatal(err)
	}
	waitForTask(t, manager, started.TaskID, func(info Info) bool { return info.State == StateCompleted })
	catalog.mu.Lock()
	refreshes := catalog.refreshCallCount
	catalog.mu.Unlock()
	if refreshes != 1 {
		t.Fatalf("manifest refresh calls = %d, want 1", refreshes)
	}
}

func TestDownloadReplacesExistingVersionAndLatestFiles(t *testing.T) {
	data := []byte("replaceable artifact")
	server := slowTLSServer(data, len(data), 0)
	defer server.Close()
	manager, err := New(context.Background(), t.TempDir(), &fakeCatalog{resource: testResource(server.URL, data), latest: true}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	first, err := manager.Start("1.0.0", "api")
	if err != nil {
		t.Fatal(err)
	}
	waitForTask(t, manager, first.TaskID, func(info Info) bool { return info.State == StateCompleted })
	second, err := manager.Start("1.0.0", "api")
	if err != nil {
		t.Fatal(err)
	}
	if second.TaskID == first.TaskID {
		t.Fatal("retry reused the previous task ID")
	}
	completed := waitForTask(t, manager, second.TaskID, func(info Info) bool { return info.State == StateCompleted })
	assertFileContents(t, completed.Destination, data)
	assertFileContents(t, filepath.Join(filepath.Dir(filepath.Dir(completed.Destination)), "latest", completed.Name), data)
}

func slowTLSServer(data []byte, chunkSize int, delay time.Duration) *httptest.Server {
	return httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for offset := 0; offset < len(data); offset += chunkSize {
			end := offset + chunkSize
			if end > len(data) {
				end = len(data)
			}
			if _, err := w.Write(data[offset:end]); err != nil {
				return
			}
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			if delay > 0 {
				select {
				case <-r.Context().Done():
					return
				case <-time.After(delay):
				}
			}
		}
	}))
}

func testResource(url string, data []byte) releasecatalog.Resource {
	sum := sha256.Sum256(data)
	return releasecatalog.Resource{
		ID:     "api",
		Kind:   "component",
		Name:   "deploy-agent-api.zip",
		URL:    url,
		Size:   int64(len(data)),
		SHA256: hex.EncodeToString(sum[:]),
	}
}

func waitForTask(t *testing.T, manager *Manager, id string, done func(Info) bool) Info {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		info, err := manager.Get(id)
		if err != nil {
			t.Fatal(err)
		}
		if done(info) {
			return info
		}
		if info.State == StateFailed {
			t.Fatalf("download failed while waiting: %s", info.Error)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for task %s", id)
	return Info{}
}

func assertFileContents(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("file %s contents do not match", path)
	}
}

func assertNotExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected %s not to exist, stat error = %v", path, err)
	}
}
