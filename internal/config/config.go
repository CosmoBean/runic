package config

import (
	"fmt"
	"os"
	"path/filepath"

	"go.yaml.in/yaml/v3"
)

type Config struct {
	Machine  MachineConfig  `yaml:"machine"`
	Server   ServerConfig   `yaml:"server"`
	Auth     AuthConfig     `yaml:"auth"`
	TLS      TLSConfig      `yaml:"tls"`
	Sessions SessionConfig  `yaml:"sessions"`
	Security SecurityConfig `yaml:"security"`
}

type MachineConfig struct {
	Name string `yaml:"name"`
}

type ServerConfig struct {
	Host string `yaml:"host"`
	Port int    `yaml:"port"`
}

type AuthConfig struct {
	TokenHash         string `yaml:"token_hash"`
	RequireToken      bool   `yaml:"require_token"`
	TokenRotationDays int    `yaml:"token_rotation_days"`
	RateLimit         int    `yaml:"rate_limit"`
	LockoutMin        int    `yaml:"lockout_minutes"`
}

type TLSConfig struct {
	Mode string `yaml:"mode"` // "self-signed", "custom", "none"
	Cert string `yaml:"cert"`
	Key  string `yaml:"key"`
}

type SessionConfig struct {
	SessionMode   string `yaml:"session_mode"` // "auto", "pty", "tmux"
	DefaultShell  string `yaml:"default_shell"`
	LoginShell    bool   `yaml:"login_shell"`
	StartDir      string `yaml:"start_dir"`
	MaxSessions   int    `yaml:"max_sessions"`
	PtyBufferSize int    `yaml:"pty_buffer_size"`
	WsWriteBuffer int    `yaml:"ws_write_buffer"`
	ThrottleMB    int    `yaml:"throttle_threshold_mb"`
}

type SecurityConfig struct {
	AllowedOrigins    []string `yaml:"allowed_origins"`
	TrustProxyHeaders bool     `yaml:"trust_proxy_headers"`
	TrustedProxyCIDRs []string `yaml:"trusted_proxy_cidrs"`
}

func DefaultConfig() *Config {
	return &Config{
		Machine: MachineConfig{Name: hostname()},
		Server:  ServerConfig{Host: "0.0.0.0", Port: 8765},
		Auth: AuthConfig{
			RequireToken:      true,
			TokenRotationDays: 30,
			RateLimit:         5,
			LockoutMin:        15,
		},
		TLS: TLSConfig{Mode: "self-signed"},
		Sessions: SessionConfig{
			SessionMode:   "auto",
			DefaultShell:  defaultShell(),
			LoginShell:    true,
			StartDir:      defaultStartDir(),
			MaxSessions:   20,
			PtyBufferSize: 16384,
			WsWriteBuffer: 64,
			ThrottleMB:    1,
		},
		Security: SecurityConfig{
			TrustProxyHeaders: false,
			TrustedProxyCIDRs: []string{"127.0.0.1/32", "::1/128"},
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

func defaultStartDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "/"
	}
	return home
}
