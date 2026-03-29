// internal/config/config_test.go
package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(`
host = "192.168.1.50"
key = "MySecurityKey1234"
name = "testbox"
port = 15100
`), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Host != "192.168.1.50" {
		t.Errorf("host = %q, want %q", cfg.Host, "192.168.1.50")
	}
	if cfg.Key != "MySecurityKey1234" {
		t.Errorf("key = %q", cfg.Key)
	}
	if cfg.Name != "testbox" {
		t.Errorf("name = %q", cfg.Name)
	}
	if cfg.Port != 15100 {
		t.Errorf("port = %d, want 15100", cfg.Port)
	}
}

func TestLoadConfigDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(`
host = "10.0.0.1"
key = "SomeKeyHere!1234"
`), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != 15100 {
		t.Errorf("default port = %d, want 15100", cfg.Port)
	}
	if cfg.Name == "" {
		t.Error("name should default to hostname")
	}
}

func TestLoadConfigValidation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	// Missing host
	if err := os.WriteFile(path, []byte(`key = "SomeKeyHere!1234"`), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Error("should fail without host")
	}

	// Missing key
	if err := os.WriteFile(path, []byte(`host = "10.0.0.1"`), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Error("should fail without key")
	}
}
