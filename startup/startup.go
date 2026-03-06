// Package startup manages registering the claw daemon as a system startup service.
//
// macOS  → LaunchAgent plist  (~/Library/LaunchAgents/com.xhao.claw-go.daemon.plist)
// Linux  → systemd user unit  (~/.config/systemd/user/claw-go.service)
package startup

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/XHao/claw-go/dirs"
)

const label = "com.xhao.claw-go.daemon"

// Install registers the daemon as a user-level startup service.
// binaryPath must be an absolute path to the claw binary.
// configPath is passed as --config to "claw serve".
func Install(binaryPath, configPath string) error {
	abs, err := filepath.Abs(binaryPath)
	if err != nil {
		return fmt.Errorf("resolve binary path: %w", err)
	}
	cfgAbs, err := filepath.Abs(configPath)
	if err != nil {
		return fmt.Errorf("resolve config path: %w", err)
	}

	switch runtime.GOOS {
	case "darwin":
		return installLaunchAgent(abs, cfgAbs)
	case "linux":
		return installSystemd(abs, cfgAbs)
	default:
		return fmt.Errorf("auto-start is not supported on %s; start the daemon manually: %s serve --config %s",
			runtime.GOOS, abs, cfgAbs)
	}
}

// Uninstall removes the startup service registration.
func Uninstall() error {
	switch runtime.GOOS {
	case "darwin":
		return uninstallLaunchAgent()
	case "linux":
		return uninstallSystemd()
	default:
		return fmt.Errorf("auto-start is not supported on %s", runtime.GOOS)
	}
}

// IsInstalled reports whether a startup entry already exists on disk.
func IsInstalled() bool {
	path, err := servicePath()
	if err != nil {
		return false
	}
	_, err = os.Stat(path)
	return err == nil
}

// ── macOS LaunchAgent ─────────────────────────────────────────────────────────

func launchAgentDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents"), nil
}

func launchAgentPath() (string, error) {
	d, err := launchAgentDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, label+".plist"), nil
}

func installLaunchAgent(binaryPath, configPath string) error {
	plistPath, err := launchAgentPath()
	if err != nil {
		return err
	}
	logFile := dirs.LogFile()
	workDir := dirs.Data()
	if err := os.MkdirAll(filepath.Dir(plistPath), 0o755); err != nil {
		return fmt.Errorf("create LaunchAgents dir: %w", err)
	}
	// dirs.MkdirAll() is called by runInstall before us, but be defensive.
	if err := os.MkdirAll(filepath.Dir(logFile), 0o700); err != nil {
		return fmt.Errorf("create log dir: %w", err)
	}

	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
  "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>%s</string>

  <key>ProgramArguments</key>
  <array>
    <string>%s</string>
    <string>serve</string>
    <string>--config</string>
    <string>%s</string>
  </array>

  <key>WorkingDirectory</key>
  <string>%s</string>

  <key>RunAtLoad</key>
  <true/>

  <key>KeepAlive</key>
  <true/>

  <key>StandardOutPath</key>
  <string>%s</string>

  <key>StandardErrorPath</key>
  <string>%s</string>
</dict>
</plist>
`, label, binaryPath, configPath, workDir, logFile, logFile)

	if err := os.WriteFile(plistPath, []byte(plist), 0o644); err != nil {
		return fmt.Errorf("write plist: %w", err)
	}

	// Load the agent so it starts immediately (and on every future login).
	if out, err := exec.Command("launchctl", "load", "-w", plistPath).CombinedOutput(); err != nil {
		return fmt.Errorf("launchctl load: %w\n%s", err, strings.TrimSpace(string(out)))
	}
	fmt.Printf("Installed as LaunchAgent: %s\n", plistPath)
	fmt.Printf("Working directory: %s\n", dirs.Data())
	fmt.Printf("Logs: %s\n", dirs.LogFile())
	fmt.Println("The daemon will start automatically at login and restart on failure.")
	return nil
}

func uninstallLaunchAgent() error {
	plistPath, err := launchAgentPath()
	if err != nil {
		return err
	}
	if _, err := os.Stat(plistPath); os.IsNotExist(err) {
		return fmt.Errorf("LaunchAgent not found: %s", plistPath)
	}
	// Unload (ignore error if the agent isn't currently loaded).
	exec.Command("launchctl", "unload", "-w", plistPath).Run() //nolint:errcheck
	if err := os.Remove(plistPath); err != nil {
		return fmt.Errorf("remove plist: %w", err)
	}
	fmt.Printf("Removed LaunchAgent: %s\n", plistPath)
	return nil
}

// ── Linux systemd user service ────────────────────────────────────────────────

func systemdUnitDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "systemd", "user"), nil
}

func systemdUnitPath() (string, error) {
	d, err := systemdUnitDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "claw-go.service"), nil
}

func installSystemd(binaryPath, configPath string) error {
	unitPath, err := systemdUnitPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(unitPath), 0o755); err != nil {
		return fmt.Errorf("create systemd unit dir: %w", err)
	}

	unit := fmt.Sprintf(`[Unit]
Description=claw AI assistant daemon
After=network.target

[Service]
ExecStart=%s serve --config %s
WorkingDirectory=%s
Restart=on-failure
RestartSec=5

[Install]
WantedBy=default.target
`, binaryPath, configPath, dirs.Data())

	if err := os.WriteFile(unitPath, []byte(unit), 0o644); err != nil {
		return fmt.Errorf("write unit file: %w", err)
	}

	run := func(args ...string) error {
		out, err := exec.Command("systemctl", args...).CombinedOutput()
		if err != nil {
			return fmt.Errorf("systemctl %s: %w\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	if err := run("--user", "daemon-reload"); err != nil {
		return err
	}
	if err := run("--user", "enable", "--now", "claw-go.service"); err != nil {
		return err
	}
	fmt.Printf("Installed as systemd user service: %s\n", unitPath)
	fmt.Println("Manage with: systemctl --user {status,stop,restart} claw-go")
	return nil
}

func uninstallSystemd() error {
	unitPath, err := systemdUnitPath()
	if err != nil {
		return err
	}
	if _, err := os.Stat(unitPath); os.IsNotExist(err) {
		return fmt.Errorf("systemd unit not found: %s", unitPath)
	}
	run := func(args ...string) {
		exec.Command("systemctl", args...).Run() //nolint:errcheck
	}
	run("--user", "disable", "--now", "claw-go.service")
	run("--user", "daemon-reload")
	if err := os.Remove(unitPath); err != nil {
		return fmt.Errorf("remove unit file: %w", err)
	}
	fmt.Printf("Removed systemd unit: %s\n", unitPath)
	return nil
}

// servicePath returns the platform-specific service file path.
func servicePath() (string, error) {
	switch runtime.GOOS {
	case "darwin":
		return launchAgentPath()
	case "linux":
		return systemdUnitPath()
	default:
		return "", fmt.Errorf("unsupported OS: %s", runtime.GOOS)
	}
}
