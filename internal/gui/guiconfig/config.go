package guiconfig

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

const (
	ModeLocal  = "local"
	ModeRemote = "remote"
)

type Config struct {
	Mode     string `json:"mode"`
	BaseURL  string `json:"baseUrl"`
	Username string `json:"username"`
	Password string `json:"password"`
}

func Default() Config {
	return Config{
		Mode: ModeLocal,
	}
}

func Path() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "deploy-agent-gui", "config.json"), nil
}

func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Default(), nil
	}
	if err != nil {
		return Config{}, err
	}

	cfg := Default()
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, err
	}
	if cfg.Mode == "" {
		cfg.Mode = ModeLocal
	}
	if cfg.Mode == ModeLocal {
		cfg = ForSave(cfg)
	}
	if cfg.Mode == ModeRemote && cfg.BaseURL == "" {
		cfg.BaseURL = "http://127.0.0.1:8080"
	}
	return cfg, nil
}

func ForSave(cfg Config) Config {
	if cfg.Mode != ModeLocal {
		return cfg
	}
	cfg.BaseURL = ""
	cfg.Username = ""
	cfg.Password = ""
	return cfg
}

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
