package vault

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// EnvVar names the environment variable that selects a vault.
const EnvVar = "GUISE_VAULT"

// Source says where a resolved vault path came from.
type Source string

const (
	FromFlag    Source = "flag"
	FromGuise   Source = "guise"
	FromEnv     Source = "env"
	FromProject Source = "project"
	FromDefault Source = "default"
)

// ProjectDir is the directory name searched for upward from a target.
const ProjectDir = ".guise"

// projectFiles are the vault file names looked for inside ProjectDir.
var projectFiles = []string{"vault.age", "vault.toml"}

// Resolve picks the vault path, most specific first: the --vault flag, the
// vault recorded on the guise, $GUISE_VAULT, the nearest .guise/ directory
// at or above startDir, and finally the user default (which may not exist
// yet).
func Resolve(flag, recorded, startDir string) (string, Source, error) {
	switch {
	case flag != "":
		return abs(flag, FromFlag)
	case recorded != "":
		return abs(recorded, FromGuise)
	}
	if env := os.Getenv(EnvVar); env != "" {
		return abs(env, FromEnv)
	}
	if startDir != "" {
		if p, ok, err := findProjectVault(startDir); err != nil {
			return "", "", err
		} else if ok {
			return p, FromProject, nil
		}
	}
	p, err := DefaultPath()
	return p, FromDefault, err
}

func abs(p string, s Source) (string, Source, error) {
	a, err := filepath.Abs(p)
	return a, s, err
}

func findProjectVault(start string) (string, bool, error) {
	dir, err := filepath.Abs(start)
	if err != nil {
		return "", false, err
	}
	for {
		for _, name := range projectFiles {
			p := filepath.Join(dir, ProjectDir, name)
			if _, err := os.Stat(p); err == nil {
				return p, true, nil
			} else if !errors.Is(err, fs.ErrNotExist) {
				return "", false, err
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false, nil
		}
		dir = parent
	}
}

// ConfigDir returns guise's per-user configuration directory:
// $XDG_CONFIG_HOME/guise if set, otherwise the platform default.
func ConfigDir() (string, error) {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		var err error
		if base, err = os.UserConfigDir(); err != nil {
			return "", err
		}
	}
	return filepath.Join(base, "guise"), nil
}

// DefaultPath is the user's default vault: vault.age or vault.toml in the
// config directory, whichever exists (vault.age if neither does).
func DefaultPath() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	for _, name := range projectFiles { // vault.age, then vault.toml
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return filepath.Join(dir, projectFiles[0]), nil
}
