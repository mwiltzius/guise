package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/term"

	"github.com/mwiltzius/guise/internal/vault"
)

// Paths used by guise. The mount point lives at a short, stable path
// because guise symlinks point into it.
type paths struct {
	config  string // registry, daemon state
	mount   string
	scratch string
}

func statePaths() (paths, error) {
	cfg, err := vault.ConfigDir()
	if err != nil {
		return paths{}, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return paths{}, err
	}
	mnt := os.Getenv("GUISE_MOUNT")
	if mnt == "" {
		mnt = filepath.Join(home, ".guise", "mnt")
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return paths{}, err
	}
	return paths{config: cfg, mount: mnt, scratch: filepath.Join(cache, "guise", "scratch")}, nil
}

func (p paths) logFile() string { return filepath.Join(p.config, "mount.log") }

// absPath makes p absolute without resolving symlinks.
func absPath(p string) (string, error) {
	if strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		p = filepath.Join(home, p[2:])
	}
	return filepath.Abs(p)
}

var stdin = bufio.NewReader(os.Stdin)

func isTerminal() bool { return term.IsTerminal(int(os.Stdin.Fd())) }

// readSecret prompts on the terminal without echo, or reads a line from
// stdin when it is not a terminal (for scripting).
func readSecret(prompt string) ([]byte, error) {
	if !isTerminal() {
		line, err := stdin.ReadString('\n')
		if err != nil && !(errors.Is(err, io.EOF) && line != "") {
			return nil, fmt.Errorf("%s (from stdin): %w", strings.TrimSuffix(prompt, ": "), err)
		}
		return []byte(strings.TrimRight(line, "\r\n")), nil
	}
	fmt.Fprint(os.Stderr, prompt)
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	return b, err
}

// readLine prompts for a visible answer.
func readLine(prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	line, err := stdin.ReadString('\n')
	if err != nil && !(errors.Is(err, io.EOF) && line != "") {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

func confirm(prompt string) bool {
	if !isTerminal() {
		return false
	}
	ans, err := readLine(prompt + " [Y/n] ")
	return err == nil && (ans == "" || strings.EqualFold(ans, "y") || strings.EqualFold(ans, "yes"))
}

// passphraseFor prompts for a vault's passphrase (or reuses one entered
// earlier in this command).
func passphraseFor(path string) vault.PassphraseFunc {
	return func() ([]byte, error) { return unlockVault(path) }
}

// newPassphrase prompts twice for a new passphrase.
func newPassphrase() ([]byte, error) {
	p, err := readSecret("New vault passphrase: ")
	if err != nil {
		return nil, err
	}
	if isTerminal() {
		again, err := readSecret("Repeat passphrase: ")
		if err != nil {
			return nil, err
		}
		if string(again) != string(p) {
			return nil, errors.New("passphrases do not match")
		}
	}
	return p, nil
}

// resolveVault picks the vault for vault/review commands.
func resolveVault(flagValue string) (string, vault.Source, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", "", err
	}
	return vault.Resolve(flagValue, "", cwd)
}

// openVault resolves and loads the vault for vault/review commands.
func openVault(flagValue string) (*vault.Vault, string, error) {
	p, src, err := resolveVault(flagValue)
	if err != nil {
		return nil, "", err
	}
	v, err := vault.Load(p, passphraseFor(p))
	if errors.Is(err, os.ErrNotExist) {
		hint := "guise vault init"
		if src != vault.FromDefault {
			hint += " " + p
		}
		return nil, "", fmt.Errorf("no vault at %s (from %s); create one with `%s`", p, src, hint)
	}
	return v, p, err
}
