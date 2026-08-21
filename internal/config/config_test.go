package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadDefaultsServicesToHTTPEnabledMQTTDisabled(t *testing.T) {
	path := writeConfig(t, `
auth:
  username: admin
  password: change-me-please
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if !cfg.Services.HTTP.Enabled {
		t.Fatalf("HTTP should default to enabled")
	}
	if cfg.Services.MQTT.Enabled {
		t.Fatalf("MQTT should default to disabled")
	}
	if cfg.Services.MQTT.Broker != "tcp://127.0.0.1:1883" {
		t.Fatalf("unexpected broker default: %q", cfg.Services.MQTT.Broker)
	}
	if cfg.Services.MQTT.ClientID != "deploy-agent" {
		t.Fatalf("unexpected clientId default: %q", cfg.Services.MQTT.ClientID)
	}
	if cfg.Services.MQTT.Username != "" {
		t.Fatalf("unexpected username default: %q", cfg.Services.MQTT.Username)
	}
	if cfg.Services.MQTT.Password != "" {
		t.Fatalf("unexpected password default: %q", cfg.Services.MQTT.Password)
	}
	if cfg.Services.MQTT.CommandTopic != "deploy-agent/run" {
		t.Fatalf("unexpected commandTopic default: %q", cfg.Services.MQTT.CommandTopic)
	}
	if cfg.Services.MQTT.QoS != 1 {
		t.Fatalf("unexpected qos default: %d", cfg.Services.MQTT.QoS)
	}
}

func TestLoadRejectsWhenHTTPAndMQTTAreDisabled(t *testing.T) {
	path := writeConfig(t, `
services:
  http:
    enabled: false
  mqtt:
    enabled: false
auth:
  username: admin
  password: change-me-please
`)

	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "at least one service must be enabled") {
		t.Fatalf("expected service validation error, got %v", err)
	}
}

func TestLoadAllowsMQTTOnlyWithoutHTTPAuth(t *testing.T) {
	path := writeConfig(t, `
services:
  http:
    enabled: false
  mqtt:
    enabled: true
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.Services.HTTP.Enabled {
		t.Fatalf("HTTP should be disabled")
	}
	if !cfg.Services.MQTT.Enabled {
		t.Fatalf("MQTT should be enabled")
	}
}

func TestLoadRejectsEnabledMQTTWithoutCommandTopic(t *testing.T) {
	path := writeConfig(t, `
services:
  mqtt:
    enabled: true
    commandTopic: ""
auth:
  username: admin
  password: change-me-please
`)

	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "services.mqtt.commandTopic is empty") {
		t.Fatalf("expected commandTopic validation error, got %v", err)
	}
}

func TestLoadRejectsInvalidMQTTQoS(t *testing.T) {
	path := writeConfig(t, `
services:
  mqtt:
    enabled: true
    qos: 3
auth:
  username: admin
  password: change-me-please
`)

	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "services.mqtt.qos must be 0, 1, or 2") {
		t.Fatalf("expected qos validation error, got %v", err)
	}
}

