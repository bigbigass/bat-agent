package registry

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

var (
	ErrNotFound = errors.New("script not found")
	ErrBusy     = errors.New("script is already running")
	ErrInvalid  = errors.New("invalid script name")
)

type Entry struct {
	Name string // original filename, e.g. "deploy.bat"
	Path string // absolute path
	mu   sync.Mutex
}

// TryLock attempts to acquire the per-script lock without blocking.
func (e *Entry) TryLock() bool { return e.mu.TryLock() }
func (e *Entry) Unlock()       { e.mu.Unlock() }

type Registry struct {
	dir string
	mu  sync.RWMutex
	// Entries keyed by lowercased filename. Entry pointers are stable
	// across rescans so in-flight locks survive reloads.
	entries map[string]*Entry
}

func New(dir string) (*Registry, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("stat scriptDir: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("scriptDir %s is not a directory", abs)
	}
	r := &Registry{dir: abs, entries: map[string]*Entry{}}
	if err := r.Rescan(); err != nil {
		return nil, err
	}
	return r, nil
}

// Rescan refreshes the whitelist from disk. Existing entries keep their
// locks; new files get fresh entries; removed files are dropped.
func (r *Registry) Rescan() error {
	items, err := os.ReadDir(r.dir)
	if err != nil {
		return err
	}
	next := map[string]*Entry{}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, it := range items {
		if it.IsDir() {
			continue
		}
		name := it.Name()
		ext := strings.ToLower(filepath.Ext(name))
		if ext != ".bat" && ext != ".cmd" {
			continue
		}
		key := strings.ToLower(name)
		if existing, ok := r.entries[key]; ok {
			next[key] = existing
			continue
		}
		next[key] = &Entry{
			Name: name,
			Path: filepath.Join(r.dir, name),
		}
	}
	r.entries = next
	return nil
}

// WatchRescan periodically rescans until ctx is done.
func (r *Registry) WatchRescan(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_ = r.Rescan()
		}
	}
}

// List returns sorted script names.
func (r *Registry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.entries))
	for _, e := range r.entries {
		out = append(out, e.Name)
	}
	sort.Strings(out)
	return out
}

// Lookup validates the requested name and returns its entry.
func (r *Registry) Lookup(name string) (*Entry, error) {
	if name == "" {
		return nil, ErrInvalid
	}
	if strings.ContainsAny(name, `/\:`) || strings.Contains(name, "..") {
		return nil, ErrInvalid
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.entries[strings.ToLower(name)]
	if !ok {
		return nil, ErrNotFound
	}
	return e, nil
}

func (r *Registry) Dir() string { return r.dir }
