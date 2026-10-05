package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// Starting at login uses a launchd agent on macOS. It runs `guise _serve`
// without passphrases: guises on plain vaults work right away, guises on
// encrypted vaults wait for `guise unlock` (a notification says so).

const launchdLabel = "dev.guise.serve"

func plistPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", launchdLabel+".plist"), nil
}

func (p paths) declinedFile() string { return filepath.Join(p.config, "autostart-declined") }

// stableExecutable prefers the PATH entry (e.g. Homebrew's bin symlink) over
// the resolved binary, whose versioned path changes on upgrade.
func stableExecutable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if onPath, err := exec.LookPath("guise"); err == nil {
		a, errA := filepath.EvalSymlinks(onPath)
		b, errB := filepath.EvalSymlinks(exe)
		if errA == nil && errB == nil && a == b {
			return filepath.Abs(onPath)
		}
	}
	return exe, nil
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
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("starting at login is not supported on %s yet", runtime.GOOS)
	}
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

// offerAutostart asks once (interactive terminals only) whether to start
// at login.
func offerAutostart(p paths) {
	if runtime.GOOS != "darwin" || autostartEnabled() {
		return
	}
	if _, err := os.Stat(p.declinedFile()); err == nil || !isTerminal() {
		return
	}
	if confirm("Start guise at login so your guises keep working after a restart?") {
		if err := enableAutostart(p); err != nil {
			fmt.Fprintln(os.Stderr, "guise: could not enable start at login:", err)
			return
		}
		fmt.Println("Guise will start at login. (`guise autostart off` to undo.)")
		return
	}
	os.WriteFile(p.declinedFile(), nil, 0o600)
	fmt.Println("OK. (`guise autostart on` if you change your mind.)")
}

func cmdAutostart(args []string) error {
	pos, err := parse(flag.NewFlagSet("autostart", flag.ContinueOnError), args)
	if err != nil {
		return err
	}
	p, err := statePaths()
	if err != nil {
		return err
	}
	switch {
	case len(pos) == 0 || pos[0] == "status":
		if autostartEnabled() {
			fmt.Println("Guise starts at login.")
		} else {
			fmt.Println("Guise does not start at login.")
		}
		return nil
	case pos[0] == "on":
		if err := enableAutostart(p); err != nil {
			return err
		}
		fmt.Println("Guise will start at login.")
		return nil
	case pos[0] == "off":
		if err := disableAutostart(); err != nil {
			return err
		}
		os.WriteFile(p.declinedFile(), nil, 0o600)
		fmt.Println("Guise will no longer start at login.")
		return nil
	}
	return errors.New("usage: guise autostart [on|off|status]")
}
