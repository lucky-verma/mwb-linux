// internal/config/config.go
package config

import (
	"fmt"
	"os"

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

	// AccelMultiplier scales raw evdev deltas before they move the remote cursor
	// (outbound, Linux->Windows). The Windows side adds no acceleration of its
	// own, so this is the only outbound speed knob. <= 0 means unset.
	AccelMultiplier float64 `toml:"accel_multiplier"`

	// InboundMultiplier scales Windows->Linux cursor movement (the inbound,
	// absolute-mirror direction). 1.0 mirrors Windows 1:1; raise it for a faster
	// local cursor when Windows is in control. <= 0 means unset (defaults to 1).
	InboundMultiplier float64 `toml:"inbound_multiplier"`
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
	if cfg.Edge == "" {
		cfg.Edge = "left"
	}
	return &cfg, nil
}

func (c *Config) MessagePort() int {
	return c.Port + 1
}

// ClipboardEnabled reports whether clipboard sharing should run. An absent
// clipboard key keeps it enabled, preserving the prior default behavior.
func (c *Config) ClipboardEnabled() bool {
	return c.Clipboard == nil || *c.Clipboard
}
