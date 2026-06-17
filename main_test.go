package main

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

func TestWaitForShutdownReturnsNilOnContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := waitForShutdown(ctx, make(chan error)); err != nil {
		t.Fatalf("waitForShutdown returned %v, want nil", err)
	}
}

func TestWaitForShutdownReturnsServiceError(t *testing.T) {
	want := errors.New("listen failed")
	errCh := make(chan error, 1)
	errCh <- want

	if got := waitForShutdown(context.Background(), errCh); !errors.Is(got, want) {
		t.Fatalf("waitForShutdown returned %v, want %v", got, want)
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
