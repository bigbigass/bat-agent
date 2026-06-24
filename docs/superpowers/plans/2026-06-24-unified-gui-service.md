# Unified GUI Service Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `deploy-agent-gui.exe` the single daily-use program: it starts the existing service stack inside the GUI process, connects through the local HTTP API without asking for local credentials, and stays alive in the system tray.

**Architecture:** Extract the current root `main.go` service startup logic into `internal/appservice`, then have both the console service entry and the GUI entry call that package. The GUI still uses the existing HTTP client and `/run/stream` API, but local mode obtains Base URL and Basic Auth from the embedded service instead of GUI input fields. Fyne close interception and desktop tray APIs keep the process running until the tray exit action shuts the service down.

**Tech Stack:** Go, standard library `net/http` / `net` / `context` / `os/signal`, existing `internal/config` / `registry` / `executor` / `httpapi` / `mqttapi`, Fyne v2 desktop tray APIs, Windows batch build.

---

## File Structure

- Create `internal/appservice/service.go`
  - Owns service lifecycle: config loading, registry watch, executor creation, HTTP server, MQTT startup, shutdown, local GUI HTTP client config.
- Create `internal/appservice/service_test.go`
  - Tests service helper behavior and HTTP lifecycle without involving Fyne.
- Modify `main.go`
  - Becomes a thin console entry that starts `internal/appservice.Service`, waits for signal or service error, then shuts down.
- Delete `main_test.go`
  - Its helper coverage moves into `internal/appservice/service_test.go`.
- Modify `internal/gui/guiconfig/config.go`
  - Local mode defaults to no GUI-stored HTTP credentials.
  - Adds `ForSave` so local mode does not persist local account/password fields.
- Modify `internal/gui/guiconfig/config_test.go`
  - Covers local no-credential defaults and save sanitization.
- Create `cmd/deploy-agent-gui/connection.go`
  - Chooses local embedded service connection config or remote GUI config.
- Modify `cmd/deploy-agent-gui/main_test.go`
  - Adds tests proving local mode ignores GUI config credentials.
- Create `cmd/deploy-agent-gui/tray.go`
  - Installs Fyne system tray menu and close interception.
- Create `cmd/deploy-agent-gui/tray_test.go`
  - Tests tray decision logic through pure callbacks.
- Modify `cmd/deploy-agent-gui/main.go`
  - Starts embedded service, removes external `deploy-agent.exe` management, hides local credential fields, and wires tray exit.
- Delete `internal/gui/localservice/service.go`
  - External process management is no longer part of GUI local mode.
- Delete `internal/gui/localservice/service_test.go`
  - Tests are obsolete after deleting external process management.
- Modify `README.md`
  - Documents one-program daily use, local no-password UI, tray behavior, and retained HTTP/MQTT APIs.
- Modify `build.bat`
  - Keep both artifacts, but update messages/comments so `deploy-agent-gui.exe` is the primary daily-use artifact.

## Task 1: Extract App Service Helpers

**Files:**
- Create: `internal/appservice/service.go`
- Create: `internal/appservice/service_test.go`

- [ ] **Step 1: Write failing helper tests**

Create `internal/appservice/service_test.go` with:

