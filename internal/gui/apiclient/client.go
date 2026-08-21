package apiclient

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	EventOutput = "output"
	EventFinal  = "final"

	DownloadStateQueued      = "queued"
	DownloadStateDownloading = "downloading"
	DownloadStateVerifying   = "verifying"
	DownloadStateCompleted   = "completed"
	DownloadStateFailed      = "failed"
	DownloadStateCancelled   = "cancelled"
)

type Client struct {
	baseURL    string
	username   string
	password   string
	httpClient *http.Client
}

type StreamEvent struct {
	Type       string    `json:"type"`
	Script     string    `json:"script"`
	Stream     string    `json:"stream,omitempty"`
	Data       string    `json:"data,omitempty"`
	ExitCode   int       `json:"exitCode,omitempty"`
	TimedOut   bool      `json:"timedOut,omitempty"`
	StartedAt  time.Time `json:"startedAt,omitempty"`
	FinishedAt time.Time `json:"finishedAt,omitempty"`
	DurationMs int64     `json:"durationMs,omitempty"`
	Error      string    `json:"error,omitempty"`
}

type RunStreamOptions struct {
	Args        []string
	PreDownload PreDownloadOptions
}

type PreDownloadOptions struct {
	Enabled  bool   `json:"enabled"`
	Project  string `json:"project"`
	Artifact string `json:"artifact"`
}

type runStreamRequest struct {
	Script      string              `json:"script"`
	Args        []string            `json:"args,omitempty"`
	PreDownload *PreDownloadOptions `json:"preDownload,omitempty"`
}

type HTTPError struct {
	StatusCode int
	Message    string
}

// ReleaseManifest is the release information exposed by the service. Resource
// URLs are intentionally not part of this client DTO: downloads are started by
// resource identity and signed URLs must never be handed to the GUI.
type ReleaseManifest struct {
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
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type DownloadInfo struct {
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

func (e HTTPError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("HTTP %d", e.StatusCode)
	}
	return fmt.Sprintf("HTTP %d: %s", e.StatusCode, e.Message)
}

func New(baseURL, username, password string) *Client {
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		username:   username,
		password:   password,
		httpClient: &http.Client{},
	}
}

func (c *Client) Health(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/health", nil)
	if err != nil {
		return err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return HTTPError{StatusCode: resp.StatusCode}
	}
	return nil
}

func (c *Client) Scripts(ctx context.Context) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/scripts", nil)
	if err != nil {
		return nil, err
	}
	c.setAuth(req)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, decodeHTTPError(resp)
	}
	var body struct {
		Scripts []string `json:"scripts"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	return body.Scripts, nil
}

func (c *Client) Releases(ctx context.Context) (*ReleaseManifest, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/releases", nil)
	if err != nil {
		return nil, err
	}
	c.setAuth(req)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, decodeHTTPError(resp)
	}
	var manifest ReleaseManifest
	if err := json.NewDecoder(resp.Body).Decode(&manifest); err != nil {
		return nil, err
	}
	return &manifest, nil
}

func (c *Client) RefreshReleases(ctx context.Context) (*ReleaseManifest, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/releases/refresh", nil)
	if err != nil {
		return nil, err
	}
	c.setAuth(req)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, decodeHTTPError(resp)
	}
	var manifest ReleaseManifest
	if err := json.NewDecoder(resp.Body).Decode(&manifest); err != nil {
		return nil, err
	}
	return &manifest, nil
}

func (c *Client) StartDownload(ctx context.Context, version, resourceID string) (DownloadInfo, error) {
	body, err := json.Marshal(struct {
		Version    string `json:"version"`
		ResourceID string `json:"resourceId"`
	}{version, resourceID})
	if err != nil {
		return DownloadInfo{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/downloads", bytes.NewReader(body))
	if err != nil {
		return DownloadInfo{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	c.setAuth(req)
	return c.decodeDownloadResponse(req)
}

func (c *Client) DownloadStatus(ctx context.Context, id string) (DownloadInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/downloads/"+url.PathEscape(id), nil)
	if err != nil {
		return DownloadInfo{}, err
	}
	c.setAuth(req)
	return c.decodeDownloadResponse(req)
}

func (c *Client) CancelDownload(ctx context.Context, id string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/downloads/"+url.PathEscape(id)+"/cancel", nil)
	if err != nil {
		return err
	}
	c.setAuth(req)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return decodeHTTPError(resp)
	}
	return nil
}

func (c *Client) decodeDownloadResponse(req *http.Request) (DownloadInfo, error) {
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return DownloadInfo{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return DownloadInfo{}, decodeHTTPError(resp)
	}
	var info DownloadInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return DownloadInfo{}, err
	}
	return info, nil
}

func (c *Client) RunStream(ctx context.Context, script string, onEvent func(StreamEvent)) error {
	return c.RunStreamWithOptions(ctx, script, RunStreamOptions{}, onEvent)
}

func (c *Client) RunStreamWithOptions(ctx context.Context, script string, opts RunStreamOptions, onEvent func(StreamEvent)) error {
	reqBody := runStreamRequest{Script: script, Args: opts.Args}
	if opts.PreDownload.Enabled {
		preDownload := opts.PreDownload
		reqBody.PreDownload = &preDownload
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/run/stream", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	c.setAuth(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return decodeHTTPError(resp)
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)
	sawFinal := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var event StreamEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			return err
		}
		if event.Type == EventFinal {
			sawFinal = true
		}
		if onEvent != nil {
			onEvent(event)
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if !sawFinal {
		return fmt.Errorf("stream ended before final event")
	}
	return nil
}

func (c *Client) setAuth(req *http.Request) {
	req.SetBasicAuth(c.username, c.password)
}

func decodeHTTPError(resp *http.Response) error {
	data, _ := io.ReadAll(resp.Body)
	var body struct {
		Error string `json:"error"`
	}
	message := ""
	if len(data) > 0 && json.Unmarshal(data, &body) == nil {
		message = body.Error
	}
	if message == "" {
		message = strings.TrimSpace(string(data))
	}
	if message == "" {
		message = http.StatusText(resp.StatusCode)
	}
	return HTTPError{StatusCode: resp.StatusCode, Message: message}
}
