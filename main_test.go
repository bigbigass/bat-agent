package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/liqixin/deploy-agent/internal/config"
	"github.com/liqixin/deploy-agent/internal/mqttapi"
)

func TestExecutorOptionsFromConfigMapsPreRunDownload(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
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