```go
package appservice

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/liqixin/deploy-agent/internal/config"
	"github.com/liqixin/deploy-agent/internal/executor"
	"github.com/liqixin/deploy-agent/internal/mqttapi"
	"github.com/liqixin/deploy-agent/internal/registry"
)

func TestHTTPBaseURLForConfigUsesLoopbackForWildcardHosts(t *testing.T) {
	tests := []string{"", "0.0.0.0", "::", "[::]"}
	for _, host := range tests {
		t.Run(host, func(t *testing.T) {
			cfg := &config.Config{Server: config.ServerConfig{Host: host, Port: 8080}}
			got := HTTPBaseURLForConfig(cfg)
			if got != "http://127.0.0.1:8080" {
				t.Fatalf("HTTPBaseURLForConfig(%q) = %q, want loopback URL", host, got)
			}
		})
	}
}

func TestHTTPBaseURLForConfigPreservesSpecificHost(t *testing.T) {
	cfg := &config.Config{Server: config.ServerConfig{Host: "192.168.1.50", Port: 9090}}
	got := HTTPBaseURLForConfig(cfg)
	if got != "http://192.168.1.50:9090" {
		t.Fatalf("HTTPBaseURLForConfig = %q, want specific host URL", got)
	}
}

func TestHTTPClientConfigReturnsEmbeddedServiceAuth(t *testing.T) {
	svc := &Service{
		cfg: &config.Config{
			Services: config.ServicesConfig{
				HTTP: config.HTTPServiceConfig{Enabled: true},
			},
			Auth: config.AuthConfig{
				Username: "admin",
				Password: "change-me-please",
			},
		},
		httpBaseURL: "http://127.0.0.1:8080",
	}

	got, ok := svc.HTTPClientConfig()
	if !ok {
		t.Fatal("HTTPClientConfig ok = false, want true")
	}
	if got.BaseURL != "http://127.0.0.1:8080" {
		t.Fatalf("BaseURL = %q", got.BaseURL)
	}
	if got.Username != "admin" {
		t.Fatalf("Username = %q", got.Username)
	}
	if got.Password != "change-me-please" {
		t.Fatalf("Password = %q", got.Password)
	}
}

func TestHTTPClientConfigUnavailableWhenHTTPDisabled(t *testing.T) {
	svc := &Service{
		cfg: &config.Config{
			Services: config.ServicesConfig{
				HTTP: config.HTTPServiceConfig{Enabled: false},
			},
			Auth: config.AuthConfig{Username: "admin", Password: "change-me-please"},
		},
		httpBaseURL: "",
	}

	if got, ok := svc.HTTPClientConfig(); ok {
		t.Fatalf("HTTPClientConfig = %#v, want unavailable", got)
	}
}

func TestExecutorOptionsFromConfigMapsPreRunDownload(t *testing.T) {
	configDir := t.TempDir()
	writeTestScript(t, configDir, filepath.Join("tools", "download_simple.bat"), "@echo off\r\necho download %~1 %~2\r\n")
	cfgPath := filepath.Join(configDir, "config.yaml")
	cfg := &config.Config{
		PreRun: config.PreRunConfig{
			Download: config.PreRunDownloadConfig{
				Script:         "tools/download_simple.bat",
				TimeoutSeconds: 123,
			},
		},
	}

	opts, err := executorOptionsFromConfig(cfg, cfgPath)
	if err != nil {
		t.Fatalf("executorOptionsFromConfig returned error: %v", err)
	}
	if len(opts) != 1 {
		t.Fatalf("options len = %d, want 1", len(opts))
	}

	regDir := t.TempDir()
	writeTestScript(t, regDir, "deploy.bat", "@echo off\r\necho target\r\n")
	reg, err := registry.New(regDir)
	if err != nil {
		t.Fatal(err)
	}
	exec := executor.New(reg, 5*time.Second, opts...)

	res, err := exec.RunCollectWithOptions(context.Background(), "deploy.bat", executor.RunOptions{
		PreDownload: executor.PreDownloadRequest{
			Enabled:  true,
			Project:  "ProjectA",
			Artifact: "app.zip",
		},
	})
	if err != nil {
		t.Fatalf("RunCollectWithOptions returned error: %v", err)
	}
	if !strings.Contains(res.Stdout, "download ProjectA app.zip") {
		t.Fatalf("Stdout = %q, want config-relative download output", res.Stdout)
	}
	if !strings.Contains(res.Stdout, "target") {
		t.Fatalf("Stdout = %q, want target output", res.Stdout)
	}
}

func TestExecutorOptionsFromConfigMapsPreRunDownloadTimeout(t *testing.T) {
	configDir := t.TempDir()
	writeTestScript(t, configDir, filepath.Join("tools", "download_slow.bat"), "@echo off\r\nping -n 3 127.0.0.1 >nul\r\n")
	cfgPath := filepath.Join(configDir, "config.yaml")
	cfg := &config.Config{
		PreRun: config.PreRunConfig{
			Download: config.PreRunDownloadConfig{
				Script:         "tools/download_slow.bat",
				TimeoutSeconds: 1,
			},
		},
	}

	opts, err := executorOptionsFromConfig(cfg, cfgPath)
	if err != nil {
		t.Fatalf("executorOptionsFromConfig returned error: %v", err)
	}
	if len(opts) != 1 {
		t.Fatalf("options len = %d, want 1", len(opts))
	}

	regDir := t.TempDir()
	writeTestScript(t, regDir, "deploy.bat", "@echo off\r\necho target\r\n")
	reg, err := registry.New(regDir)
	if err != nil {
		t.Fatal(err)
	}
	exec := executor.New(reg, 5*time.Second, opts...)

	res, err := exec.RunCollectWithOptions(context.Background(), "deploy.bat", executor.RunOptions{
		PreDownload: executor.PreDownloadRequest{
			Enabled:  true,
			Project:  "ProjectA",
			Artifact: "app.zip",
		},
	})

	if !errors.Is(err, executor.ErrPreDownloadTimedOut) {
		t.Fatalf("expected ErrPreDownloadTimedOut, got %v", err)
	}
	if !res.TimedOut {
		t.Fatal("TimedOut = false, want true")
	}
}

func TestExecutorOptionsFromConfigSkipsEmptyPreRunDownload(t *testing.T) {
	cfg := &config.Config{}

	opts, err := executorOptionsFromConfig(cfg, filepath.Join(t.TempDir(), "config.yaml"))
	if err != nil {
		t.Fatalf("executorOptionsFromConfig returned error: %v", err)
	}
	if len(opts) != 0 {
		t.Fatalf("options len = %d, want 0", len(opts))
	}
}

func TestMQTTConfigFromConfigMapsServiceFields(t *testing.T) {
	cfg := &config.Config{
		Services: config.ServicesConfig{
			MQTT: config.MQTTServiceConfig{
				Broker:       "ssl://mqtt.example.com:8883",
				ClientID:     "agent-1",
				Username:     "mqtt-user",
				Password:     "mqtt-password",
				CommandTopic: "agents/agent-1/run",
				QoS:          2,
			},
		},
	}

	got := mqttConfigFromConfig(cfg)
	want := mqttapi.Config{
		Broker:       "ssl://mqtt.example.com:8883",
		ClientID:     "agent-1",
		Username:     "mqtt-user",
		Password:     "mqtt-password",
		CommandTopic: "agents/agent-1/run",
		QoS:          2,
	}

	if got != want {
		t.Fatalf("mqttConfigFromConfig() = %#v, want %#v", got, want)
	}
}

func writeTestScript(t *testing.T, dir, name, body string) string {
	t.Helper()

	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
```

- [ ] **Step 2: Run helper tests to verify they fail**

Run:

```cmd
go test ./internal/appservice
```

Expected: FAIL because `internal/appservice` does not exist.

- [ ] **Step 3: Implement helper skeleton**

Create `internal/appservice/service.go` with:

```go
package appservice

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/liqixin/deploy-agent/internal/config"
	"github.com/liqixin/deploy-agent/internal/executor"
	"github.com/liqixin/deploy-agent/internal/mqttapi"
)

type Options struct {
	ConfigPath string
}

type HTTPClientConfig struct {
	BaseURL  string
	Username string
	Password string
}

type Service struct {
	options     Options
	cfg         *config.Config
	cfgPath     string
	httpBaseURL string
}

func New(options Options) *Service {
	return &Service{options: options}
}

func (s *Service) HTTPClientConfig() (HTTPClientConfig, bool) {
	if s.cfg == nil || !s.cfg.Services.HTTP.Enabled || s.httpBaseURL == "" {
		return HTTPClientConfig{}, false
	}
	return HTTPClientConfig{
		BaseURL:  s.httpBaseURL,
		Username: s.cfg.Auth.Username,
		Password: s.cfg.Auth.Password,
	}, true
}

func HTTPBaseURLForConfig(cfg *config.Config) string {
	host := localConnectHost(cfg.Server.Host)
	return "http://" + net.JoinHostPort(host, strconv.Itoa(cfg.Server.Port))
}

func localConnectHost(host string) string {
	host = strings.TrimSpace(host)
	switch host {
	case "", "0.0.0.0", "::", "[::]":
		return "127.0.0.1"
	default:
		return host
	}
}

func mqttConfigFromConfig(cfg *config.Config) mqttapi.Config {
	return mqttapi.Config{
		Broker:       cfg.Services.MQTT.Broker,
		ClientID:     cfg.Services.MQTT.ClientID,
		Username:     cfg.Services.MQTT.Username,
		Password:     cfg.Services.MQTT.Password,
		CommandTopic: cfg.Services.MQTT.CommandTopic,
		QoS:          byte(cfg.Services.MQTT.QoS),
	}
}

func executorOptionsFromConfig(cfg *config.Config, cfgPath string) ([]executor.Option, error) {
	downloadScript, err := cfg.ResolvePreRunDownloadScript(cfgPath)
	if err != nil {
		return nil, fmt.Errorf("resolve preRun.download.script: %w", err)
	}
	if downloadScript == "" {
		return nil, nil
	}
	return []executor.Option{
		executor.WithPreDownloadConfig(executor.PreDownloadConfig{
			ScriptPath: downloadScript,
			Timeout:    time.Duration(cfg.PreRun.Download.TimeoutSeconds) * time.Second,
		}),
	}, nil
}

func FindConfig() (string, error) {
	candidates := []string{}
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), "config.yaml"))
	}
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(cwd, "config.yaml"))
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("config.yaml not found (looked in: %v). Copy config.example.yaml to config.yaml and edit it", candidates)
}
```

