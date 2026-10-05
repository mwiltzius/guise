// Package rules holds the layered settings that control how a guise
// transforms text: which detectors run, which names are exposed, what to
// ignore, and extra patterns.
//
// Layers are merged in order, later layers overriding earlier ones:
// built-in defaults, vault defaults, project config (.guise/config.toml),
// per-guise overrides, and per-path rules inside a directory guise.
package rules

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/BurntSushi/toml"
	"github.com/bmatcuk/doublestar/v4"
)

// Rules is one layer of settings. Zero values mean "no opinion".
type Rules struct {
	// Detectors enables or disables detectors by kind ("phone": false).
	// Kinds not mentioned stay enabled.
	Detectors map[string]bool `toml:"detectors,omitempty"`
	// Expose shows the real value for these names ("city": true).
	Expose map[string]bool `toml:"expose,omitempty"`
	// Ignore lists exact text detectors should never flag. Additive.
	Ignore []string `toml:"ignore,omitempty"`
	// Patterns adds user detection patterns (name → regular expression).
	Patterns map[string]string `toml:"patterns,omitempty"`
}

// Merge returns r overlaid with over: map keys in over win, Ignore lists
// are concatenated.
func (r Rules) Merge(over Rules) Rules {
	return Rules{
		Detectors: mergeMap(r.Detectors, over.Detectors),
		Expose:    mergeMap(r.Expose, over.Expose),
		Ignore:    append(append([]string(nil), r.Ignore...), over.Ignore...),
		Patterns:  mergeMap(r.Patterns, over.Patterns),
	}
}

func mergeMap[V any](a, b map[string]V) map[string]V {
	if len(a) == 0 && len(b) == 0 {
		return nil
	}
	out := make(map[string]V, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

// DetectorEnabled reports whether the detector kind should run.
func (r Rules) DetectorEnabled(kind string) bool {
	on, set := r.Detectors[kind]
	return !set || on
}

// ExposedNames returns the names whose real values are shown, sorted.
func (r Rules) ExposedNames() []string {
	var out []string
	for name, on := range r.Expose {
		if on {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// PathRules applies Rules to paths matching a glob (with ** support),
// relative to the root of a directory guise.
type PathRules struct {
	Match string `toml:"match"`
	Rules
}

// Config is a rules layer plus optional per-path layers. It is the format
// of .guise/config.toml and of per-guise overrides.
type Config struct {
	Rules
	Paths []PathRules `toml:"path,omitempty"`
}

// Validate checks globs.
func (c Config) Validate() error {
	for _, p := range c.Paths {
		if !doublestar.ValidatePattern(p.Match) {
			return fmt.Errorf("invalid path pattern %q", p.Match)
		}
	}
	return nil
}

// Overlay returns c with over's base rules merged on top and over's path
// rules appended (so they apply after c's).
func (c Config) Overlay(over Config) Config {
	return Config{
		Rules: c.Rules.Merge(over.Rules),
		Paths: append(append([]PathRules(nil), c.Paths...), over.Paths...),
	}
}

// For returns the effective rules for a slash-separated path relative to
// the guise root: the base rules, then each matching path layer in order.
func (c Config) For(rel string) Rules {
	r := c.Rules
	for _, p := range c.Paths {
		if ok, _ := doublestar.Match(p.Match, rel); ok {
			r = r.Merge(p.Rules)
		}
	}
	return r
}

// ProjectFile is the project config file name inside a .guise directory.
const ProjectFile = "config.toml"

// FindProject looks for .guise/config.toml at or above dir. It returns the
// zero Config and an empty path if there is none.
func FindProject(dir string) (Config, string, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return Config{}, "", err
	}
	for {
		p := filepath.Join(dir, ".guise", ProjectFile)
		c, err := Load(p)
		if err == nil {
			return c, p, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return Config{}, "", err
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return Config{}, "", nil
		}
		dir = parent
	}
}

// Load reads a Config from a TOML file.
func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var c Config
	if _, err := toml.Decode(string(data), &c); err != nil {
		return Config{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := c.Validate(); err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}
