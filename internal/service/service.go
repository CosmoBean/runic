package service

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

const (
	launchdLabel = "com.runic.daemon"
	systemdUnit  = "runic.service"
)

func Install(binaryPath, configPath string) error {
	switch runtime.GOOS {
	case "darwin":
		return installLaunchd(binaryPath, configPath)
	case "linux":
		return installSystemdUser(binaryPath, configPath)
	default:
		return fmt.Errorf("service install unsupported on %s", runtime.GOOS)
	}
}

func Uninstall() error {
	switch runtime.GOOS {
	case "darwin":
		return uninstallLaunchd()
	case "linux":
		return uninstallSystemdUser()
	default:
		return fmt.Errorf("service uninstall unsupported on %s", runtime.GOOS)
	}
}

func Start() error {
	switch runtime.GOOS {
	case "darwin":
		return startLaunchd()
	case "linux":
		return run("systemctl", "--user", "start", systemdUnit)
	default:
		return fmt.Errorf("service start unsupported on %s", runtime.GOOS)
	}
}

func Stop() error {
	switch runtime.GOOS {
	case "darwin":
		return stopLaunchd()
	case "linux":
		return run("systemctl", "--user", "stop", systemdUnit)
	default:
		return fmt.Errorf("service stop unsupported on %s", runtime.GOOS)
	}
}

func Restart() error {
	switch runtime.GOOS {
	case "darwin":
		if err := stopLaunchd(); err != nil {
			return err
		}
		return startLaunchd()
	case "linux":
		return run("systemctl", "--user", "restart", systemdUnit)
	default:
		return fmt.Errorf("service restart unsupported on %s", runtime.GOOS)
	}
}

func Status() (string, error) {
	switch runtime.GOOS {
	case "darwin":
		return launchdStatus()
	case "linux":
		out, err := runOutput("systemctl", "--user", "status", "--no-pager", systemdUnit)
		if err != nil {
			lout := strings.ToLower(out)
			if strings.Contains(lout, "could not be found") || strings.Contains(lout, "not found") || strings.Contains(lout, "loaded: not-found") {
				return "runic service is not installed", nil
			}
		}
		return out, err
	default:
		return "", fmt.Errorf("service status unsupported on %s", runtime.GOOS)
	}
}

func installLaunchd(binaryPath, configPath string) error {
	serviceBinary, err := materializeServiceBinary(binaryPath)
	if err != nil {
		return err
	}

	plistPath, err := launchdPlistPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(plistPath), 0755); err != nil {
		return fmt.Errorf("create launchd dir: %w", err)
	}

	logDir, err := runicLogDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(logDir, 0700); err != nil {
		return fmt.Errorf("create log dir: %w", err)
	}

	stdoutPath := filepath.Join(logDir, "runic-service.log")
	stderrPath := filepath.Join(logDir, "runic-service.err.log")

	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>%s</string>
  <key>ProgramArguments</key>
  <array>
    <string>%s</string>
    <string>start</string>
    <string>--config</string>
    <string>%s</string>
  </array>
  <key>KeepAlive</key>
  <true/>
  <key>RunAtLoad</key>
  <true/>
  <key>StandardOutPath</key>
  <string>%s</string>
  <key>StandardErrorPath</key>
  <string>%s</string>
