// Package vault stores the values a guise swaps in and out.
//
// A vault is a TOML document, optionally encrypted with age (passphrase /
// scrypt). The same format serves both uses: an encrypted vault of personal
// information for hide mode, and a plain, hand-editable value set for fill
// mode.
//
//	[settings]
//	prefix = "pi"
//
//	[values]
//	lastname = "Wiltzius"
//
//	[variants.lastname]
//	2 = "WiLtZiUs"
//
//	[patterns]
//	employee_id = 'EMP-\d{6}'
//
//	[unreviewed]
//	"unreviewed.email.1" = "jane@example.org"
//
//	ignore = ["support@example.com"]
package vault

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"filippo.io/age"
	"github.com/BurntSushi/toml"

	"github.com/mwiltzius/guise/internal/rules"
	"github.com/mwiltzius/guise/internal/transform"
)

// PassphraseFunc supplies a passphrase when one is needed. It is not called
// for plain vaults.
type PassphraseFunc func() ([]byte, error)

// ErrExists is returned by Create when the vault file already exists.
var ErrExists = errors.New("vault already exists")

// scryptWorkFactor is age's default; tests lower it.
var scryptWorkFactor = 18

const ageHeader = "age-encryption.org/v1"

// Vault is an in-memory vault. It is not safe for concurrent mutation; the
// Unreviewed set it hands to engines is.
type Vault struct {
	Encrypted bool

	prefix     string
	values     map[string]string
	patterns   map[string]string
	ignore     []string
	unreviewed *transform.Unreviewed
	variants   *transform.Variants
	passphrase []byte // kept after unlock so Save can re-encrypt
}

type fileFormat struct {
	Ignore     []string                     `toml:"ignore,omitempty"`
	Settings   settings                     `toml:"settings"`
	Values     map[string]string            `toml:"values"`
	Variants   map[string]map[string]string `toml:"variants,omitempty"`
	Patterns   map[string]string            `toml:"patterns,omitempty"`
	Unreviewed map[string]string            `toml:"unreviewed,omitempty"`
}

type settings struct {
	Prefix string `toml:"prefix"`
}

func newVault(encrypted bool) *Vault {
	return &Vault{
		Encrypted:  encrypted,
		prefix:     transform.DefaultSyntax.Prefix,
		values:     map[string]string{},
		patterns:   map[string]string{},
		unreviewed: transform.NewUnreviewed(nil),
		variants:   transform.NewVariants(nil),
	}
}