- [ ] **Step 4: Run helper tests to verify they pass**

Run:

```cmd
go test ./internal/appservice
```

Expected: PASS.

- [ ] **Step 5: Commit helper extraction**

Run:

```cmd
gofmt -w internal/appservice
go test ./internal/appservice
git add internal/appservice
git commit -m "feat: add app service helpers"
```

Expected: commit succeeds with only `internal/appservice` files staged.

## Task 2: Implement App Service Lifecycle

**Files:**
- Modify: `internal/appservice/service_test.go`
- Modify: `internal/appservice/service.go`

- [ ] **Step 1: Add lifecycle tests**

Append these tests and helpers to `internal/appservice/service_test.go`:

```go
func TestServiceStartExposesHealthAndShutdownStopsHTTP(t *testing.T) {
	dir := t.TempDir()
	port := freePort(t)
	cfgPath := writeConfig(t, dir, port, true, false)

	svc := New(Options{ConfigPath: cfgPath})
	if err := svc.Start(context.Background()); err != nil {
		t.Fatalf("Start returned error: %v", err)
	}

	resp, err := httpGet(svc.HTTPBaseURL() + "/health")
	if err != nil {
		t.Fatalf("GET /health returned error: %v", err)
	}
	if resp != http.StatusOK {
		t.Fatalf("GET /health status = %d, want 200", resp)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := svc.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown returned error: %v", err)
	}
}

func TestServiceStartReturnsListenErrorWhenPortBusy(t *testing.T) {
	dir := t.TempDir()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	cfgPath := writeConfig(t, dir, port, true, false)

	svc := New(Options{ConfigPath: cfgPath})
	err = svc.Start(context.Background())
	if err == nil {
		t.Fatal("Start returned nil, want listen error")
	}
	if !strings.Contains(err.Error(), "listen http") {
		t.Fatalf("Start error = %q, want listen context", err.Error())
	}
}

func TestServiceWaitReturnsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	svc := New(Options{})
	if err := svc.Wait(ctx); err != nil {
		t.Fatalf("Wait returned %v, want nil on context cancellation", err)
	}
}

func TestServiceStartValidatesConfig(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("server:\n  port: 70000\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	svc := New(Options{ConfigPath: cfgPath})
	err := svc.Start(context.Background())
	if err == nil {
		t.Fatal("Start returned nil, want config validation error")
	}
	if !strings.Contains(err.Error(), "server.port") {
		t.Fatalf("Start error = %q, want server.port validation", err.Error())
	}
}

func writeConfig(t *testing.T, dir string, port int, httpEnabled bool, mqttEnabled bool) string {
	t.Helper()

	scriptDir := filepath.Join(dir, "scripts")
	if err := os.MkdirAll(scriptDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "config.yaml")
	body := fmt.Sprintf(`server:
  host: 127.0.0.1
  port: %d
services:
  http:
    enabled: %t
  mqtt:
    enabled: %t
    broker: "tcp://127.0.0.1:1883"
    clientId: "deploy-agent-test"
    commandTopic: "deploy-agent-test/run"
    qos: 1
auth:
  username: admin
  password: change-me-please
runner:
  timeoutSeconds: 300
  scriptDir: %q
preRun:
  download:
    script: ""
    timeoutSeconds: 300
`, port, httpEnabled, mqttEnabled, filepath.ToSlash(scriptDir))
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return cfgPath
}

func freePort(t *testing.T) int {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func httpGet(url string) (int, error) {
	resp, err := http.Get(url)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	return resp.StatusCode, nil
}
```

Add these imports to `internal/appservice/service_test.go`:

```go
import (
	"fmt"
	"net"
	"net/http"
)
```

Keep the existing imports from Task 1 and let `gofmt` sort the final block.

- [ ] **Step 2: Run lifecycle tests to verify they fail**

Run:

```cmd
go test ./internal/appservice
```

Expected: FAIL because `Service.Start`, `Service.Shutdown`, `Service.Wait`, and `Service.HTTPBaseURL` are not implemented.

- [ ] **Step 3: Implement service lifecycle**

Replace `internal/appservice/service.go` with:

