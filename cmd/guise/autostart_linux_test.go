package main

import (
	"strings"
	"testing"
)

func TestUnitQuotesExecutable(t *testing.T) {
	u := unit(`/home/me/my "tools"/guise`)
	if !strings.Contains(u, `ExecStart="/home/me/my \"tools\"/guise" _serve`) {
		t.Fatalf("unit:\n%s", u)
	}
}
