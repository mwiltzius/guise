package main

import (
	"fmt"
	"os"
	"os/exec"
)

// notify shows a desktop notification, best effort.
func notify(msg string) {
	if os.Getenv("GUISE_NO_NOTIFY") != "" {
		return
	}
	script := fmt.Sprintf("display notification %q with title \"guise\"", msg)
	exec.Command("/usr/bin/osascript", "-e", script).Run()
}