```go
package appservice

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/liqixin/deploy-agent/internal/auth"
	"github.com/liqixin/deploy-agent/internal/config"
	"github.com/liqixin/deploy-agent/internal/executor"
	"github.com/liqixin/deploy-agent/internal/httpapi"
	"github.com/liqixin/deploy-agent/internal/mqttapi"
	"github.com/liqixin/deploy-agent/internal/registry"
)

var ErrAlreadyStarted = errors.New("app service already started")

type Options struct {
	ConfigPath string
}

type HTTPClientConfig struct {
	BaseURL  string
	Username string
	Password string
}

type Service struct {
	options Options

	mu          sync.Mutex
	started     bool
	cfg         *config.Config
	cfgPath     string
	cancel      context.CancelFunc
	srv         *http.Server
	errCh       chan error
	httpBaseURL string
}

func New(options Options) *Service {
	return &Service{options: options}
}

func (s *Service) Start(parent context.Context) error {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return ErrAlreadyStarted
	}
	s.started = true
	s.errCh = make(chan error, 1)
	s.mu.Unlock()

	success := false
	var cancel context.CancelFunc
	defer func() {
		if success {
			return
		}
		if cancel != nil {
			cancel()
		}
		s.mu.Lock()
		s.started = false
		s.cancel = nil
		s.srv = nil
		s.cfg = nil
		s.cfgPath = ""
		s.httpBaseURL = ""
		s.mu.Unlock()
	}()

	cfgPath := s.options.ConfigPath
	if cfgPath == "" {
		var err error
		cfgPath, err = FindConfig()
		if err != nil {
			return err
		}
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	log.Printf("loaded config from %s", cfgPath)

	scriptDir, err := cfg.ResolveScriptDir()
	if err != nil {
		return fmt.Errorf("resolve scriptDir: %w", err)
	}
	reg, err := registry.New(scriptDir)
	if err != nil {
		return err
	}
	log.Printf("script dir: %s", reg.Dir())
	log.Printf("discovered %d script(s): %v", len(reg.List()), reg.List())

	ctx, cancel := context.WithCancel(parent)
	go reg.WatchRescan(ctx, 60*time.Second)

	timeout := time.Duration(cfg.Runner.TimeoutSeconds) * time.Second
	execOptions, err := executorOptionsFromConfig(cfg, cfgPath)
	if err != nil {
		return err
	}
	exec := executor.New(reg, timeout, execOptions...)

	if cfg.Services.MQTT.Enabled {
		mqttClient := mqttapi.NewClient(mqttConfigFromConfig(cfg), exec)
		if err := mqttClient.Start(ctx); err != nil {
			return fmt.Errorf("start mqtt: %w", err)
		}
	}

	var srv *http.Server
	httpBaseURL := ""
	if cfg.Services.HTTP.Enabled {
		api := httpapi.New(exec)
		authWrap := func(h http.Handler) http.Handler {
			return auth.BasicAuth(cfg.Auth.Username, cfg.Auth.Password, h)
		}
		addr := net.JoinHostPort(cfg.Server.Host, strconv.Itoa(cfg.Server.Port))
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			return fmt.Errorf("listen http %s: %w", addr, err)
		}
		srv = &http.Server{
			Addr:              addr,
			Handler:           api.Routes(authWrap),
			ReadHeaderTimeout: 10 * time.Second,
		}
		httpBaseURL = HTTPBaseURLForConfig(cfg)
		go func() {
			log.Printf("deploy-agent http listening on %s (timeout %s)", addr, timeout)
			if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				select {
				case s.errCh <- err:
				default:
				}
			}
		}()
	}

	s.mu.Lock()
	s.cfg = cfg
	s.cfgPath = cfgPath
	s.cancel = cancel
	s.srv = srv
	s.httpBaseURL = httpBaseURL
	s.mu.Unlock()
	success = true
	return nil
}

func (s *Service) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	if !s.started {
		s.mu.Unlock()
		return nil
	}
	cancel := s.cancel
	srv := s.srv
	s.started = false
	s.cancel = nil
	s.srv = nil
	s.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if srv != nil {
		return srv.Shutdown(ctx)
	}
	return nil
}

func (s *Service) Wait(ctx context.Context) error {
	s.mu.Lock()
	errCh := s.errCh
	s.mu.Unlock()
	if errCh == nil {
		<-ctx.Done()
		return nil
	}
	select {
	case <-ctx.Done():
		return nil
	case err := <-errCh:
		return err
	}
}

func (s *Service) HTTPBaseURL() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.httpBaseURL
}

func (s *Service) HTTPClientConfig() (HTTPClientConfig, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cfg == nil || !s.cfg.Services.HTTP.Enabled || s.httpBaseURL == "" {
		return HTTPClientConfig{}, false
	}
	return HTTPClientConfig{
		BaseURL:  s.httpBaseURL,
		Username: s.cfg.Auth.Username,
		Password: s.cfg.Auth.Password,
	}, true
}

func HTTPBaseURLForConfig(cfg *config.Config) string {
	host := localConnectHost(cfg.Server.Host)
	return "http://" + net.JoinHostPort(host, strconv.Itoa(cfg.Server.Port))
}

func localConnectHost(host string) string {
	host = strings.TrimSpace(host)
	switch host {
	case "", "0.0.0.0", "::", "[::]":
		return "127.0.0.1"
	default:
		return host
	}
}

func mqttConfigFromConfig(cfg *config.Config) mqttapi.Config {
	return mqttapi.Config{
		Broker:       cfg.Services.MQTT.Broker,
		ClientID:     cfg.Services.MQTT.ClientID,
		Username:     cfg.Services.MQTT.Username,
		Password:     cfg.Services.MQTT.Password,
		CommandTopic: cfg.Services.MQTT.CommandTopic,
		QoS:          byte(cfg.Services.MQTT.QoS),
	}
}

func executorOptionsFromConfig(cfg *config.Config, cfgPath string) ([]executor.Option, error) {
	downloadScript, err := cfg.ResolvePreRunDownloadScript(cfgPath)
	if err != nil {
		return nil, fmt.Errorf("resolve preRun.download.script: %w", err)
	}
	if downloadScript == "" {
		return nil, nil
	}
	return []executor.Option{
		executor.WithPreDownloadConfig(executor.PreDownloadConfig{
			ScriptPath: downloadScript,
			Timeout:    time.Duration(cfg.PreRun.Download.TimeoutSeconds) * time.Second,
		}),
	}, nil
}

func FindConfig() (string, error) {
	candidates := []string{}
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), "config.yaml"))
	}
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(cwd, "config.yaml"))
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("config.yaml not found (looked in: %v). Copy config.example.yaml to config.yaml and edit it", candidates)
}
```

- [ ] **Step 4: Run lifecycle tests**

Run:

```cmd
gofmt -w internal/appservice
go test ./internal/appservice
```

Expected: PASS.

- [ ] **Step 5: Commit lifecycle service**

Run:

```cmd
git add internal/appservice
git commit -m "feat: add app service lifecycle"
```

Expected: commit succeeds.

## Task 3: Refactor Console Entry To Use App Service

**Files:**
- Modify: `main.go`
- Delete: `main_test.go`

- [ ] **Step 1: Replace root main with thin service entry**

Replace `main.go` with:

```go
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/liqixin/deploy-agent/internal/appservice"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	if err := run(); err != nil {
		log.Printf("fatal: %v", err)
		fmt.Fprintln(os.Stderr, "\nPress Enter to exit...")
		_, _ = fmt.Scanln()
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	svc := appservice.New(appservice.Options{})
	if err := svc.Start(ctx); err != nil {
		return err
	}
	if err := svc.Wait(ctx); err != nil {
		return err
	}
	log.Printf("shutdown signal received")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return svc.Shutdown(shutdownCtx)
}
```

