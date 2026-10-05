package main

import "os/exec"

// notify shows a desktop notification if notify-send is available.
func notify(msg string) {
	if path, err := exec.LookPath("notify-send"); err == nil {
		exec.Command(path, "guise", msg).Run()
	}
}
