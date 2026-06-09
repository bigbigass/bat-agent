package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server ServerConfig `yaml:"server"`
	Auth   AuthConfig   `yaml:"auth"`
	Runner RunnerConfig `yaml:"runner"`
}

type ServerConfig struct {
	Host string `yaml:"host"`
	Port int    `yaml:"port"`
}

type AuthConfig struct {
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

type RunnerConfig struct {
	TimeoutSeconds int    `yaml:"timeoutSeconds"`
	ScriptDir      string `yaml:"scriptDir"`
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
		Runner: RunnerConfig{TimeoutSeconds: 300},
	}
}

func (c *Config) validate() error {
	if c.Server.Port <= 0 || c.Server.Port > 65535 {
		return fmt.Errorf("server.port %d out of range", c.Server.Port)
	}
	if c.Auth.Username == "" {
		return fmt.Errorf("auth.username is empty")
	}
	if len(c.Auth.Password) < 8 {
		return fmt.Errorf("auth.password must be at least 8 characters")
	}
	if c.Runner.TimeoutSeconds <= 0 {
		return fmt.Errorf("runner.timeoutSeconds must be > 0")
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
