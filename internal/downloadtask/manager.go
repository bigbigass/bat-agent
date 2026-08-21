// Package downloadtask downloads release resources to the service machine.
package downloadtask

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/liqixin/deploy-agent/internal/releasecatalog"
)

const (
	StateQueued      = "queued"
	StateDownloading = "downloading"
	StateVerifying   = "verifying"
	StateCompleted   = "completed"
	StateFailed      = "failed"
	StateCancelled   = "cancelled"
)

var (
	ErrConflict       = errors.New("another download is active")
	ErrNotFound       = errors.New("download task not found")
	ErrInvalidRequest = errors.New("invalid download request")
	// Compatibility aliases for callers that prefer the HTTP terminology.
	ErrDownloadConflict = ErrConflict
	ErrDownloadNotFound = ErrNotFound
)

type Catalog interface {
	Resolve(version, resourceID string) (releasecatalog.Resource, bool, error)
	Refresh(context.Context) error
}

type Info struct {
	TaskID              string  `json:"taskId"`
	State               string  `json:"state"`
	Version             string  `json:"version"`
	ResourceID          string  `json:"resourceId"`
	Name                string  `json:"name"`
	Kind                string  `json:"kind"`
	Phase               string  `json:"phase"`
	BytesDone           int64   `json:"bytesDone"`
	TotalBytes          int64   `json:"totalBytes"`
	Percent             float64 `json:"percent"`
	SpeedBytesPerSecond float64 `json:"speedBytesPerSecond"`
	Destination         string  `json:"destination"`
	Error               string  `json:"error"`
}

type task struct {
	info   Info
	ctx    context.Context
	cancel context.CancelFunc
}

type Manager struct {
	ctx     context.Context
	root    string
	catalog Catalog
	client  *http.Client

	mu     sync.RWMutex
	tasks  map[string]*task
	active string
}

func New(parent context.Context, root string, catalog Catalog, client *http.Client) (*Manager, error) {
	if parent == nil {
		parent = context.Background()
	}
	if catalog == nil {
		return nil, errors.New("release catalog is required")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve download directory: %w", err)
	}
	if err := os.MkdirAll(absRoot, 0o755); err != nil {
		return nil, fmt.Errorf("create download directory: %w", err)
	}
	if client == nil {
		client = http.DefaultClient
	}
	return &Manager{
		ctx:     parent,
		root:    absRoot,
		catalog: catalog,
		client:  client,
		tasks:   make(map[string]*task),
	}, nil
}

func (m *Manager) Start(version, resourceID string) (Info, error) {
	if strings.TrimSpace(version) == "" || strings.TrimSpace(resourceID) == "" {
		return Info{}, fmt.Errorf("%w: version and resourceId are required", ErrInvalidRequest)
	}
	m.mu.RLock()
	active := m.active
	m.mu.RUnlock()
	if active != "" {
		return Info{}, ErrConflict
	}
	resource, latest, err := m.catalog.Resolve(version, resourceID)
	if err != nil {
		return Info{}, err
	}
	target, err := m.targetPath(version, resource.Name)
	if err != nil {
		return Info{}, err
	}
	id, err := randomID()
	if err != nil {
		return Info{}, fmt.Errorf("create task ID: %w", err)
	}

	m.mu.Lock()
	if m.active != "" {
		m.mu.Unlock()
		return Info{}, ErrConflict
	}
	ctx, cancel := context.WithCancel(m.ctx)
	entry := &task{
		info: Info{
			TaskID:      id,
			State:       StateQueued,
			Version:     version,
			ResourceID:  resourceID,
			Name:        resource.Name,
			Kind:        resource.Kind,
			Phase:       "queued",
			TotalBytes:  resource.Size,
			Destination: target,
		},
		ctx:    ctx,
		cancel: cancel,
	}
	m.tasks[id] = entry
	m.active = id
	info := entry.info
	m.mu.Unlock()

	go m.run(ctx, id, resource, latest)
	return info, nil
}

func (m *Manager) Get(id string) (Info, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	entry := m.tasks[id]
	if entry == nil {
		return Info{}, ErrNotFound
	}
	return entry.info, nil
}

func (m *Manager) Cancel(id string) error {
	m.mu.RLock()
	entry := m.tasks[id]
	if entry == nil {
		m.mu.RUnlock()
		return ErrNotFound
	}
	cancel := entry.cancel
	terminal := isTerminal(entry.info.State)
	m.mu.RUnlock()
	if !terminal {
		cancel()
	}
	return nil
}