- [ ] **Step 2: Delete obsolete root tests**

Run:

```cmd
git rm main_test.go
```

Expected: `main_test.go` is staged for deletion. Its coverage now lives in `internal/appservice/service_test.go`.

- [ ] **Step 3: Run service and package tests**

Run:

```cmd
gofmt -w main.go
go test ./internal/appservice
go test ./...
```

Expected: PASS. The root package should have no stale tests after `main_test.go` is deleted.

- [ ] **Step 4: Commit console refactor**

Run:

```cmd
git add main.go main_test.go internal/appservice
git commit -m "refactor: use app service from console entry"
```

Expected: commit succeeds.

## Task 4: Stop Storing Local GUI Credentials

**Files:**
- Modify: `internal/gui/guiconfig/config.go`
- Modify: `internal/gui/guiconfig/config_test.go`

- [ ] **Step 1: Update GUI config tests**

Modify `internal/gui/guiconfig/config_test.go`:

Replace `TestDefaultConfig` with:

```go
func TestDefaultConfig(t *testing.T) {
	cfg := Default()

	if cfg.Mode != ModeLocal {
		t.Fatalf("Mode = %q, want %q", cfg.Mode, ModeLocal)
	}
	if cfg.BaseURL != "" {
		t.Fatalf("BaseURL = %q, want empty local default", cfg.BaseURL)
	}
	if cfg.Username != "" {
		t.Fatalf("Username = %q, want empty local default", cfg.Username)
	}
	if cfg.Password != "" {
		t.Fatalf("Password = %q, want empty", cfg.Password)
	}
}
```

Append these tests:

```go
func TestForSaveClearsLocalConnectionCredentials(t *testing.T) {
	cfg := Config{
		Mode:     ModeLocal,
		BaseURL:  "http://old-local:8080",
		Username: "admin",
		Password: "change-me-please",
	}

	got := ForSave(cfg)
	if got.Mode != ModeLocal {
		t.Fatalf("Mode = %q, want local", got.Mode)
	}
	if got.BaseURL != "" {
		t.Fatalf("BaseURL = %q, want empty", got.BaseURL)
	}
	if got.Username != "" {
		t.Fatalf("Username = %q, want empty", got.Username)
	}
	if got.Password != "" {
		t.Fatalf("Password = %q, want empty", got.Password)
	}
}

func TestForSaveKeepsRemoteConnectionCredentials(t *testing.T) {
	cfg := Config{
		Mode:     ModeRemote,
		BaseURL:  "http://10.0.0.5:8080",
		Username: "alice",
		Password: "secret-password",
	}

	got := ForSave(cfg)
	if got != cfg {
		t.Fatalf("ForSave remote = %#v, want %#v", got, cfg)
	}
}
```

- [ ] **Step 2: Run GUI config tests to verify they fail**

Run:

```cmd
go test ./internal/gui/guiconfig
```

Expected: FAIL because `Default` still sets local Base URL/username and `ForSave` does not exist.

- [ ] **Step 3: Implement local save sanitization**

Modify `internal/gui/guiconfig/config.go`:

Change `Default` to:

```go
func Default() Config {
	return Config{
		Mode: ModeLocal,
	}
}
```

Change the empty Base URL fallback in `Load` to apply only to remote mode:

```go
	if cfg.Mode == "" {
		cfg.Mode = ModeLocal
	}
	if cfg.Mode == ModeRemote && cfg.BaseURL == "" {
		cfg.BaseURL = "http://127.0.0.1:8080"
	}
```

Add this function before `Save`:

```go
func ForSave(cfg Config) Config {
	if cfg.Mode != ModeLocal {
		return cfg
	}
	cfg.BaseURL = ""
	cfg.Username = ""
	cfg.Password = ""
	return cfg
}
```

Change `Save` to sanitize before marshaling:

```go
func Save(path string, cfg Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	cfg = ForSave(cfg)
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o600)
}
```

- [ ] **Step 4: Run GUI config tests**

Run:

```cmd
gofmt -w internal/gui/guiconfig
go test ./internal/gui/guiconfig
```

Expected: PASS.

- [ ] **Step 5: Commit GUI config sanitization**

Run:

```cmd
git add internal/gui/guiconfig
git commit -m "feat: avoid storing local gui credentials"
```

Expected: commit succeeds.

## Task 5: Add GUI Connection Selection Helper

**Files:**
- Create: `cmd/deploy-agent-gui/connection.go`
- Modify: `cmd/deploy-agent-gui/main_test.go`

- [ ] **Step 1: Add connection helper tests**

Append this code to `cmd/deploy-agent-gui/main_test.go`:

```go
type fakeLocalHTTPProvider struct {
	cfg appservice.HTTPClientConfig
	ok  bool
}

func (f fakeLocalHTTPProvider) HTTPClientConfig() (appservice.HTTPClientConfig, bool) {
	return f.cfg, f.ok
}

func TestClientConfigForLocalModeUsesEmbeddedServiceAndIgnoresGUISecrets(t *testing.T) {
	guiCfg := guiconfig.Config{
		Mode:     guiconfig.ModeLocal,
		BaseURL:  "http://wrong:9999",
		Username: "wrong-user",
		Password: "wrong-password",
	}
	local := fakeLocalHTTPProvider{
		ok: true,
		cfg: appservice.HTTPClientConfig{
			BaseURL:  "http://127.0.0.1:8080",
			Username: "admin",
			Password: "change-me-please",
		},
	}

	got, err := clientConfigForMode(guiCfg.Mode, guiCfg, local)
	if err != nil {
		t.Fatalf("clientConfigForMode returned error: %v", err)
	}
	if got.BaseURL != "http://127.0.0.1:8080" {
		t.Fatalf("BaseURL = %q", got.BaseURL)
	}
	if got.Username != "admin" {
		t.Fatalf("Username = %q", got.Username)
	}
	if got.Password != "change-me-please" {
		t.Fatalf("Password = %q", got.Password)
	}
}

func TestClientConfigForLocalModeRequiresEmbeddedHTTP(t *testing.T) {
	_, err := clientConfigForMode(guiconfig.ModeLocal, guiconfig.Config{Mode: guiconfig.ModeLocal}, fakeLocalHTTPProvider{})
	if err == nil {
		t.Fatal("clientConfigForMode returned nil, want local service error")
	}
	if !strings.Contains(err.Error(), "本机服务 HTTP 未启用") {
		t.Fatalf("error = %q, want local HTTP message", err.Error())
	}
}

func TestClientConfigForRemoteModeUsesGUIConfig(t *testing.T) {
	guiCfg := guiconfig.Config{
		Mode:     guiconfig.ModeRemote,
		BaseURL:  "http://10.0.0.5:8080",
		Username: "alice",
		Password: "secret-password",
	}
	local := fakeLocalHTTPProvider{
		ok: true,
		cfg: appservice.HTTPClientConfig{
			BaseURL:  "http://127.0.0.1:8080",
			Username: "admin",
			Password: "change-me-please",
		},
	}

	got, err := clientConfigForMode(guiCfg.Mode, guiCfg, local)
	if err != nil {
		t.Fatalf("clientConfigForMode returned error: %v", err)
	}
	if got.BaseURL != guiCfg.BaseURL || got.Username != guiCfg.Username || got.Password != guiCfg.Password {
		t.Fatalf("client config = %#v, want remote GUI config", got)
	}
}
```

