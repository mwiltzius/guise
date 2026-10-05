package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPlistIsValid(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("plutil is macOS-only")
	}
	p := filepath.Join(t.TempDir(), "agent.plist")
	content := plist("/opt/homebrew/bin/guise", "/Users/me/Library/Application Support/guise/a&b <log>")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("/usr/bin/plutil", "-lint", p).CombinedOutput(); err != nil {
		t.Fatalf("plutil: %v: %s", err, out)
	}
	if !strings.Contains(content, "<string>_serve</string>") || !strings.Contains(content, "a&amp;b &lt;log&gt;") {
		t.Fatalf("unexpected plist:\n%s", content)
	}
}
