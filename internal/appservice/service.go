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
