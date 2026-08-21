// Package releasecatalog parses and validates the release manifest used by
// the update service.
package releasecatalog

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	SchemaVersion    = 1
	maxManifestBytes = 4 << 20
)

var (
	ErrUnavailable      = errors.New("release catalog unavailable")
	ErrReleaseNotFound  = errors.New("release not found")
	ErrResourceNotFound = errors.New("release resource not found")
)

type Manifest struct {
	SchemaVersion int       `json:"schemaVersion"`
	Product       string    `json:"product"`
	LatestVersion string    `json:"latestVersion"`
	GeneratedAt   time.Time `json:"generatedAt"`
	Releases      []Release `json:"releases"`
}

type Release struct {
	Version     string     `json:"version"`
	PublishedAt time.Time  `json:"publishedAt"`
	Resources   []Resource `json:"resources"`
}

type Resource struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	URL    string `json:"url,omitempty"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

func Parse(data []byte) (*Manifest, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var m Manifest
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("decode manifest: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, errors.New("decode manifest: multiple JSON values")
		}
		return nil, fmt.Errorf("decode manifest: %w", err)
	}
	if err := Validate(&m); err != nil {
		return nil, err
	}
	return &m, nil
}

func Validate(m *Manifest) error {
	if m == nil {
		return errors.New("manifest is nil")
	}
	if m.SchemaVersion != SchemaVersion {
		return fmt.Errorf("schemaVersion must be %d", SchemaVersion)
	}
	if strings.TrimSpace(m.Product) == "" || strings.TrimSpace(m.LatestVersion) == "" {
		return errors.New("product and latestVersion are required")
	}
	if m.GeneratedAt.IsZero() {
		return errors.New("generatedAt is required")
	}
	foundLatest := false
	versions := make(map[string]bool, len(m.Releases))
	for i, r := range m.Releases {
		if !validPathSegment(r.Version) {
			return fmt.Errorf("releases[%d].version is invalid", i)
		}
		if versions[r.Version] {
			return fmt.Errorf("releases[%d].version %q is duplicated", i, r.Version)
		}
		versions[r.Version] = true
		if r.PublishedAt.IsZero() {
			return fmt.Errorf("releases[%d].publishedAt is required", i)
		}
		if r.Version == m.LatestVersion {
			foundLatest = true
		}
		ids := make(map[string]bool, len(r.Resources))
		names := make(map[string]bool, len(r.Resources))
		for j, x := range r.Resources {
			if strings.TrimSpace(x.ID) == "" || ids[x.ID] {
				return fmt.Errorf("releases[%d].resources[%d].id must be unique and non-empty", i, j)
			}
			ids[x.ID] = true
			if x.Kind != "bundle" && x.Kind != "component" {
				return fmt.Errorf("resource %q kind must be bundle or component", x.ID)
			}
			if !validPathSegment(x.Name) || filepath.Base(x.Name) != x.Name {
				return fmt.Errorf("resource %q has invalid name", x.ID)
			}
			if names[x.Name] {
				return fmt.Errorf("releases[%d].resources[%d].name must be unique within release", i, j)
			}
			names[x.Name] = true
			u, err := url.Parse(x.URL)
			if err != nil || !strings.EqualFold(u.Scheme, "https") || u.Host == "" {
				return fmt.Errorf("resource %q URL must be HTTPS", x.ID)
			}
			if x.Size <= 0 {
				return fmt.Errorf("resource %q size must be greater than zero", x.ID)
			}
			if len(x.SHA256) != sha256.Size*2 {
				return fmt.Errorf("resource %q sha256 must be 64 hex characters", x.ID)
			}
			if _, err := hex.DecodeString(x.SHA256); err != nil {
				return fmt.Errorf("resource %q sha256 must be hexadecimal", x.ID)
			}
		}
	}
	if !foundLatest {
		return fmt.Errorf("latestVersion %q is not present in releases", m.LatestVersion)
	}
	return nil
}

func validPathSegment(value string) bool {
	return value != "" && strings.TrimSpace(value) == value && value != "." && value != ".." &&
		!strings.HasSuffix(value, ".") && !strings.ContainsAny(value, `/\\:<>"|?*`) && !strings.ContainsRune(value, 0)
}

func cloneManifest(m *Manifest, includeURLs bool) *Manifest {
	if m == nil {
		return nil
	}
	clone := *m
	clone.Releases = make([]Release, len(m.Releases))
	for i := range m.Releases {
		clone.Releases[i] = m.Releases[i]
		clone.Releases[i].Resources = append([]Resource(nil), m.Releases[i].Resources...)
		if !includeURLs {
			for j := range clone.Releases[i].Resources {
				clone.Releases[i].Resources[j].URL = ""
			}
		}
	}
	return &clone
}

type Cache struct {
	mu       sync.RWMutex
	manifest *Manifest
}

func NewCache(m *Manifest) (*Cache, error) {
	if err := Validate(m); err != nil {
		return nil, err
	}
	return &Cache{manifest: cloneManifest(m, true)}, nil
}
func (c *Cache) Get() *Manifest {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return cloneManifest(c.manifest, true)
}
func (c *Cache) Refresh(data []byte) error {
	m, err := Parse(data)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.manifest = m
	c.mu.Unlock()
	return nil
}

// Catalog downloads and caches the manifest. A failed refresh never replaces
// the last valid manifest.
type Catalog struct {
	manifestURL string
	client      *http.Client
	refreshMu   sync.Mutex
	mu          sync.RWMutex
	manifest    *Manifest
}

func New(manifestURL string, client *http.Client) (*Catalog, error) {
	u, err := url.Parse(strings.TrimSpace(manifestURL))
	if err != nil || !strings.EqualFold(u.Scheme, "https") || u.Host == "" {
		return nil, errors.New("manifest URL must be HTTPS")
	}
	if client == nil {
		client = http.DefaultClient
	}
	return &Catalog{manifestURL: u.String(), client: client}, nil
}

func (c *Catalog) Refresh(ctx context.Context) error {
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.manifestURL, nil)
	if err != nil {
		return fmt.Errorf("create manifest request: %w", err)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("fetch manifest: %w", err)
	}
	defer resp.Body.Close()
	if resp.Request != nil && !strings.EqualFold(resp.Request.URL.Scheme, "https") {
		return errors.New("manifest request redirected away from HTTPS")
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fetch manifest: HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxManifestBytes+1))
	if err != nil {
		return fmt.Errorf("read manifest: %w", err)
	}
	if len(data) > maxManifestBytes {
		return fmt.Errorf("manifest exceeds %d bytes", maxManifestBytes)
	}
	manifest, err := Parse(data)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.manifest = manifest
	c.mu.Unlock()
	return nil
}

// Manifest returns a public copy with signed resource URLs removed.
func (c *Catalog) Manifest() (*Manifest, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.manifest == nil {
		return nil, ErrUnavailable
	}
	return cloneManifest(c.manifest, false), nil
}

// Resolve returns the selected resource and whether its release is the
// publisher-designated latest version.
func (c *Catalog) Resolve(version, resourceID string) (Resource, bool, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.manifest == nil {
		return Resource{}, false, ErrUnavailable
	}
	for _, release := range c.manifest.Releases {
		if release.Version != version {
			continue
		}
		for _, resource := range release.Resources {
			if resource.ID == resourceID {
				return resource, version == c.manifest.LatestVersion, nil
			}
		}
		return Resource{}, false, fmt.Errorf("%w: %s/%s", ErrResourceNotFound, version, resourceID)
	}
	return Resource{}, false, fmt.Errorf("%w: %s", ErrReleaseNotFound, version)
}
