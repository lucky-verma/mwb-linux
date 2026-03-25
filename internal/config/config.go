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
	if cfg.Edge == "" {
		cfg.Edge = "left"
	}
	return &cfg, nil
}

func (c *Config) MessagePort() int {
	return c.Port + 1
}
