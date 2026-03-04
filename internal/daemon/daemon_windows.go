//go:build windows

package daemon

import (
	"errors"
	"time"
)

var ErrNotRunning = errors.New("daemon is not running")

type StartOpts struct {
	BinaryPath string
	ConfigPath string
	PIDPath    string
	LogPath    string
}

type StatusInfo struct {
	PID      int
	Running  bool
	PIDPath  string
	LogPath  string
	Detached bool
}

func StartDetached(opts StartOpts) error {
	return errors.New("detached daemon mode is not supported on windows; use `runic service`")
}

func Stop(pidPath string, timeout time.Duration) error {
	return errors.New("detached daemon mode is not supported on windows; use `runic service`")
}

func Status(pidPath, logPath string) (StatusInfo, error) {
	if pidPath == "" {
		pidPath = DefaultPIDPath()
	}
	if logPath == "" {
		logPath = DefaultLogPath()
	}
	return StatusInfo{
		PIDPath:  pidPath,
		LogPath:  logPath,
		Detached: false,
	}, nil
}