// Create writes a new, empty vault to path. Encrypted vaults ask pass for a
// passphrase.
func Create(path string, encrypted bool, pass PassphraseFunc) (*Vault, error) {
	if _, err := os.Lstat(path); err == nil {
		return nil, fmt.Errorf("%s: %w", path, ErrExists)
	}
	v := newVault(encrypted)
	if encrypted {
		p, err := pass()
		if err != nil {
			return nil, err
		}
		if len(p) == 0 {
			return nil, errors.New("empty passphrase")
		}
		v.passphrase = p
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	return v, v.Save(path)
}

// Load reads a vault. Encrypted vaults (detected by content, not file name)
// ask pass for a passphrase.
func Load(path string, pass PassphraseFunc) (*Vault, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	v := newVault(bytes.HasPrefix(data, []byte(ageHeader)))
	if v.Encrypted {
		p, err := pass()
		if err != nil {
			return nil, err
		}
		id, err := age.NewScryptIdentity(string(p))
		if err != nil {
			return nil, err
		}
		r, err := age.Decrypt(bytes.NewReader(data), id)
		if err != nil {
			return nil, fmt.Errorf("unlock %s: %w", path, err)
		}
		if data, err = io.ReadAll(r); err != nil {
			return nil, fmt.Errorf("unlock %s: %w", path, err)
		}
		v.passphrase = p
	}
	var f fileFormat
	if _, err := toml.Decode(string(data), &f); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if f.Settings.Prefix != "" {
		v.prefix = f.Settings.Prefix
	}
	for name, value := range f.Values {
		if err := transform.CheckEntry(v.Syntax(), name, value); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		v.values[name] = value
	}
	for name, p := range f.Patterns {
		if _, err := transform.NewPatternDetector(name, p); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		v.patterns[name] = p
	}
	variants := map[string]map[int]string{}
	for name, spellings := range f.Variants {
		value, ok := v.values[name]
		if !ok {
			return nil, fmt.Errorf("%s: variants for unknown name %q", path, name)
		}
		variants[name] = map[int]string{}
		for key, spelling := range spellings {
			n, err := strconv.Atoi(key)
			if err == nil {
				err = transform.CheckVariant(value, n, spelling)
			}
			if err != nil {
				return nil, fmt.Errorf("%s: variant %s:%s: %v", path, name, key, err)
			}
			variants[name][n] = spelling
		}
	}
	v.variants = transform.NewVariants(variants)
	v.ignore = f.Ignore
	v.unreviewed = transform.NewUnreviewed(f.Unreviewed)
	return v, nil
}

// Save writes the vault atomically with owner-only permissions.
func (v *Vault) Save(path string) error {
	plain, err := v.encode()
	if err != nil {
		return err
	}
	return writeVault(path, plain, v.Encrypted, v.passphrase)
}

// encode renders the vault as TOML (fast).
func (v *Vault) encode() ([]byte, error) {
	f := fileFormat{
		Settings:   settings{Prefix: v.prefix},
		Values:     v.values,
		Patterns:   v.patterns,
		Ignore:     v.ignore,
		Unreviewed: v.unreviewed.Entries(),
		Variants:   map[string]map[string]string{},
	}
	for name, spellings := range v.variants.Entries() {
		m := map[string]string{}
		for n, spelling := range spellings {
			m[strconv.Itoa(n)] = spelling
		}
		f.Variants[name] = m
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(f); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// writeVault encrypts (slow, by design: scrypt) if needed and writes
// atomically. It touches no Vault state, so callers can run it without
// holding their locks.
func writeVault(path string, plain []byte, encrypted bool, passphrase []byte) error {
	data := plain
	if encrypted {
		rcpt, err := age.NewScryptRecipient(string(passphrase))
		if err != nil {
			return err
		}
		rcpt.SetWorkFactor(scryptWorkFactor)
		var out bytes.Buffer
		w, err := age.Encrypt(&out, rcpt)
		if err != nil {
			return err
		}
		if _, err := w.Write(data); err != nil {
			return err
		}
		if err := w.Close(); err != nil {
			return err
		}
		data = out.Bytes()
	}
	return writeAtomic(path, data)
}

func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Syntax returns the placeholder syntax for this vault.
func (v *Vault) Syntax() transform.Syntax {
	s := transform.DefaultSyntax
	s.Prefix = v.prefix
	return s
}

// SetPrefix changes the placeholder prefix ({{<prefix>.name}}).
func (v *Vault) SetPrefix(prefix string) error {
	s := v.Syntax()
	s.Prefix = prefix
	if _, err := transform.New(transform.Config{Syntax: s}); err != nil {
		return err
	}
	for name, value := range v.values {
		if err := transform.CheckEntry(s, name, value); err != nil {
			return err
		}
	}
	v.prefix = prefix
	return nil
}

// Set stores a value under name. Recorded variant spellings are dropped if
// the new value is not just a change of case.
func (v *Vault) Set(name, value string) error {
	if err := transform.CheckEntry(v.Syntax(), name, value); err != nil {
		return err
	}
	if old, ok := v.values[name]; ok && !strings.EqualFold(old, value) {
		v.variants.Remove(name)
	}
	v.values[name] = value
	return nil
}

// Get returns the value stored under name.
func (v *Vault) Get(name string) (string, bool) {
	value, ok := v.values[name]
	return value, ok
}

// Remove deletes name and reports whether it existed.
func (v *Vault) Remove(name string) bool {
	_, ok := v.values[name]
	delete(v.values, name)
	v.variants.Remove(name)
	return ok
}

// Names returns all value names, sorted.
func (v *Vault) Names() []string {
	names := make([]string, 0, len(v.values))
	for name := range v.values {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// SetPattern stores a user detection pattern (a regular expression).
func (v *Vault) SetPattern(name, pattern string) error {
	if _, err := transform.NewPatternDetector(name, pattern); err != nil {
		return err
	}
	v.patterns[name] = pattern
	return nil
}

// RemovePattern deletes a pattern and reports whether it existed.
func (v *Vault) RemovePattern(name string) bool {
	_, ok := v.patterns[name]
	delete(v.patterns, name)
	return ok
}

// Rules returns the vault's layer of rules: its patterns and ignore list.
func (v *Vault) Rules() rules.Rules {
	r := rules.Rules{Ignore: append([]string(nil), v.ignore...)}
	if len(v.patterns) > 0 {
		r.Patterns = make(map[string]string, len(v.patterns))
		for k, p := range v.patterns {
			r.Patterns[k] = p
		}
	}
	return r
}

// Unreviewed returns the set of detector matches awaiting review.
func (v *Vault) Unreviewed() *transform.Unreviewed { return v.unreviewed }

// Variants returns the recorded spellings of vault values.
func (v *Vault) Variants() *transform.Variants { return v.variants }

// Dirty reports whether engines recorded new unreviewed entries or variants
// since the last call, i.e. whether the vault should be saved.
func (v *Vault) Dirty() bool {
	u := v.unreviewed.TakeDirty()
	s := v.variants.TakeDirty()
	return u || s
}

// SuggestName proposes a vault name for an unreviewed entry: its kind
// ("email"), or the next free numbered name ("email.2") if taken.
func (v *Vault) SuggestName(unreviewedName string) string {
	kind := transform.UnreviewedKind(unreviewedName)
	if kind == "" {
		kind = "value"
	}
	if _, taken := v.values[kind]; !taken {
		return kind
	}
	for n := 2; ; n++ {
		name := kind + "." + strconv.Itoa(n)
		if _, taken := v.values[name]; !taken {
			return name
		}
	}
}

// Promote moves an unreviewed entry into the vault under a new name.
func (v *Vault) Promote(unreviewedName, name string) error {
	value, ok := v.unreviewed.Lookup(unreviewedName)
	if !ok {
		return fmt.Errorf("no unreviewed entry %q", unreviewedName)
	}
	if _, taken := v.values[name]; taken {
		return fmt.Errorf("name %q is already in the vault", name)
	}
	if err := v.Set(name, value); err != nil {
		return err
	}
	v.unreviewed.Remove(unreviewedName)
	return nil
}

// Dismiss marks an unreviewed entry as not sensitive: it is removed and its
// text is added to the ignore list so detectors stop flagging it.
func (v *Vault) Dismiss(unreviewedName string) error {
	value, ok := v.unreviewed.Lookup(unreviewedName)
	if !ok {
		return fmt.Errorf("no unreviewed entry %q", unreviewedName)
	}
	v.ignore = append(v.ignore, value)
	v.unreviewed.Remove(unreviewedName)
	return nil
}

// Mode selects how an engine built from the vault behaves.
type Mode int

const (
	// Hide: vault values, built-in detectors, and user patterns are hidden.
	Hide Mode = iota
	// Fill: only vault values are used; no detection.
	Fill
)

// Engine builds a transform engine from the vault's current contents. It
// shares the vault's Unreviewed and Variants sets, so new detections and
// spellings are saved with the vault (see Dirty).
func (v *Vault) Engine(mode Mode) (*transform.Engine, error) {
	cfg := transform.Config{Syntax: v.Syntax(), Values: v.values, Variants: v.variants}
	if mode == Hide {
		cfg.Detectors = transform.BuiltinDetectors()
		names := make([]string, 0, len(v.patterns))
		for name := range v.patterns {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			d, err := transform.NewPatternDetector(name, v.patterns[name])
			if err != nil {
				return nil, err
			}
			cfg.Detectors = append(cfg.Detectors, d)
		}
		cfg.Unreviewed = v.unreviewed
		cfg.Ignore = v.ignore
	}
	return transform.New(cfg)
}

// IsEncrypted reports whether the vault file at path is age-encrypted.
func IsEncrypted(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	head := make([]byte, len(ageHeader))
	n, _ := io.ReadFull(f, head)
	return string(head[:n]) == ageHeader, nil
}
