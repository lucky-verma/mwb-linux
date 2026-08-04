// internal/config/config.go
package config

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

type Config struct {
	Host         string `toml:"host"`
	Key          string `toml:"key"`
	Name         string `toml:"name"`
	Port         int    `toml:"port"`
	RemoteWidth  int    `toml:"remote_width"`
	RemoteHeight int    `toml:"remote_height"`
	Edge         string `toml:"edge"`
	Clipboard    *bool  `toml:"clipboard"` // nil = unset, treated as enabled

	// FileTransfer controls the MWB file copy channel. nil = enabled.
	FileTransfer *bool `toml:"file_transfer"`

	// FileDir is retained only so older configs continue to decode. Clipboard
	// files now use private cache staging and appear in a chosen folder on paste;
	// writing every copied file into a visible directory was incorrect MWB UX.
	// Deprecated: ignored.
	FileDir string `toml:"file_dir"`

	// MaxFileSize caps a single transfer in bytes. 0 = MWB's own 100 MB limit,
	// which a stock Windows peer neither exceeds nor accepts.
	MaxFileSize int64 `toml:"max_file_size"`

	// KeyboardLayout controls inbound Windows->Linux keyboard mapping. "auto"
	// detects the local Linux layout when possible; unsupported layouts fall back
	// to the US-compatible mapping.
	KeyboardLayout string `toml:"keyboard_layout"`

	// AccelMultiplier scales raw evdev deltas before they move the remote cursor
	// (outbound, Linux->Windows). The Windows side adds no acceleration of its
	// own, so this is the only outbound speed knob. <= 0 means unset.
	AccelMultiplier float64 `toml:"accel_multiplier"`

	// InboundMultiplier scales Windows->Linux cursor movement (the inbound,
	// absolute-mirror direction). 1.0 mirrors Windows 1:1; raise it for a faster
	// local cursor when Windows is in control. <= 0 means unset (defaults to 1).
	InboundMultiplier float64 `toml:"inbound_multiplier"`

	// CaptureBackend selects bidirectional edge capture. "auto" keeps X11 on
	// X11 and uses the InputCapture portal in native Wayland sessions when that
	// backend was compiled in. "x11" and "portal" are explicit overrides.
	CaptureBackend string `toml:"capture_backend"`
}

func Load(path string) (*Config, error) {
	var cfg Config
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return nil, fmt.Errorf("decode config: %w", err)
	}
	if cfg.Host == "" {
		return nil, fmt.Errorf("config: host is required")
	}
	if cfg.Key == "" {
		return nil, fmt.Errorf("config: key is required")
	}
	if cfg.Port == 0 {
		cfg.Port = 15100
	}
	if cfg.Name == "" {
		hostname, _ := os.Hostname()
		if hostname == "" {
			hostname = "linux"
		}
		cfg.Name = hostname
	}
	if len(cfg.Name) > 15 {
		cfg.Name = cfg.Name[:15]
	}
	if cfg.RemoteWidth == 0 {
		cfg.RemoteWidth = 1920
	}
	if cfg.RemoteHeight == 0 {
		cfg.RemoteHeight = 1080
	}
	if cfg.AccelMultiplier <= 0 {
		cfg.AccelMultiplier = 2.0
	}
	if cfg.InboundMultiplier <= 0 {
		cfg.InboundMultiplier = 1.0
	}
	if cfg.KeyboardLayout == "" {
		cfg.KeyboardLayout = "auto"
	}
	if cfg.Edge == "" {
		cfg.Edge = "left"
	}
	if cfg.CaptureBackend == "" {
		cfg.CaptureBackend = "auto"
	}
	switch cfg.CaptureBackend {
	case "auto", "x11", "portal":
	default:
		return nil, fmt.Errorf("config: capture_backend must be auto, x11, or portal")
	}

	// The config holds the plaintext security key — the only secret protecting
	// input injection. Tighten permissions to owner-only (0600) so other local
	// accounts can't read it. This self-heals older installs whose file was
	// created world-readable (0644) without requiring a manual chmod.
	if err := secureConfigPermissions(path); err != nil {
		slog.Warn("could not tighten config file permissions", "path", path, "err", err)
	}
	if len(cfg.Key) < 12 {
		slog.Warn("security key is short; use a long, random key — it is the only secret protecting keyboard/mouse injection",
			"length", len(cfg.Key))
	}

	return &cfg, nil
}

// secureConfigPermissions restricts the config file to 0600 (owner read/write)
// if it is currently group- or world-accessible.
func secureConfigPermissions(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Mode().Perm()&0o077 == 0 {
		return nil // already 0600 or tighter
	}
	return os.Chmod(path, 0o600)
}

func (c *Config) MessagePort() int {
	return c.Port + 1
}

// ClipboardPort is where file and clipboard channels are opened. MWB runs two
// listeners and they are not the same one:
//
//	skMessageServer   = new TcpServer(TcpPort + 1, TCPServerThread);
//	skClipboardServer = new TcpServer(TcpPort,     AcceptConnectionAndSendClipboardData);
//
// Control is TcpPort+1, which MessagePort returns. Clipboard is the base port,
// and ConnectToRemoteClipboardSocket dials it. Sending a file channel to the
// control port instead reaches TCPServerThread, which answers with a control
// Handshake and never routes the connection to the file receiver.
func (c *Config) ClipboardPort() int {
	return c.Port
}

// ClipboardEnabled reports whether clipboard sharing should run. An absent
// clipboard key keeps it enabled, preserving the prior default behavior.
func (c *Config) ClipboardEnabled() bool {
	return c.Clipboard == nil || *c.Clipboard
}

// FileTransferEnabled reports whether the file copy channel should run.
// An absent setting is treated as enabled, matching clipboard.
func (c *Config) FileTransferEnabled() bool {
	return c.FileTransfer == nil || *c.FileTransfer
}

// FileDirectory resolves the legacy direct-download directory.
// Deprecated: clipboard file transfers no longer use this path.
func (c *Config) FileDirectory() string {
	if c.FileDir != "" {
		return os.ExpandEnv(c.FileDir)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "mwb")
	}
	return filepath.Join(home, "Downloads", "mwb")
}