Add imports to `cmd/deploy-agent-gui/main_test.go`:

```go
	"github.com/liqixin/deploy-agent/internal/appservice"
	"github.com/liqixin/deploy-agent/internal/gui/guiconfig"
```

Let `gofmt` sort the final import block.

- [ ] **Step 2: Run GUI tests to verify they fail**

Run:

```cmd
go test ./cmd/deploy-agent-gui
```

Expected: FAIL because `clientConfigForMode` and `clientConfig` do not exist.

- [ ] **Step 3: Implement connection helper**

Create `cmd/deploy-agent-gui/connection.go` with:

```go
package main

import (
	"fmt"

	"github.com/liqixin/deploy-agent/internal/appservice"
	"github.com/liqixin/deploy-agent/internal/gui/guiconfig"
)

type clientConfig struct {
	BaseURL  string
	Username string
	Password string
}

type localHTTPProvider interface {
	HTTPClientConfig() (appservice.HTTPClientConfig, bool)
}

func clientConfigForMode(mode string, cfg guiconfig.Config, local localHTTPProvider) (clientConfig, error) {
	if mode == guiconfig.ModeLocal {
		if local == nil {
			return clientConfig{}, fmt.Errorf("本机服务 HTTP 未启用")
		}
		localCfg, ok := local.HTTPClientConfig()
		if !ok {
			return clientConfig{}, fmt.Errorf("本机服务 HTTP 未启用，请在 config.yaml 中启用 services.http.enabled")
		}
		return clientConfig{
			BaseURL:  localCfg.BaseURL,
			Username: localCfg.Username,
			Password: localCfg.Password,
		}, nil
	}
	return clientConfig{
		BaseURL:  cfg.BaseURL,
		Username: cfg.Username,
		Password: cfg.Password,
	}, nil
}
```

- [ ] **Step 4: Run GUI tests**

Run:

```cmd
gofmt -w cmd/deploy-agent-gui/connection.go cmd/deploy-agent-gui/main_test.go
go test ./cmd/deploy-agent-gui
```

Expected: PASS.

- [ ] **Step 5: Commit connection helper**

Run:

```cmd
git add cmd/deploy-agent-gui/connection.go cmd/deploy-agent-gui/main_test.go
git commit -m "feat: select embedded gui connection config"
```

Expected: commit succeeds.

## Task 6: Embed Service Into GUI Startup

**Files:**
- Modify: `cmd/deploy-agent-gui/main.go`
- Delete: `internal/gui/localservice/service.go`
- Delete: `internal/gui/localservice/service_test.go`

- [ ] **Step 1: Update GUI state fields and imports**

In `cmd/deploy-agent-gui/main.go`, remove these imports:

```go
	"os"

	"github.com/liqixin/deploy-agent/internal/gui/localservice"
```

Add this import:

```go
	"github.com/liqixin/deploy-agent/internal/appservice"
```

In `guiState`, remove:

```go
	service    *localservice.Manager

	startButton  *widget.Button
	stopButton   *widget.Button
```

Add:

```go
	service      *appservice.Service
	remoteFields fyne.CanvasObject
```

- [ ] **Step 2: Start embedded service from GUI main**

In `main`, replace the executable/path based service initialization:

```go
	exe, _ := os.Executable()
	state := &guiState{
		configPath: cfgPath,
		config:     cfg,
		service:    localservice.New(localservice.AgentPath(exe)),
		statusText: binding.NewString(),
		outputText: binding.NewString(),
		history:    binding.NewStringList(),
	}
	state.setStatus("未连接")

	w.SetContent(state.buildUI())
	w.ShowAndRun()
```

with:

```go
	state := &guiState{
		configPath: cfgPath,
		config:     cfg,
		service:    appservice.New(appservice.Options{}),
		statusText: binding.NewString(),
		outputText: binding.NewString(),
		history:    binding.NewStringList(),
	}
	state.setStatus("服务启动中...")

	w.SetContent(state.buildUI())
	state.startEmbeddedService()
	w.ShowAndRun()
```

- [ ] **Step 3: Replace local process controls with embedded service status**

In `buildUI`, remove:

```go
	s.startButton = widget.NewButton("启动服务", s.startLocalService)
	s.stopButton = widget.NewButton("停止服务", s.stopLocalService)
```

Replace the `mode` select callback:

```go
	mode := widget.NewSelect([]string{guiconfig.ModeLocal, guiconfig.ModeRemote}, func(value string) {
		s.config.Mode = value
		s.updateLocalButtons()
	})
```

with:

```go
	mode := widget.NewSelect([]string{guiconfig.ModeLocal, guiconfig.ModeRemote}, func(value string) {
		s.config.Mode = value
		s.updateModeControls()
		s.updateRunButton()
	})
```

Replace the connection form and action section:

```go
	connectionForm := widget.NewForm(
		widget.NewFormItem("模式", mode),
		widget.NewFormItem("服务地址", baseURL),
		widget.NewFormItem("用户名", username),
		widget.NewFormItem("密码", password),
	)
	mode.SetSelected(s.config.Mode)
	s.updateLocalButtons()

	actions := container.NewHBox(connect, save, s.startButton, s.stopButton)
	top := container.NewBorder(nil, nil, nil, actions, connectionForm)
```

with:

