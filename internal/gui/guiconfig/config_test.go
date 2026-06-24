package guiconfig

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultConfig(t *testing.T) {
	cfg := Default()

	if cfg.Mode != ModeLocal {
		t.Fatalf("Mode = %q, want %q", cfg.Mode, ModeLocal)
	}
	if cfg.BaseURL != "" {
		t.Fatalf("BaseURL = %q, want empty local default", cfg.BaseURL)
	}
	if cfg.Username != "" {
		t.Fatalf("Username = %q, want empty local default", cfg.Username)
	}
	if cfg.Password != "" {
		t.Fatalf("Password = %q, want empty", cfg.Password)
	}
}

func TestForSaveClearsLocalConnectionCredentials(t *testing.T) {
	cfg := Config{
		Mode:     ModeLocal,
		BaseURL:  "http://old-local:8080",
		Username: "admin",
		Password: "change-me-please",
	}

	got := ForSave(cfg)
	if got.Mode != ModeLocal {
		t.Fatalf("Mode = %q, want local", got.Mode)
	}
	if got.BaseURL != "" {
		t.Fatalf("BaseURL = %q, want empty", got.BaseURL)
	}
	if got.Username != "" {
		t.Fatalf("Username = %q, want empty", got.Username)
	}
	if got.Password != "" {
		t.Fatalf("Password = %q, want empty", got.Password)
	}
}

func TestForSaveKeepsRemoteConnectionCredentials(t *testing.T) {
	cfg := Config{
		Mode:     ModeRemote,
		BaseURL:  "http://10.0.0.5:8080",
		Username: "alice",
		Password: "secret-password",
	}

	got := ForSave(cfg)
	if got != cfg {
		t.Fatalf("ForSave remote = %#v, want %#v", got, cfg)
	}
}

func TestSaveAndLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	want := Config{
		Mode:     ModeRemote,
		BaseURL:  "http://10.0.0.5:8080",
		Username: "alice",
		Password: "secret-password",
	}

	if err := Save(path, want); err != nil {
		t.Fatalf("Save returned error: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if got != want {
		t.Fatalf("Load = %#v, want %#v", got, want)
	}
}

func TestLoadClearsLegacyLocalConnectionCredentials(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	data := []byte(`{"mode":"local","baseUrl":"http://old","username":"admin","password":"secret"}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if got.Mode != ModeLocal {
		t.Fatalf("Mode = %q, want local", got.Mode)
	}
	if got.BaseURL != "" {
		t.Fatalf("BaseURL = %q, want empty", got.BaseURL)
	}
	if got.Username != "" {
		t.Fatalf("Username = %q, want empty", got.Username)
	}
	if got.Password != "" {
		t.Fatalf("Password = %q, want empty", got.Password)
	}
}

func TestLoadMissingFileReturnsDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.json")

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if got != Default() {
		t.Fatalf("Load missing = %#v, want default %#v", got, Default())
	}
}

func TestPathUsesUserConfigDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("APPDATA", dir)

	got, err := Path()
	if err != nil {
		t.Fatalf("Path returned error: %v", err)
	}
	want := filepath.Join(dir, "deploy-agent-gui", "config.json")
	if got != want {
		t.Fatalf("Path = %q, want %q", got, want)
	}
}

func TestSaveCreatesParentDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.json")

	if err := Save(path, Default()); err != nil {
		t.Fatalf("Save returned error: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected config file to exist: %v", err)
	}
}
