package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Start at login on macOS: a launchd agent.

const launchdLabel = "dev.guise.serve"

func plistPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", launchdLabel+".plist"), nil
}

func plist(exe, logPath string) string {
	esc := func(s string) string {
		return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
	}
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + launchdLabel + `</string>
	<key>ProgramArguments</key>
	<array>
		<string>` + esc(exe) + `</string>
		<string>_serve</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>ProcessType</key>
	<string>Background</string>
	<key>StandardOutPath</key>
	<string>` + esc(logPath) + `</string>
	<key>StandardErrorPath</key>
	<string>` + esc(logPath) + `</string>
</dict>
</plist>
`
}

func autostartEnabled() bool {
	pp, err := plistPath()
	if err != nil {
		return false
	}
	_, err = os.Stat(pp)
	return err == nil
}

func enableAutostart(p paths) error {
	exe, err := stableExecutable()
	if err != nil {
		return err
	}
	pp, err := plistPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(pp), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(pp, []byte(plist(exe, p.logFile())), 0o644); err != nil {
		return err
	}
	os.Remove(p.declinedFile())
	// Register for future logins. RunAtLoad also starts it now; if one is
	// already running, the new instance exits because the socket is taken.
	domain := "gui/" + strconv.Itoa(os.Getuid())
	exec.Command("/bin/launchctl", "bootout", domain+"/"+launchdLabel).Run()
	if out, err := exec.Command("/bin/launchctl", "bootstrap", domain, pp).CombinedOutput(); err != nil {
		return fmt.Errorf("launchctl bootstrap: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func disableAutostart() error {
	pp, err := plistPath()
	if err != nil {
		return err
	}
	exec.Command("/bin/launchctl", "bootout", "gui/"+strconv.Itoa(os.Getuid())+"/"+launchdLabel).Run()
	if err := os.Remove(pp); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