```go
	modeForm := widget.NewForm(widget.NewFormItem("模式", mode))
	s.remoteFields = widget.NewForm(
		widget.NewFormItem("服务地址", baseURL),
		widget.NewFormItem("用户名", username),
		widget.NewFormItem("密码", password),
	)
	mode.SetSelected(s.config.Mode)
	s.updateModeControls()

	actions := container.NewHBox(connect, save)
	top := container.NewBorder(nil, nil, nil, actions, container.NewVBox(modeForm, s.remoteFields))
```

In the save button callback, replace:

```go
		if err := guiconfig.Save(s.configPath, s.config); err != nil {
```

with:

```go
		if err := guiconfig.Save(s.configPath, guiconfig.ForSave(s.config)); err != nil {
```

- [ ] **Step 4: Route GUI connection through embedded service config**

In `connectWithRetry`, replace:

```go
	client := apiclient.New(s.config.BaseURL, s.config.Username, s.config.Password)
```

with:

```go
	clientCfg, err := clientConfigForMode(s.config.Mode, s.config, s.service)
	if err != nil {
		s.setStatus(err.Error())
		return
	}
	client := apiclient.New(clientCfg.BaseURL, clientCfg.Username, clientCfg.Password)
```

- [ ] **Step 5: Add embedded service startup and mode controls**

Add these methods to `cmd/deploy-agent-gui/main.go`:

```go
func (s *guiState) startEmbeddedService() {
	go func() {
		err := s.service.Start(context.Background())
		fyne.Do(func() {
			if err != nil {
				s.setStatus("服务启动失败: " + err.Error())
				s.updateRunButton()
				return
			}
			if s.config.Mode == guiconfig.ModeLocal {
				s.connectWithRetry(20, 500*time.Millisecond, "服务已启动，连接中...")
				return
			}
			s.setStatus("服务已启动")
		})
	}()
}

func (s *guiState) updateModeControls() {
	if s.remoteFields == nil {
		return
	}
	if s.config.Mode == guiconfig.ModeLocal {
		s.remoteFields.Hide()
		return
	}
	s.remoteFields.Show()
}
```

- [ ] **Step 6: Remove external localservice methods**

Delete these methods from `cmd/deploy-agent-gui/main.go`:

```go
func (s *guiState) updateLocalButtons()
func (s *guiState) startLocalService()
func (s *guiState) stopLocalService()
```

Run:

```cmd
git rm internal/gui/localservice/service.go internal/gui/localservice/service_test.go
```

- [ ] **Step 7: Run GUI tests**

Run:

```cmd
gofmt -w cmd/deploy-agent-gui
go test ./cmd/deploy-agent-gui
go test ./internal/gui/...
go test ./...
```

Expected: PASS. The deleted `internal/gui/localservice` package should no longer appear in `go test ./...`.

- [ ] **Step 8: Commit embedded GUI service startup**

Run:

```cmd
git add cmd/deploy-agent-gui internal/gui/localservice
git commit -m "feat: start embedded service from gui"
```

Expected: commit succeeds.

## Task 7: Add Tray Close And Exit Behavior

**Files:**
- Create: `cmd/deploy-agent-gui/tray.go`
- Create: `cmd/deploy-agent-gui/tray_test.go`
- Modify: `cmd/deploy-agent-gui/main.go`

- [ ] **Step 1: Add pure tray controller tests**

Create `cmd/deploy-agent-gui/tray_test.go` with:

```go
package main

import "testing"

func TestTrayControllerCloseHidesWindow(t *testing.T) {
	hidden := false
	closed := false
	controller := trayController{
		hide: func() { hidden = true },
		close: func() { closed = true },
	}

	controller.interceptClose()

	if !hidden {
		t.Fatal("hidden = false, want true")
	}
	if closed {
		t.Fatal("closed = true, want false while not exiting")
	}
}

func TestTrayControllerExitClosesWindowAndRunsShutdown(t *testing.T) {
	closed := false
	shutdown := false
	controller := trayController{
		close:    func() { closed = true },
		shutdown: func() { shutdown = true },
	}

	controller.exit()

	if !controller.exiting {
		t.Fatal("exiting = false, want true")
	}
	if !shutdown {
		t.Fatal("shutdown = false, want true")
	}
	if !closed {
		t.Fatal("closed = false, want true")
	}
}

func TestTrayControllerOpenShowsAndFocusesWindow(t *testing.T) {
	shown := false
	focused := false
	controller := trayController{
		show:  func() { shown = true },
		focus: func() { focused = true },
	}

	controller.open()

	if !shown {
		t.Fatal("shown = false, want true")
	}
	if !focused {
		t.Fatal("focused = false, want true")
	}
}
```

- [ ] **Step 2: Run tray tests to verify they fail**

Run:

```cmd
go test ./cmd/deploy-agent-gui
```

Expected: FAIL because `trayController` does not exist.

- [ ] **Step 3: Implement tray wiring**

Create `cmd/deploy-agent-gui/tray.go` with:

```go
package main

import (
	"context"
	"log"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver/desktop"
)

type trayController struct {
	exiting  bool
	hide     func()
	show     func()
	focus    func()
	close    func()
	shutdown func()
	quit     func()
}

func (c *trayController) interceptClose() {
	if c.exiting {
		if c.close != nil {
			c.close()
		}
		return
	}
	if c.hide != nil {
		c.hide()
	}
}

func (c *trayController) open() {
	if c.show != nil {
		c.show()
	}
	if c.focus != nil {
		c.focus()
	}
}

func (c *trayController) exit() {
	if c.exiting {
		return
	}
	c.exiting = true
	if c.shutdown != nil {
		c.shutdown()
	}
	if c.close != nil {
		c.close()
	}
	if c.quit != nil {
		c.quit()
	}
}

func (s *guiState) installTray(a fyne.App, w fyne.Window) {
	controller := &trayController{
		hide:  w.Hide,
		show:  w.Show,
		focus: w.RequestFocus,
		close: func() {
			w.SetCloseIntercept(nil)
			w.Close()
		},
		shutdown: func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := s.service.Shutdown(ctx); err != nil {
				log.Printf("shutdown embedded service: %v", err)
			}
		},
		quit: a.Quit,
	}
	w.SetCloseIntercept(controller.interceptClose)

	desktopApp, ok := a.(desktop.App)
	if !ok {
		return
	}
	desktopApp.SetSystemTrayWindow(w)
	desktopApp.SetSystemTrayMenu(fyne.NewMenu("deploy-agent",
		fyne.NewMenuItem("打开", controller.open),
		fyne.NewMenuItem("退出", controller.exit),
	))
}
```

