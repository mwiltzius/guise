package main

import (
	"flag"
	"strings"
	"testing"
)

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
