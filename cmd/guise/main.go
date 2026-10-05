// Command guise presents files in a different guise: the same file at
// another path, with specific values swapped. See `guise help`.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
)

const version = "0.1.0-dev"

const usage = `guise — the same file, in a different guise

Usage:
  guise <command> [flags] [args]

Guises:
  new <target> <path>     create a guise of a file or directory at <path>
        --fill            fill mode: target holds placeholders, guise shows values
        --vault V         vault to use (repeatable; first is primary)
        --expose NAME     show NAME's real value in this guise (repeatable)
  rm <path>               remove a guise (the target is untouched)
  list                    list guises

Vaults:
  vault init [path]       create a vault (encrypted unless --plain)
  vault set <name>        set a value (prompted, or read from stdin)
  vault get <name>        print a value
  vault list              list value names
  vault rm <name>         remove a value
  vault pattern set <name> <regex> | rm <name> | list
  review                  review detected values: promote or dismiss them
  review promote <unreviewed-name> [as <name>]
  review dismiss <unreviewed-name>

Mount:
  mount start             mount guises (asks for vault passphrases)
        --foreground      stay in the foreground
  mount stop              unmount
  mount status            show whether guises are mounted

Global flags:
  --vault V               vault for vault/review commands (default: $GUISE_VAULT,
                          nearest .guise/ vault, or your default vault)
  --help, --version
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		if !errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(os.Stderr, "guise:", err)
		}
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		fmt.Print(usage)
		return nil
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "help", "-h", "--help":
		fmt.Print(usage)
		return nil
	case "version", "--version":
		fmt.Println("guise", version)
		return nil
	case "new":
		return cmdNew(rest)
	case "rm":
		return cmdRm(rest)
	case "list", "ls":
		return cmdList(rest)
	case "vault":
		return cmdVault(rest)
	case "review":
		return cmdReview(rest)
	case "mount":
		return cmdMount(rest)
	}
	return fmt.Errorf("unknown command %q (see `guise help`)", cmd)
}

// stringList is a repeatable string flag.
type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

// parse parses flags that may appear anywhere among the arguments and
// returns the positional arguments. "--" ends flag parsing.
func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	fs.SetOutput(os.Stderr)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			pos = append(pos, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			pos = append(pos, a)
			continue
		}
		flags = append(flags, a)
		name := strings.TrimLeft(a, "-")
		if strings.Contains(name, "=") {
			continue
		}
		f := fs.Lookup(name)
		if f == nil {
			continue // let fs.Parse report it
		}
		if b, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && b.IsBoolFlag() {
			continue
		}
		if i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	}
	if err := fs.Parse(flags); err != nil {
		return nil, err
	}
	return pos, nil
}

func wantArgs(pos []string, n int, what string) error {
	if len(pos) != n {
		return fmt.Errorf("expected %s", what)
	}
	return nil
}
