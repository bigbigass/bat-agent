package main

import (
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
