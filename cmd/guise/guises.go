package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/mwiltzius/guise/internal/registry"
	"github.com/mwiltzius/guise/internal/rules"
	"github.com/mwiltzius/guise/internal/vault"
)

func cmdNew(args []string) error {
	fs := flag.NewFlagSet("new", flag.ContinueOnError)
	fill := fs.Bool("fill", false, "fill mode")
	var vaults, expose stringList
	fs.Var(&vaults, "vault", "vault path (repeatable)")
	fs.Var(&expose, "expose", "name to expose (repeatable)")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := wantArgs(pos, 2, "a target and a path: guise new <target> <path>"); err != nil {
		return err
	}
	target, err := absPath(pos[0])
	if err != nil {
		return err
	}
	link, err := absPath(pos[1])
	if err != nil {
		return err
	}
	fi, err := os.Stat(target)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(link); err == nil {
		return fmt.Errorf("%s already exists", link)
	}
	if _, err := os.Stat(filepath.Dir(link)); err != nil {
		return fmt.Errorf("parent directory of %s: %w", link, err)
	}

	// Vaults: explicit, else resolved from the target's location.
	var vaultPaths []string
	for _, v := range vaults {
		p, err := absPath(v)
		if err != nil {
			return err
		}
		vaultPaths = append(vaultPaths, p)
	}
	if len(vaultPaths) == 0 {
		p, src, err := vault.Resolve("", "", filepath.Dir(target))
		if err != nil {
			return err
		}
		if _, err := os.Stat(p); errors.Is(err, os.ErrNotExist) {
			if src != vault.FromDefault || !confirm("No vault yet. Create an encrypted vault at "+p+"?") {
				return fmt.Errorf("no vault at %s; create one with `guise vault init`", p)
			}
			var created []byte
			if _, err := vault.Create(p, true, func() ([]byte, error) {
				pw, err := newPassphrase()
				created = pw
				return pw, err
			}); err != nil {
				return err
			}
			known[p] = created
		}
		vaultPaths = []string{p}
	}
	for _, p := range vaultPaths {
		if _, err := os.Stat(p); err != nil {
			return fmt.Errorf("vault %s: %w", p, err)
		}
	}

	g := registry.Guise{Target: target, Path: link, Kind: registry.KindFile, Mode: registry.ModeHide, Vaults: vaultPaths}
	if fi.IsDir() {
		g.Kind = registry.KindDir
	}
	if *fill {
		g.Mode = registry.ModeFill
	}
	if len(expose) > 0 {
		g.Rules = rules.Config{Rules: rules.Rules{Expose: map[string]bool{}}}
		for _, name := range expose {
			g.Rules.Expose[name] = true
		}
	}

	p, err := statePaths()
	if err != nil {
		return err
	}
	err = registry.Update(p.config, func(r *registry.Registry) error {
		var err error
		g, err = r.Add(g)
		return err
	})
	if err != nil {
		return err
	}
	dest := filepath.Join(p.mount, g.ID)
	if g.Kind == registry.KindFile {
		dest = filepath.Join(dest, filepath.Base(target))
	}
	if err := os.Symlink(dest, link); err != nil {
		registry.Update(p.config, func(r *registry.Registry) error { r.Remove(link); return nil })
		return err
	}
	if err := ensureServing(p, vaultPaths); err != nil {
		return fmt.Errorf("created %s, but it cannot be served yet: %w", link, err)
	}
	if err := waitReady(link); err != nil {
		return err
	}
	fmt.Printf("%s → guise of %s (%s mode)\n", link, target, g.Mode)
	offerAutostart(p)
	return nil
}

// waitReady waits until the guise can be opened.
func waitReady(link string) error {
	var err error
	for i := 0; i < 50; i++ {
		if _, err = os.Stat(link); err == nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("%s is not available yet: %w (see `guise status`)", link, err)
}

func cmdRm(args []string) error {
	fs := flag.NewFlagSet("rm", flag.ContinueOnError)
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := wantArgs(pos, 1, "a guise path: guise rm <path>"); err != nil {
		return err
	}
	link, err := absPath(pos[0])
	if err != nil {
		return err
	}
	p, err := statePaths()
	if err != nil {
		return err
	}
	var removed registry.Guise
	err = registry.Update(p.config, func(r *registry.Registry) error {
		g, ok := r.Remove(link)
		if !ok {
			return fmt.Errorf("%s is not a guise (see `guise list`)", link)
		}
		removed = g
		return nil
	})
	if err != nil {
		return err
	}
	// Remove the symlink only if it still points into our mount.
	if dest, err := os.Readlink(link); err == nil && strings.HasPrefix(dest, filepath.Join(p.mount, removed.ID)) {
		os.Remove(link)
	}
	fmt.Printf("Removed guise %s (target %s untouched)\n", link, removed.Target)
	return nil
}

func cmdList(args []string) error {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	if _, err := parse(fs, args); err != nil {
		return err
	}
	p, err := statePaths()
	if err != nil {
		return err
	}
	r, err := registry.Load(p.config)
	if err != nil {
		return err
	}
	if len(r.Guises) == 0 {
		fmt.Println("No guises. Create one with `guise new <target> <path>`.")
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "GUISE\tTARGET\tMODE\tVAULTS")
	for _, g := range r.Guises {
		status := ""
		if _, err := os.Stat(g.Target); err != nil {
			status = " (target missing)"
		}
		fmt.Fprintf(tw, "%s\t%s%s\t%s\t%s\n", g.Path, g.Target, status, g.Mode, strings.Join(g.Vaults, ", "))
	}
	tw.Flush()
	if resp, err := status(p); err != nil {
		fmt.Println("\nNot being served; run `guise start` (or any `guise new`).")
	} else if len(resp.Locked) > 0 {
		fmt.Printf("\n%d vault(s) locked; run `guise unlock`.\n", len(resp.Locked))
	}
	return nil
}
