package daemon

import (
	"os"
	"path/filepath"
)

func DefaultPIDPath() string {
	return filepath.Join(configDir(), "runic.pid")
}

func DefaultLogPath() string {
	return filepath.Join(configDir(), "logs", "runic-daemon.log")
}

func configDir() string {
	cfgDir, err := os.UserConfigDir()
	if err != nil || cfgDir == "" {
		return "."
	}
	return filepath.Join(cfgDir, "runic")
}
