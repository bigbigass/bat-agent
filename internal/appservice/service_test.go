package appservice

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
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

func TestHTTPBaseURLForConfigNormalizesIPv6Hosts(t *testing.T) {
	tests := []string{"::1", "[::1]"}
	for _, host := range tests {
		t.Run(host, func(t *testing.T) {
			cfg := &config.Config{Server: config.ServerConfig{Host: host, Port: 8080}}
			got := HTTPBaseURLForConfig(cfg)
			if got != "http://[::1]:8080" {
				t.Fatalf("HTTPBaseURLForConfig(%q) = %q, want normalized IPv6 URL", host, got)
			}
		})
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

func TestServiceStartExposesHealthAndShutdownStopsHTTP(t *testing.T) {
	dir := t.TempDir()
	port := reserveHTTPListener(t)
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

func TestServiceWaitReturnsAfterShutdown(t *testing.T) {
	dir := t.TempDir()
	port := reserveHTTPListener(t)
	cfgPath := writeConfig(t, dir, port, true, false)

	svc := New(Options{ConfigPath: cfgPath})
	if err := svc.Start(context.Background()); err != nil {
		t.Fatalf("Start returned error: %v", err)
	}

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelShutdown()
	if err := svc.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown returned error: %v", err)
	}

	waitCtx, cancelWait := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelWait()
	waitDone := make(chan error, 1)
	go func() {
		waitDone <- svc.Wait(waitCtx)
	}()

	select {
	case err := <-waitDone:
		if err != nil {
			t.Fatalf("Wait returned error: %v", err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("Wait did not return after Shutdown")
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

func TestHTTPListenAddressNormalizesBracketedIPv6Host(t *testing.T) {
	cfg := &config.Config{Server: config.ServerConfig{Host: "[::1]", Port: 8080}}
	got := httpListenAddress(cfg)
	if got != "[::1]:8080" {
		t.Fatalf("httpListenAddress = %q, want normalized IPv6 listen address", got)
	}
}

func TestHTTPListenAddressPreservesWildcardHost(t *testing.T) {
	cfg := &config.Config{Server: config.ServerConfig{Host: "0.0.0.0", Port: 8080}}
	got := httpListenAddress(cfg)
	if got != "0.0.0.0:8080" {
		t.Fatalf("httpListenAddress = %q, want wildcard listen address", got)
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

func reserveHTTPListener(t *testing.T) int {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	addr := net.JoinHostPort("127.0.0.1", fmt.Sprint(port))

	originalListenTCP := listenTCP
	used := false
	listenTCP = func(network, address string) (net.Listener, error) {
		if network == "tcp" && address == addr && !used {
			used = true
			return ln, nil
		}
		return originalListenTCP(network, address)
	}
	t.Cleanup(func() {
		listenTCP = originalListenTCP
		if !used {
			_ = ln.Close()
		}
	})

	return port
}

func httpGet(url string) (int, error) {
	resp, err := http.Get(url)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	return resp.StatusCode, nil
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
