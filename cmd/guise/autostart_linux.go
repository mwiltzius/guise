package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Start at login on Linux: a systemd user service.

const systemdUnit = "guise.service"

func unitPath() (string, error) {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "systemd", "user", systemdUnit), nil
}

func unit(exe string) string {
	// systemd unit values: quote the path in case it contains spaces.
	return `[Unit]
Description=guise: serve guises

[Service]
ExecStart="` + strings.ReplaceAll(exe, `"`, `\"`) + `" _serve
Restart=no

[Install]
WantedBy=default.target
`
}

func autostartEnabled() bool {
	up, err := unitPath()
	if err != nil {
		return false
	}
	_, err = os.Stat(up)
	return err == nil
}

func systemctl(args ...string) error {
	out, err := exec.Command("systemctl", append([]string{"--user"}, args...)...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl --user %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func enableAutostart(p paths) error {
	exe, err := stableExecutable()
	if err != nil {
		return err
	}
	up, err := unitPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(up), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(up, []byte(unit(exe)), 0o644); err != nil {
		return err
	}
	os.Remove(p.declinedFile())
	if err := systemctl("daemon-reload"); err != nil {
		return err
	}
	return systemctl("enable", systemdUnit)
}

func disableAutostart() error {
	up, err := unitPath()
	if err != nil {
		return err
	}
	systemctl("disable", systemdUnit)
	if err := os.Remove(up); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return systemctl("daemon-reload")
}
