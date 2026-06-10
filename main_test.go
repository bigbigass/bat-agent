package main

import (
	"context"
	"errors"
	"testing"

	"github.com/liqixin/deploy-agent/internal/config"
	"github.com/liqixin/deploy-agent/internal/mqttapi"
)

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
