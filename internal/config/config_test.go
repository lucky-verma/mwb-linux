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
	if cfg.AccelMultiplier != 2.0 {
		t.Errorf("default accel_multiplier = %v, want 2.0", cfg.AccelMultiplier)
	}
	if cfg.InboundMultiplier != 1.0 {
		t.Errorf("default inbound_multiplier = %v, want 1.0", cfg.InboundMultiplier)
	}
	if cfg.KeyboardLayout != "auto" {
		t.Errorf("default keyboard_layout = %q, want auto", cfg.KeyboardLayout)
	}
}

func TestAccelMultiplierExplicit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(`
host = "10.0.0.1"
key = "SomeKeyHere!1234"
accel_multiplier = 0.5
`), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AccelMultiplier != 0.5 {
		t.Errorf("accel_multiplier = %v, want 0.5", cfg.AccelMultiplier)
	}
}

func TestKeyboardLayoutExplicit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(`
host = "10.0.0.1"
key = "SomeKeyHere!1234"
keyboard_layout = "de"
`), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.KeyboardLayout != "de" {
		t.Errorf("keyboard_layout = %q, want de", cfg.KeyboardLayout)
	}
}

func TestClipboardEnabledDefault(t *testing.T) {
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
	if !cfg.ClipboardEnabled() {
		t.Error("clipboard should default to enabled when the key is absent")
	}
}

func TestClipboardDisabled(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(`
host = "10.0.0.1"
key = "SomeKeyHere!1234"
clipboard = false
`), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ClipboardEnabled() {
		t.Error("clipboard = false should disable clipboard sharing")
	}
}

func TestClipboardEnabledExplicit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(`
host = "10.0.0.1"
key = "SomeKeyHere!1234"
clipboard = true
`), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.ClipboardEnabled() {
		t.Error("clipboard = true should enable clipboard sharing")
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
