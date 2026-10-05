package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// Starting at login runs `guise _serve` without passphrases: guises on plain
// vaults work right away, guises on encrypted vaults wait for `guise unlock`
// (a notification says so). macOS uses a launchd agent, Linux a systemd user
// service; see autostart_<os>.go for autostartEnabled, enableAutostart and
// disableAutostart.

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

// offerAutostart asks once (interactive terminals only) whether to start
// at login.
func offerAutostart(p paths) {
	if autostartEnabled() {
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