</dict>
</plist>
`, launchdLabel, xmlEscape(serviceBinary), xmlEscape(configPath), xmlEscape(stdoutPath), xmlEscape(stderrPath))

	if err := os.WriteFile(plistPath, []byte(plist), 0644); err != nil {
		return fmt.Errorf("write launchd plist: %w", err)
	}

	domain, err := launchdDomain()
	if err != nil {
		return err
	}
	_ = run("launchctl", "bootout", domain, plistPath)
	_ = run("launchctl", "enable", domain+"/"+launchdLabel)
	if err := run("launchctl", "bootstrap", domain, plistPath); err != nil {
		// launchd keeps disable state across reinstall; force-enable then retry once.
		if isLaunchdDisabled(err.Error()) {
			_ = run("launchctl", "enable", domain+"/"+launchdLabel)
			if retryErr := run("launchctl", "bootstrap", domain, plistPath); retryErr != nil {
				return retryErr
			}
		} else {
			return err
		}
	}
	return run("launchctl", "kickstart", "-k", domain+"/"+launchdLabel)
}

func uninstallLaunchd() error {
	plistPath, err := launchdPlistPath()
	if err != nil {
		return err
	}
	domain, err := launchdDomain()
	if err != nil {
		return err
	}
	_ = run("launchctl", "disable", domain+"/"+launchdLabel)
	_ = run("launchctl", "bootout", domain, plistPath)
	if err := os.Remove(plistPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove plist: %w", err)
	}
	return nil
}

func startLaunchd() error {
	domain, err := launchdDomain()
	if err != nil {
		return err
	}
	plistPath, err := launchdPlistPath()
	if err != nil {
		return err
	}
	if _, err := os.Stat(plistPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("service is not installed; run `runic service install` first")
		}
		return err
	}

	_ = run("launchctl", "enable", domain+"/"+launchdLabel)
	if err := run("launchctl", "bootstrap", domain, plistPath); err != nil {
		lower := strings.ToLower(err.Error())
		if !strings.Contains(lower, "service already loaded") && !strings.Contains(lower, "already loaded") {
			// If already bootstrapped this can fail; ignore only that case.
			return err
		}
	}
	return run("launchctl", "kickstart", "-k", domain+"/"+launchdLabel)
}

func stopLaunchd() error {
	domain, err := launchdDomain()
	if err != nil {
		return err
	}
	plistPath, err := launchdPlistPath()
	if err != nil {
		return err
	}
	_ = run("launchctl", "disable", domain+"/"+launchdLabel)
	if err := run("launchctl", "bootout", domain, plistPath); err != nil {
		low := strings.ToLower(err.Error())
		if strings.Contains(low, "could not find service") || strings.Contains(low, "not loaded") {
			return nil
		}
		return err
	}
	return nil
}

func launchdStatus() (string, error) {
	out, err := runOutput("launchctl", "print", "gui/"+currentUIDString()+"/"+launchdLabel)
	if err == nil {
		return out, nil
	}
	if isLaunchdDisabled(out) || isLaunchdDisabled(err.Error()) {
		if launchdIsInstalled() {
			return "runic service is disabled (installed)", nil
		}
		return "runic service is not installed", nil
	}
	if isLaunchdNotFound(out) || isLaunchdNotFound(err.Error()) {
		if launchdIsInstalled() {
			return "runic service is stopped (installed)", nil
		}
		return "runic service is not installed", nil
	}
	fallback, ferr := runOutput("launchctl", "list", launchdLabel)
	if ferr == nil {
		return fallback, nil
	}
	if isLaunchdDisabled(fallback) || isLaunchdDisabled(ferr.Error()) {
		if launchdIsInstalled() {
			return "runic service is disabled (installed)", nil
		}
		return "runic service is not installed", nil
	}
	if isLaunchdNotFound(fallback) || isLaunchdNotFound(ferr.Error()) {
		if launchdIsInstalled() {
			return "runic service is stopped (installed)", nil
		}
		return "runic service is not installed", nil
	}
	return out + "\n" + fallback, err
}

func launchdPlistPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, "Library", "LaunchAgents", launchdLabel+".plist"), nil
}

func launchdDomain() (string, error) {
	uid := currentUIDString()
	if uid == "" {
		return "", errors.New("unable to determine uid for launchd domain")
	}
	return "gui/" + uid, nil
}

func currentUIDString() string {
	u, err := user.Current()
	if err == nil && u.Uid != "" {
		return u.Uid
	}
	return strconv.Itoa(os.Getuid())
}

func installSystemdUser(binaryPath, configPath string) error {
	serviceBinary, err := materializeServiceBinary(binaryPath)
	if err != nil {
		return err
	}

	unitPath, err := systemdUnitPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(unitPath), 0755); err != nil {
		return fmt.Errorf("create systemd user dir: %w", err)
	}

	logDir, err := runicLogDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(logDir, 0700); err != nil {
		return fmt.Errorf("create log dir: %w", err)
	}

	service := fmt.Sprintf(`[Unit]
Description=Runic daemon
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=%q start --config %q
Restart=always
RestartSec=2
StandardOutput=append:%s
StandardError=append:%s