func (m *Manager) run(ctx context.Context, id string, resource releasecatalog.Resource, latest bool) {
	m.setState(id, StateDownloading, "download")
	resource, latest, target, err := m.downloadWithRefresh(ctx, id, resource, latest)
	if err != nil {
		m.finishError(id, err)
		return
	}
	if latest {
		if err := m.updateLatest(ctx, id, target, resource.Name); err != nil {
			m.finishError(id, fmt.Errorf("update latest copy: %w", err))
			return
		}
	}
	if err := ctx.Err(); err != nil {
		m.finishError(id, err)
		return
	}
	m.mu.Lock()
	entry := m.tasks[id]
	if entry != nil {
		entry.info.State = StateCompleted
		entry.info.Phase = "complete"
		entry.info.BytesDone = entry.info.TotalBytes
		entry.info.Percent = 100
		entry.info.Error = ""
	}
	if m.active == id {
		m.active = ""
	}
	m.mu.Unlock()
}

func (m *Manager) downloadWithRefresh(ctx context.Context, id string, resource releasecatalog.Resource, latest bool) (releasecatalog.Resource, bool, string, error) {
	target, err := m.targetPath(m.taskVersion(id), resource.Name)
	if err != nil {
		return resource, latest, "", err
	}
	m.setResource(id, resource, target)
	err = m.downloadOnce(ctx, id, resource, target)
	var statusErr httpStatusError
	if !errors.As(err, &statusErr) || !isRefreshableResourceStatus(statusErr.code) {
		return resource, latest, target, err
	}

	if refreshErr := m.catalog.Refresh(ctx); refreshErr != nil {
		return resource, latest, target, fmt.Errorf("signed resource URL expired and manifest refresh failed: %w", refreshErr)
	}
	version, resourceID := m.taskIdentity(id)
	resource, latest, err = m.catalog.Resolve(version, resourceID)
	if err != nil {
		return resource, latest, target, fmt.Errorf("resolve resource after manifest refresh: %w", err)
	}
	target, err = m.targetPath(version, resource.Name)
	if err != nil {
		return resource, latest, "", err
	}
	m.setResource(id, resource, target)
	return resource, latest, target, m.downloadOnce(ctx, id, resource, target)
}

func isRefreshableResourceStatus(code int) bool {
	return code == http.StatusUnauthorized || code == http.StatusForbidden || code == http.StatusNotFound || code == http.StatusGone
}

