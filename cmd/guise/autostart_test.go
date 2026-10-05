package main

import (
	"flag"
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

func TestParseInterspersedFlags(t *testing.T) {
	fs := newFlagSetForTest()
	pos, err := parse(fs, []string{"a", "--fill", "b", "--vault", "v1", "--vault=v2", "--", "--literal"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(pos, ",") != "a,b,--literal" {
		t.Fatalf("pos = %v", pos)
	}
	if fs.Lookup("fill").Value.String() != "true" || fs.Lookup("vault").Value.String() != "v1,v2" {
		t.Fatal("flags not parsed")
	}
}

func newFlagSetForTest() *flag.FlagSet {
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	fs.Bool("fill", false, "")
	var v stringList
	fs.Var(&v, "vault", "")
	return fs
}
