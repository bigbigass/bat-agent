package config

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server   ServerConfig   `yaml:"server"`
	Services ServicesConfig `yaml:"services"`
	Auth     AuthConfig     `yaml:"auth"`
	Runner   RunnerConfig   `yaml:"runner"`
	PreRun   PreRunConfig   `yaml:"preRun"`
	Release  ReleaseConfig  `yaml:"release"`
}

type ServerConfig struct {
	Host string `yaml:"host"`
	Port int    `yaml:"port"`
}

type ServicesConfig struct {
	HTTP HTTPServiceConfig `yaml:"http"`
	MQTT MQTTServiceConfig `yaml:"mqtt"`
}

type HTTPServiceConfig struct {
	Enabled bool `yaml:"enabled"`
}

type MQTTServiceConfig struct {
	Enabled      bool   `yaml:"enabled"`
	Broker       string `yaml:"broker"`
	ClientID     string `yaml:"clientId"`
	Username     string `yaml:"username"`
	Password     string `yaml:"password"`
	CommandTopic string `yaml:"commandTopic"`
	QoS          int    `yaml:"qos"`
}

type AuthConfig struct {
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

type RunnerConfig struct {
	TimeoutSeconds int    `yaml:"timeoutSeconds"`
	ScriptDir      string `yaml:"scriptDir"`
}

type PreRunConfig struct {
	Download PreRunDownloadConfig `yaml:"download"`
}

type PreRunDownloadConfig struct {
	Script         string `yaml:"script"`
	TimeoutSeconds int    `yaml:"timeoutSeconds"`
}

type ReleaseConfig struct {
	Enabled        bool   `yaml:"enabled"`
	ManifestURL    string `yaml:"manifestURL"`
	DownloadDir    string `yaml:"downloadDir"`
	RefreshSeconds int    `yaml:"refreshSeconds"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	cfg := defaults()
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func defaults() *Config {
	return &Config{
		Server: ServerConfig{Host: "0.0.0.0", Port: 8080},
		Services: ServicesConfig{
			HTTP: HTTPServiceConfig{Enabled: true},
			MQTT: MQTTServiceConfig{
				Broker:       "tcp://127.0.0.1:1883",
				ClientID:     "deploy-agent",
				CommandTopic: "deploy-agent/run",
				QoS:          1,
			},
		},
		Runner: RunnerConfig{TimeoutSeconds: 300},
		PreRun: PreRunConfig{
			Download: PreRunDownloadConfig{TimeoutSeconds: 300},
		},
		Release: ReleaseConfig{DownloadDir: filepath.Join("tools", "download"), RefreshSeconds: 300},
	}
}

func (c *Config) validate() error {
	if c.Server.Port <= 0 || c.Server.Port > 65535 {
		return fmt.Errorf("server.port %d out of range", c.Server.Port)
	}
	if !c.Services.HTTP.Enabled && !c.Services.MQTT.Enabled {
		return fmt.Errorf("at least one service must be enabled")
	}
	if c.Services.HTTP.Enabled {
		if c.Auth.Username == "" {
			return fmt.Errorf("auth.username is empty")
		}
		if len(c.Auth.Password) < 8 {
			return fmt.Errorf("auth.password must be at least 8 characters")
		}
	}
	if c.Services.MQTT.Enabled {
		if c.Services.MQTT.Broker == "" {
			return fmt.Errorf("services.mqtt.broker is empty")
		}
		if c.Services.MQTT.CommandTopic == "" {
			return fmt.Errorf("services.mqtt.commandTopic is empty")
		}
		if c.Services.MQTT.QoS < 0 || c.Services.MQTT.QoS > 2 {
			return fmt.Errorf("services.mqtt.qos must be 0, 1, or 2")
		}
	}
	if c.Runner.TimeoutSeconds <= 0 {
		return fmt.Errorf("runner.timeoutSeconds must be > 0")
	}
	if c.PreRun.Download.TimeoutSeconds <= 0 {
		return fmt.Errorf("preRun.download.timeoutSeconds must be > 0")
	}
	if c.Release.Enabled && strings.TrimSpace(c.Release.ManifestURL) != "" {
		u, err := url.Parse(strings.TrimSpace(c.Release.ManifestURL))
		if err != nil || !strings.EqualFold(u.Scheme, "https") || u.Host == "" {
			return fmt.Errorf("release.manifestURL must be a valid HTTPS URL")
		}
		if c.Release.RefreshSeconds <= 0 {
			return fmt.Errorf("release.refreshSeconds must be > 0")
		}
	}
	return nil
}

// ResolveScriptDir returns the absolute script directory.
// If ScriptDir is empty, use the executable's own directory.
func (c *Config) ResolveScriptDir() (string, error) {
	if c.Runner.ScriptDir != "" {
		return filepath.Abs(c.Runner.ScriptDir)
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.Dir(exe), nil
}

func (c *Config) ResolvePreRunDownloadScript(configPath string) (string, error) {
	script := strings.TrimSpace(c.PreRun.Download.Script)
	if script == "" {
		return "", nil
	}
	if filepath.IsAbs(script) {
		return filepath.Clean(script), nil
	}
	base := filepath.Dir(configPath)
	return filepath.Abs(filepath.Join(base, script))
}

// ResolveReleaseDownloadDir returns the release download directory, resolved
// relative to the config file directory when configured as a relative path.
func (c *Config) ResolveReleaseDownloadDir(configPath string) (string, error) {
	dir := strings.TrimSpace(c.Release.DownloadDir)
	if dir == "" {
		dir = filepath.Join("tools", "download")
	}
	if filepath.IsAbs(dir) {
		return filepath.Clean(dir), nil
	}
	return filepath.Abs(filepath.Join(filepath.Dir(configPath), dir))
}