func (m *Manager) downloadOnce(ctx context.Context, id string, resource releasecatalog.Resource, target string) (retErr error) {
	part := target + ".part"
	defer func() {
		if retErr != nil {
			_ = os.Remove(part)
		}
	}()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, resource.URL, nil)
	if err != nil {
		return errors.New("create resource request failed")
	}
	resp, err := m.client.Do(req)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		return errors.New("download resource request failed")
	}
	defer resp.Body.Close()
	if resp.Request != nil && !strings.EqualFold(resp.Request.URL.Scheme, "https") {
		return errors.New("resource request redirected away from HTTPS")
	}
	if resp.StatusCode != http.StatusOK {
		return httpStatusError{code: resp.StatusCode}
	}
	if resp.ContentLength >= 0 && resp.ContentLength != resource.Size {
		return fmt.Errorf("resource size mismatch: manifest=%d HTTP=%d", resource.Size, resp.ContentLength)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("create version directory: %w", err)
	}
	file, err := os.OpenFile(part, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("create temporary file: %w", err)
	}
	defer file.Close()

	started := time.Now()
	writer := &progressWriter{
		dst: file,
		onWrite: func(bytesDone int64) {
			m.setProgress(id, bytesDone, started)
		},
	}
	limit := resource.Size
	if limit < int64(^uint64(0)>>1) {
		limit++
	}
	written, err := io.Copy(writer, io.LimitReader(resp.Body, limit))
	if err != nil {
		return fmt.Errorf("write temporary file: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync temporary file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close temporary file: %w", err)
	}

	m.setState(id, StateVerifying, "verify")
	if written != resource.Size {
		return fmt.Errorf("resource size mismatch: expected %d bytes, got %d", resource.Size, written)
	}
	verifyFile, err := os.Open(part)
	if err != nil {
		return fmt.Errorf("open temporary file for verification: %w", err)
	}
	verifyHash := sha256.New()
	verifiedBytes, verifyErr := copyWithContext(ctx, verifyHash, verifyFile)
	closeErr := verifyFile.Close()
	if verifyErr != nil {
		return fmt.Errorf("read temporary file for verification: %w", verifyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close temporary file after verification: %w", closeErr)
	}
	actualHash := hex.EncodeToString(verifyHash.Sum(nil))
	if verifiedBytes != resource.Size {
		return fmt.Errorf("verified size mismatch: expected %d bytes, got %d", resource.Size, verifiedBytes)
	}
	if !strings.EqualFold(actualHash, resource.SHA256) {
		return fmt.Errorf("SHA-256 mismatch: expected %s, got %s", resource.SHA256, actualHash)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := replaceFile(part, target); err != nil {
		return fmt.Errorf("publish downloaded file: %w", err)
	}
	return nil
}

func (m *Manager) updateLatest(ctx context.Context, id, source, name string) (retErr error) {
	dir := filepath.Join(m.root, "latest")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	target := filepath.Join(dir, name)
	temp := target + "." + id + ".part"
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(temp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer func() {
		_ = out.Close()
		if retErr != nil {
			_ = os.Remove(temp)
		}
	}()

	if _, err := copyWithContext(ctx, out, in); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := out.Sync(); err != nil {
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return replaceFile(temp, target)
}

func (m *Manager) targetPath(version, name string) (string, error) {
	if !safeSegment(version) || !safeSegment(name) {
		return "", fmt.Errorf("%w: release contains an unsafe destination path", ErrInvalidRequest)
	}
	target := filepath.Join(m.root, version, name)
	rel, err := filepath.Rel(m.root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: release destination escapes download directory", ErrInvalidRequest)
	}
	return target, nil
}

func (m *Manager) taskVersion(id string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if entry := m.tasks[id]; entry != nil {
		return entry.info.Version
	}
	return ""
}

func (m *Manager) taskIdentity(id string) (string, string) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if entry := m.tasks[id]; entry != nil {
		return entry.info.Version, entry.info.ResourceID
	}
	return "", ""
}

func (m *Manager) setResource(id string, resource releasecatalog.Resource, target string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if entry := m.tasks[id]; entry != nil {
		entry.info.Name = resource.Name
		entry.info.Kind = resource.Kind
		entry.info.TotalBytes = resource.Size
		entry.info.Destination = target
		entry.info.BytesDone = 0
		entry.info.Percent = 0
		entry.info.SpeedBytesPerSecond = 0
	}
}

func (m *Manager) setState(id, state, phase string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if entry := m.tasks[id]; entry != nil {
		entry.info.State = state
		entry.info.Phase = phase
	}
}

func (m *Manager) setProgress(id string, bytesDone int64, started time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry := m.tasks[id]
	if entry == nil {
		return
	}
	entry.info.BytesDone = bytesDone
	if entry.info.TotalBytes > 0 {
		entry.info.Percent = float64(bytesDone) * 100 / float64(entry.info.TotalBytes)
		if entry.info.Percent > 100 {
			entry.info.Percent = 100
		}
		entry.info.Percent = math.Round(entry.info.Percent*100) / 100
	}
	if elapsed := time.Since(started).Seconds(); elapsed > 0 {
		entry.info.SpeedBytesPerSecond = float64(bytesDone) / elapsed
	}
}

func (m *Manager) finishError(id string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry := m.tasks[id]
	if entry != nil {
		cancelled := entry.ctx != nil && entry.ctx.Err() != nil &&
			(errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded))
		if cancelled {
			entry.info.State = StateCancelled
			entry.info.Phase = "cancelled"
			entry.info.Error = ""
		} else {
			entry.info.State = StateFailed
			entry.info.Phase = "failed"
			entry.info.Error = err.Error()
		}
	}
	if m.active == id {
		m.active = ""
	}
}

func isTerminal(state string) bool {
	return state == StateCompleted || state == StateFailed || state == StateCancelled
}

func safeSegment(value string) bool {
	return value != "" && strings.TrimSpace(value) == value && value != "." && value != ".." &&
		!strings.HasSuffix(value, ".") && !strings.ContainsAny(value, `/\\:<>"|?*`) && !strings.ContainsRune(value, 0)
}

func randomID() (string, error) {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(data[:]), nil
}

func copyWithContext(ctx context.Context, dst io.Writer, src io.Reader) (int64, error) {
	buffer := make([]byte, 128*1024)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		n, readErr := src.Read(buffer)
		if n > 0 {
			written, writeErr := dst.Write(buffer[:n])
			total += int64(written)
			if writeErr != nil {
				return total, writeErr
			}
			if written != n {
				return total, io.ErrShortWrite
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return total, nil
			}
			return total, readErr
		}
	}
}

type progressWriter struct {
	dst     io.Writer
	written int64
	onWrite func(int64)
}

func (w *progressWriter) Write(data []byte) (int, error) {
	n, err := w.dst.Write(data)
	w.written += int64(n)
	if w.onWrite != nil {
		w.onWrite(w.written)
	}
	return n, err
}

type httpStatusError struct {
	code int
}

func (e httpStatusError) Error() string {
	return fmt.Sprintf("download resource: HTTP %d", e.code)
}
