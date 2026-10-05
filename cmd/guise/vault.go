package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"guise/internal/transform"
	"guise/internal/vault"
)

func cmdVault(args []string) error {
	if len(args) == 0 {
		return errors.New("expected a vault command: init, set, get, list, rm, pattern")
	}
	sub, args := args[0], args[1:]
	fs := flag.NewFlagSet("vault "+sub, flag.ContinueOnError)
	vaultFlag := fs.String("vault", "", "vault path")
	plain := fs.Bool("plain", false, "create an unencrypted vault")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}

	if sub == "init" {
		target := *vaultFlag
		if len(pos) == 1 {
			target = pos[0]
		} else if len(pos) > 1 {
			return errors.New("usage: guise vault init [path] [--plain]")
		}
		p, _, err := resolveVault(target)
		if err != nil {
			return err
		}
		if target == "" && *plain {
			p = strings.TrimSuffix(p, ".age") + ".toml"
		}
		if _, err := vault.Create(p, !*plain, newPassphrase); err != nil {
			return err
		}
		kind := "encrypted"
		if *plain {
			kind = "plain"
		}
		fmt.Printf("Created %s vault %s\n", kind, p)
		return nil
	}

	v, path, err := openVault(*vaultFlag)
	if err != nil {
		return err
	}
	save := func() error { return v.Save(path) }

	switch sub {
	case "set":
		if err := wantArgs(pos, 1, "a name: guise vault set <name>"); err != nil {
			return err
		}
		if err := transform.CheckEntry(v.Syntax(), pos[0], "x"); err != nil {
			return err
		}
		value, err := readSecret("Value for " + pos[0] + ": ")
		if err != nil {
			return err
		}
		if err := v.Set(pos[0], string(value)); err != nil {
			return err
		}
		if len(value) < 3 {
			fmt.Fprintf(os.Stderr, "note: short values match more often; %q will be replaced wherever it appears as a whole word\n", pos[0])
		}
		return save()
	case "get":
		if err := wantArgs(pos, 1, "a name: guise vault get <name>"); err != nil {
			return err
		}
		value, ok := v.Get(pos[0])
		if !ok {
			return fmt.Errorf("no value named %q", pos[0])
		}
		fmt.Println(value)
		return nil
	case "list", "ls":
		names := v.Names()
		if len(names) == 0 {
			fmt.Println("(no values)")
		}
		for _, name := range names {
			fmt.Println(name)
		}
		if n := len(v.Unreviewed().Entries()); n > 0 {
			fmt.Fprintf(os.Stderr, "\n%d detected value(s) awaiting review: run `guise review`\n", n)
		}
		return nil
	case "rm":
		if err := wantArgs(pos, 1, "a name: guise vault rm <name>"); err != nil {
			return err
		}
		if !v.Remove(pos[0]) {
			return fmt.Errorf("no value named %q", pos[0])
		}
		return save()
	case "pattern":
		return cmdPattern(v, pos, save)
	}
	return fmt.Errorf("unknown vault command %q", sub)
}

func cmdPattern(v *vault.Vault, pos []string, save func() error) error {
	if len(pos) == 0 {
		return errors.New("usage: guise vault pattern set <name> <regex> | rm <name> | list")
	}
	switch pos[0] {
	case "set":
		if len(pos) != 3 {
			return errors.New("usage: guise vault pattern set <name> <regex>")
		}
		if err := v.SetPattern(pos[1], pos[2]); err != nil {
			return err
		}
		return save()
	case "rm":
		if len(pos) != 2 {
			return errors.New("usage: guise vault pattern rm <name>")
		}
		if !v.RemovePattern(pos[1]) {
			return fmt.Errorf("no pattern named %q", pos[1])
		}
		return save()
	case "list", "ls":
		r := v.Rules()
		names := make([]string, 0, len(r.Patterns))
		for name := range r.Patterns {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			fmt.Printf("%s\t%s\n", name, r.Patterns[name])
		}
		return nil
	}
	return fmt.Errorf("unknown pattern command %q", pos[0])
}

func cmdReview(args []string) error {
	fs := flag.NewFlagSet("review", flag.ContinueOnError)
	vaultFlag := fs.String("vault", "", "vault path")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	v, path, err := openVault(*vaultFlag)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		switch {
		case pos[0] == "promote" && (len(pos) == 2 || len(pos) == 4 && pos[2] == "as"):
			name := v.SuggestName(pos[1])
			if len(pos) == 4 {
				name = pos[3]
			}
			if err := v.Promote(pos[1], name); err != nil {
				return err
			}
			fmt.Printf("Promoted %s to {{%s.%s}}\n", pos[1], v.Syntax().Prefix, name)
		case pos[0] == "dismiss" && len(pos) == 2:
			if err := v.Dismiss(pos[1]); err != nil {
				return err
			}
			fmt.Printf("Dismissed %s; it will no longer be flagged\n", pos[1])
		default:
			return errors.New("usage: guise review [promote <name> [as <name>] | dismiss <name>]")
		}
		return v.Save(path)
	}

	entries := v.Unreviewed().Entries()
	if len(entries) == 0 {
		fmt.Println("Nothing to review.")
		return nil
	}
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)
	if !isTerminal() {
		for _, name := range names {
			fmt.Printf("%s\t%s\n", name, entries[name])
		}
		return nil
	}
	changed := false
	for _, name := range names {
		suggest := v.SuggestName(name)
		fmt.Printf("\n%s (%s): %q\n", name, transform.UnreviewedKind(name), entries[name])
		ans, err := readLine(fmt.Sprintf("  [p]romote as %s, promote as [n]ame…, [d]ismiss, [s]kip, [q]uit? ", suggest))
		if err != nil {
			return err
		}
		switch strings.ToLower(ans) {
		case "p", "":
			err = v.Promote(name, suggest)
		case "n":
			var as string
			if as, err = readLine("  name: "); err == nil {
				err = v.Promote(name, as)
			}
		case "d":
			err = v.Dismiss(name)
		case "q":
			if changed {
				return v.Save(path)
			}
			return nil
		default:
			continue
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "  error:", err)
			continue
		}
		changed = true
	}
	if changed {
		return v.Save(path)
	}
	return nil
}
