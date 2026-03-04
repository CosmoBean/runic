//go:build !windows

package daemon

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
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
	if opts.BinaryPath == "" {
		return errors.New("binary path is required")
	}
	if opts.ConfigPath == "" {
		return errors.New("config path is required")
	}
	if opts.PIDPath == "" {
		opts.PIDPath = DefaultPIDPath()
	}
	if opts.LogPath == "" {
		opts.LogPath = DefaultLogPath()
	}

	status, err := Status(opts.PIDPath, opts.LogPath)
	if err != nil {
		return err
	}
	if status.Running {
		return fmt.Errorf("runic daemon is already running (pid %d)", status.PID)
	}

	if err := os.MkdirAll(filepath.Dir(opts.PIDPath), 0700); err != nil {
		return fmt.Errorf("create pid dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(opts.LogPath), 0700); err != nil {
		return fmt.Errorf("create log dir: %w", err)
	}

	logf, err := os.OpenFile(opts.LogPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("open daemon log: %w", err)
	}
	defer logf.Close()

	cmd := exec.Command(opts.BinaryPath, "start", "--config", opts.ConfigPath)
	cmd.Stdin = nil
	cmd.Stdout = logf
	cmd.Stderr = logf
	cmd.Env = os.Environ()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start detached daemon: %w", err)
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Release()

	if err := writePID(opts.PIDPath, pid); err != nil {
		return err
	}

	// Detect immediate startup failure (e.g., bind error).
	time.Sleep(250 * time.Millisecond)
	if !isRunning(pid) {
		_ = os.Remove(opts.PIDPath)
		return fmt.Errorf("daemon failed to start; check log %s", opts.LogPath)
	}
	return nil
}

func Stop(pidPath string, timeout time.Duration) error {
	if pidPath == "" {
		pidPath = DefaultPIDPath()
	}
	pid, err := readPID(pidPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ErrNotRunning
		}
		return err
	}
	if pid <= 0 || !isRunning(pid) {
		_ = os.Remove(pidPath)
		return ErrNotRunning
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("find daemon pid %d: %w", pid, err)
	}
	if err := proc.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("stop daemon pid %d: %w", pid, err)
	}

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !isRunning(pid) {
			_ = os.Remove(pidPath)
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}

	_ = proc.Signal(syscall.SIGKILL)
	for i := 0; i < 20; i++ {
		if !isRunning(pid) {
			_ = os.Remove(pidPath)
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("daemon pid %d did not stop within %s", pid, timeout)
}

func Status(pidPath, logPath string) (StatusInfo, error) {
	if pidPath == "" {
		pidPath = DefaultPIDPath()
	}
	if logPath == "" {
		logPath = DefaultLogPath()
	}
	status := StatusInfo{
		PIDPath:  pidPath,
		LogPath:  logPath,
		Detached: true,
	}

	pid, err := readPID(pidPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return status, nil
		}
		return status, err
	}
	status.PID = pid
	status.Running = pid > 0 && isRunning(pid)
	if !status.Running {
		_ = os.Remove(pidPath)
	}
	return status, nil
}

func writePID(pidPath string, pid int) error {
	tmp := pidPath + ".tmp"
	if err := os.WriteFile(tmp, []byte(strconv.Itoa(pid)+"\n"), 0600); err != nil {
		return fmt.Errorf("write pid file: %w", err)
	}
	if err := os.Rename(tmp, pidPath); err != nil {
		return fmt.Errorf("install pid file: %w", err)
	}
	return nil
}

func readPID(pidPath string) (int, error) {
	b, err := os.ReadFile(pidPath)
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return 0, fmt.Errorf("parse pid file %s: %w", pidPath, err)
	}
	return pid, nil
}

func isRunning(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
