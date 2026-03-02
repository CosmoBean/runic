package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Machine  MachineConfig `yaml:"machine"`
	Server   ServerConfig  `yaml:"server"`
	Auth     AuthConfig    `yaml:"auth"`
	TLS      TLSConfig     `yaml:"tls"`
	Sessions SessionConfig `yaml:"sessions"`
}

type MachineConfig struct {
	Name string `yaml:"name"`
}

type ServerConfig struct {
	Host string `yaml:"host"`
	Port int    `yaml:"port"`
}

type AuthConfig struct {
	TokenHash  string `yaml:"token_hash"`
	RateLimit  int    `yaml:"rate_limit"`
	LockoutMin int    `yaml:"lockout_minutes"`
}

type TLSConfig struct {
	Mode string `yaml:"mode"` // "self-signed", "custom", "none"
	Cert string `yaml:"cert"`
	Key  string `yaml:"key"`
}

type SessionConfig struct {
	DefaultShell  string `yaml:"default_shell"`
	MaxSessions   int    `yaml:"max_sessions"`
	PtyBufferSize int    `yaml:"pty_buffer_size"`
	WsWriteBuffer int    `yaml:"ws_write_buffer"`
	ThrottleMB    int    `yaml:"throttle_threshold_mb"`
}

func DefaultConfig() *Config {
	return &Config{
		Machine: MachineConfig{Name: hostname()},
		Server:  ServerConfig{Host: "0.0.0.0", Port: 8765},
		Auth:    AuthConfig{RateLimit: 5, LockoutMin: 15},
		TLS:     TLSConfig{Mode: "self-signed"},
		Sessions: SessionConfig{
			DefaultShell:  defaultShell(),
			MaxSessions:   20,
			PtyBufferSize: 16384,
			WsWriteBuffer: 64,
			ThrottleMB:    1,
		},
	}
}

func Load(path string) (*Config, error) {
	cfg := DefaultConfig()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil // Use defaults if no config file
		}
		return nil, fmt.Errorf("reading config: %w", err)
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	return cfg, nil
}

func ConfigDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "runic")
}

func ConfigPath() string {
	return filepath.Join(ConfigDir(), "config.yaml")
}

func hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	return h
}

func defaultShell() string {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	return shell
}