func TestLoadAcceptsMQTTTLSBroker(t *testing.T) {
	path := writeConfig(t, `
services:
  mqtt:
    enabled: true
    broker: "ssl://broker.example.com:8883"
auth:
  username: admin
  password: change-me-please
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.Services.MQTT.Broker != "ssl://broker.example.com:8883" {
		t.Fatalf("unexpected broker: %q", cfg.Services.MQTT.Broker)
	}
}

func TestLoadDefaultsPreRunDownloadTimeout(t *testing.T) {
	path := writeConfig(t, `
auth:
  username: admin
  password: change-me-please
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.PreRun.Download.Script != "" {
		t.Fatalf("preRun.download.script = %q, want empty", cfg.PreRun.Download.Script)
	}
	if cfg.PreRun.Download.TimeoutSeconds != 300 {
		t.Fatalf("preRun.download.timeoutSeconds = %d, want 300", cfg.PreRun.Download.TimeoutSeconds)
	}
}

func TestLoadParsesPreRunDownload(t *testing.T) {
	path := writeConfig(t, `
auth:
  username: admin
  password: change-me-please
preRun:
  download:
    script: tools/download_simple.bat
    timeoutSeconds: 120
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.PreRun.Download.Script != "tools/download_simple.bat" {
		t.Fatalf("script = %q, want configured script", cfg.PreRun.Download.Script)
	}
	if cfg.PreRun.Download.TimeoutSeconds != 120 {
		t.Fatalf("timeoutSeconds = %d, want 120", cfg.PreRun.Download.TimeoutSeconds)
	}
}

func TestLoadRejectsInvalidPreRunDownloadTimeout(t *testing.T) {
	path := writeConfig(t, `
auth:
  username: admin
  password: change-me-please
preRun:
  download:
    timeoutSeconds: 0
`)

	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "preRun.download.timeoutSeconds must be > 0") {
		t.Fatalf("expected preRun download timeout validation error, got %v", err)
	}
}

func TestResolvePreRunDownloadScriptUsesConfigDirectory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	cfg := &Config{
		PreRun: PreRunConfig{
			Download: PreRunDownloadConfig{
				Script: "tools/download_simple.bat",
			},
		},
	}

	got, err := cfg.ResolvePreRunDownloadScript(path)
	if err != nil {
		t.Fatalf("ResolvePreRunDownloadScript returned error: %v", err)
	}
	want := filepath.Join(dir, "tools", "download_simple.bat")
	if got != want {
		t.Fatalf("path = %q, want %q", got, want)
	}
}

func TestLoadDefaultsReleaseDownloadSettings(t *testing.T) {
	path := writeConfig(t, `
auth:
  username: admin
  password: change-me-please
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Release.Enabled {
		t.Fatal("release should default to disabled")
	}
	if cfg.Release.DownloadDir != filepath.Join("tools", "download") || cfg.Release.RefreshSeconds != 300 {
		t.Fatalf("unexpected release defaults: %#v", cfg.Release)
	}
}

func TestLoadParsesReleaseSettings(t *testing.T) {
	path := writeConfig(t, `
auth:
  username: admin
  password: change-me-please
release:
  enabled: true
  manifestURL: https://release.example.com/deploy-agent/manifest.json
  downloadDir: artifacts
  refreshSeconds: 60
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Release.Enabled || cfg.Release.ManifestURL == "" || cfg.Release.DownloadDir != "artifacts" || cfg.Release.RefreshSeconds != 60 {
		t.Fatalf("unexpected release config: %#v", cfg.Release)
	}
}

func TestLoadTreatsEmptyReleaseManifestURLAsDisabled(t *testing.T) {
	path := writeConfig(t, `
auth:
  username: admin
  password: change-me-please
release:
  enabled: true
  manifestURL: ""
`)
	if _, err := Load(path); err != nil {
		t.Fatalf("empty manifest URL should leave the optional feature unavailable: %v", err)
	}
}

func TestLoadRejectsInvalidReleaseSettings(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{"non HTTPS URL", "manifestURL: http://release.example.com/manifest.json\n  refreshSeconds: 60", "valid HTTPS URL"},
		{"zero refresh", "manifestURL: https://release.example.com/manifest.json\n  refreshSeconds: 0", "refreshSeconds must be > 0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writeConfig(t, "auth:\n  username: admin\n  password: change-me-please\nrelease:\n  enabled: true\n  "+tc.body+"\n")
			_, err := Load(path)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Load error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestResolveReleaseDownloadDirUsesConfigDirectory(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{Release: ReleaseConfig{DownloadDir: filepath.Join("tools", "download")}}
	got, err := cfg.ResolveReleaseDownloadDir(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "tools", "download")
	if got != want {
		t.Fatalf("download dir = %q, want %q", got, want)
	}
}
