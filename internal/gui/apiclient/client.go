package apiclient

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	EventOutput = "output"
	EventFinal  = "final"
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

type HTTPError struct {
	StatusCode int
	Message    string
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

func (c *Client) RunStream(ctx context.Context, script string, onEvent func(StreamEvent)) error {
	body, err := json.Marshal(map[string]string{"script": script})
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