[Install]
WantedBy=default.target
`, serviceBinary, configPath, filepath.Join(logDir, "runic-service.log"), filepath.Join(logDir, "runic-service.err.log"))

	if err := os.WriteFile(unitPath, []byte(service), 0644); err != nil {
		return fmt.Errorf("write systemd user unit: %w", err)
	}

	if err := run("systemctl", "--user", "daemon-reload"); err != nil {
		return err
	}
	if err := run("systemctl", "--user", "enable", "--now", systemdUnit); err != nil {
		return err
	}
	return nil
}

func uninstallSystemdUser() error {
	unitPath, err := systemdUnitPath()
	if err != nil {
		return err
	}
	_ = run("systemctl", "--user", "disable", "--now", systemdUnit)
	if err := os.Remove(unitPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove systemd user unit: %w", err)
	}
	return run("systemctl", "--user", "daemon-reload")
}

func systemdUnitPath() (string, error) {
	cfg, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve user config dir: %w", err)
	}
	return filepath.Join(cfg, "systemd", "user", systemdUnit), nil
}

func runicLogDir() (string, error) {
	cfg, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve user config dir: %w", err)
	}
	return filepath.Join(cfg, "runic", "logs"), nil
}

func run(name string, args ...string) error {
	_, err := runOutput(name, args...)
	return err
}

func runOutput(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	var b bytes.Buffer
	cmd.Stdout = &b
	cmd.Stderr = &b
	err := cmd.Run()
	out := strings.TrimSpace(b.String())
	if err != nil {
		if out == "" {
			return "", fmt.Errorf("%s %s failed: %w", name, strings.Join(args, " "), err)
		}
		return out, fmt.Errorf("%s %s failed: %w\n%s", name, strings.Join(args, " "), err, out)
	}
	return out, nil
}

func xmlEscape(s string) string {
	r := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		"\"", "&quot;",
		"'", "&apos;",
	)
	return r.Replace(s)
}

func isLaunchdNotFound(out string) bool {
	l := strings.ToLower(out)
	return strings.Contains(l, "could not find service")
}

func isLaunchdDisabled(out string) bool {
	l := strings.ToLower(out)
	return strings.Contains(l, "service is disabled")
}

func launchdIsInstalled() bool {
	plistPath, err := launchdPlistPath()
	if err != nil {
		return false
	}
	_, err = os.Stat(plistPath)
	return err == nil
}

func materializeServiceBinary(sourcePath string) (string, error) {
	absSource, err := filepath.Abs(sourcePath)
	if err != nil {
		return "", fmt.Errorf("resolve binary path: %w", err)
	}
	srcInfo, err := os.Stat(absSource)
	if err != nil {
		return "", fmt.Errorf("stat binary path: %w", err)
	}
	if srcInfo.IsDir() {
		return "", fmt.Errorf("binary path is a directory: %s", absSource)
	}

	cfgDir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve user config dir: %w", err)
	}
	targetDir := filepath.Join(cfgDir, "runic", "bin")
	if err := os.MkdirAll(targetDir, 0700); err != nil {
		return "", fmt.Errorf("create service binary dir: %w", err)
	}
	targetPath := filepath.Join(targetDir, "runic")

	if samePath(absSource, targetPath) {
		return targetPath, nil
	}

	in, err := os.Open(absSource)
	if err != nil {
		return "", fmt.Errorf("open binary source: %w", err)
	}
	defer in.Close()

	tmpPath := targetPath + ".tmp"
	out, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0755)
	if err != nil {
		return "", fmt.Errorf("create service binary temp file: %w", err)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return "", fmt.Errorf("copy service binary: %w", err)
	}
	if err := out.Close(); err != nil {
		return "", fmt.Errorf("flush service binary: %w", err)
	}
	if err := os.Chmod(tmpPath, 0755); err != nil {
		return "", fmt.Errorf("chmod service binary: %w", err)
	}
	if err := os.Rename(tmpPath, targetPath); err != nil {
		return "", fmt.Errorf("install service binary: %w", err)
	}
	return targetPath, nil
}

func samePath(a, b string) bool {
	ca := filepath.Clean(a)
	cb := filepath.Clean(b)
	return ca == cb
}