- [ ] **Step 4: Wire tray install in GUI main**

In `cmd/deploy-agent-gui/main.go`, after:

```go
	w.SetContent(state.buildUI())
```

add:

```go
	state.installTray(a, w)
```

The block should become:

```go
	w.SetContent(state.buildUI())
	state.installTray(a, w)
	state.startEmbeddedService()
	w.ShowAndRun()
```

- [ ] **Step 5: Run tray and GUI tests**

Run:

```cmd
gofmt -w cmd/deploy-agent-gui
go test ./cmd/deploy-agent-gui
go test ./...
```

Expected: PASS.

- [ ] **Step 6: Commit tray behavior**

Run:

```cmd
git add cmd/deploy-agent-gui
git commit -m "feat: keep gui service in system tray"
```

Expected: commit succeeds.

## Task 8: Update README And Build Messaging

**Files:**
- Modify: `README.md`
- Modify: `build.bat`

- [ ] **Step 1: Update README product description**

In `README.md`, replace the opening description that says the project is only a Windows Go HTTP service with text that states:

```markdown
一个跑在 Windows 上的 Go 管理工具。日常使用启动 `deploy-agent-gui.exe`：它既是桌面 GUI，也是后台 HTTP/MQTT 服务，会从配置目录扫描白名单 `.bat` / `.cmd` 脚本并以管理员权限执行。HTTP API 仍然保留，便于 curl、远程调用和集成系统触发脚本。
```

- [ ] **Step 2: Update build artifact section**

Replace the current artifact paragraph:

```markdown
产物：`deploy-agent.exe` 和 `deploy-agent-gui.exe`。二者都内嵌 UAC 清单，双击会弹管理员确认框。
```

with:

```markdown
产物：`deploy-agent-gui.exe` 和兼容用的 `deploy-agent.exe`。日常使用优先启动 `deploy-agent-gui.exe`，它会在 GUI 进程内启动服务；`deploy-agent.exe` 只是无界面的后台入口。二者都内嵌 UAC 清单，双击会弹管理员确认框。
```

- [ ] **Step 3: Replace GUI management section**

Replace the `## GUI 管理端` section through the end of its “第一版限制” list with:

```markdown
## GUI 管理端

`deploy-agent-gui.exe` 是主要入口：它既是 Windows 桌面程序，也是 `deploy-agent` 后台服务。

本机模式：

- 直接启动 `deploy-agent-gui.exe`。
- GUI 本机模式需要管理员权限；`build.bat` 生成的 GUI 已内嵌 `requireAdministrator` 清单，启动时会弹 UAC。
- GUI 会在同一进程内启动 HTTP/MQTT 服务，不再要求同目录存在 `deploy-agent.exe`。
- 本机模式不需要在界面里输入服务地址、用户名或密码；GUI 会使用 `config.yaml` 中的 `server` 和 `auth` 配置连接内嵌服务。
- 关闭窗口只会隐藏到系统托盘，服务继续运行。
- 从托盘菜单选择“打开”可恢复窗口，选择“退出”才会停止内嵌服务并结束进程。

远程模式：

- 填写远程服务地址、HTTP Basic Auth 用户名和密码。
- GUI 通过 `/scripts` 列出脚本，通过 `/run/stream` 执行脚本并实时显示输出。
- 远程模式不会管理本机内嵌服务以外的任何远程进程。

GUI 会把远程模式的服务地址、用户名和密码保存到本地配置文件，默认在用户配置目录下的 `deploy-agent-gui/config.json`。本机模式不会保存本机服务账号密码；本机认证来自 `config.yaml`。远程密码是便捷存储，不是强安全存储，请保护该文件权限，不要提交或分享它。

第一版限制：

- 不支持脚本参数。
- 不支持中止正在运行的脚本。
- 不持久化执行历史。
- 不提供 Windows Service 注册。
```

- [ ] **Step 4: Update build.bat messages**

In `build.bat`, replace:

```bat
REM Build deploy-agent.exe and deploy-agent-gui.exe with UAC-elevating manifests embedded.
```

with:

```bat
REM Build deploy-agent-gui.exe as the primary GUI+service app, plus deploy-agent.exe as a compatible console service.
```

Replace:

```bat
echo Done: deploy-agent.exe deploy-agent-gui.exe
```

with:

```bat
echo Done: deploy-agent-gui.exe deploy-agent.exe
```

- [ ] **Step 5: Commit docs and build text**

Run:

```cmd
git add README.md build.bat
git commit -m "docs: describe unified gui service"
```

Expected: commit succeeds. If `build.bat` has pre-existing unrelated local edits, inspect `git diff build.bat`, stage only the lines from this task, and leave unrelated hunks unstaged.

## Task 9: Full Verification

**Files:**
- No new source changes unless verification exposes a defect.

- [ ] **Step 1: Format all Go code**

Run:

```cmd
gofmt -w .
```

Expected: command exits 0.

- [ ] **Step 2: Run all tests**

Run:

```cmd
go test ./...
```

Expected: PASS.

- [ ] **Step 3: Run vet**

Run:

```cmd
go vet ./...
```

Expected: PASS.

- [ ] **Step 4: Build Windows artifacts**

Run:

```cmd
build.bat
```

Expected: command exits 0 and prints:

```text
Done: deploy-agent-gui.exe deploy-agent.exe
```

- [ ] **Step 5: Manual Windows smoke test**

Run these checks on Windows:

```cmd
deploy-agent-gui.exe
curl http://127.0.0.1:8080/health
curl -u admin:change-me-please http://127.0.0.1:8080/scripts
```

Expected:

1. GUI starts after UAC.
2. The GUI local mode does not show service address, username, or password fields.
3. `/health` returns JSON with `status` equal to `ok`.
4. `/scripts` requires Basic Auth and returns the whitelist.
5. Closing the GUI window hides it and leaves `/health` working.
6. Tray “打开” restores the window.
7. Tray “退出” ends the process and releases port 8080.

- [ ] **Step 6: Confirm no unexpected verification edits remain**

Run:

```cmd
git status --short
```

Expected: no source changes from verification. Ignored build outputs such as `deploy-agent.exe`, `deploy-agent-gui.exe`, `resource.syso`, and local `config.yaml` should not appear in the status output.
