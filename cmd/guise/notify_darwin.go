package main

import (
	"fmt"
	"os/exec"
)

// notify shows a desktop notification, best effort.
func notify(msg string) {
	script := fmt.Sprintf("display notification %q with title \"guise\"", msg)
	exec.Command("/usr/bin/osascript", "-e", script).Run()
}
