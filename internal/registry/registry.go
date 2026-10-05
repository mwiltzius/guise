// Package registry records the guises a user has created.
package registry

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/mwiltzius/guise/internal/rules"
)

// Modes.
const (
	ModeHide = "hide"
	ModeFill = "fill"
)

// Kinds of target.
const (
	KindFile = "file"
	KindDir  = "dir"
)

// Guise is one registered guise.
type Guise struct {
	// ID names the guise's directory in the mount.
	ID string `toml:"id"`
	// Target is the absolute path of the original file or directory.
	Target string `toml:"target"`
	// Path is the absolute path of the symlink the user asked for.
	Path string `toml:"path"`
	Kind string `toml:"kind"`
	Mode string `toml:"mode"`
	// Vaults lists vault paths, primary first.
	Vaults []string `toml:"vaults"`
	// Rules are this guise's overrides.
	Rules   rules.Config `toml:"rules,omitempty"`
	Created time.Time    `toml:"created"`
}

// Registry is the set of guises, stored as TOML in the config directory.
type Registry struct {
	Guises []Guise `toml:"guise"`

	path string
}

// File is the registry file name inside the config directory.
const File = "guises.toml"

// Load reads the registry in dir. A missing file is an empty registry.
func Load(dir string) (*Registry, error) {
	r := &Registry{path: filepath.Join(dir, File)}
	data, err := os.ReadFile(r.path)
	if errors.Is(err, os.ErrNotExist) {
		return r, nil
	}
	if err != nil {
		return nil, err
	}
	if _, err := toml.Decode(string(data), r); err != nil {
		return nil, fmt.Errorf("parse %s: %w", r.path, err)
	}
	return r, nil
}

// Path returns the registry file path.
func (r *Registry) Path() string { return r.path }

// Save writes the registry atomically.
func (r *Registry) Save() error {
	if err := os.MkdirAll(filepath.Dir(r.path), 0o700); err != nil {
		return err
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(r); err != nil {
		return err
	}
	tmp := r.path + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, r.path)
}

// Update locks the registry in dir, loads it, applies fn, and saves it if fn
// succeeds. Use it for every modification so concurrent commands don't
// clobber each other.
func Update(dir string, fn func(*Registry) error) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(dir, File+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	r, err := Load(dir)
	if err != nil {
		return err
	}
	if err := fn(r); err != nil {
		return err
	}
	return r.Save()
}

// ByPath finds a guise by its symlink path.
func (r *Registry) ByPath(path string) (*Guise, bool) {
	for i := range r.Guises {
		if r.Guises[i].Path == path {
			return &r.Guises[i], true
		}
	}
	return nil, false
}

// ByID finds a guise by ID.
func (r *Registry) ByID(id string) (*Guise, bool) {
	for i := range r.Guises {
		if r.Guises[i].ID == id {
			return &r.Guises[i], true
		}
	}
	return nil, false
}

// Add validates g, assigns an ID, and appends it.
func (r *Registry) Add(g Guise) (Guise, error) {
	if !filepath.IsAbs(g.Target) || !filepath.IsAbs(g.Path) {
		return Guise{}, errors.New("target and path must be absolute")
	}
	if g.Mode != ModeHide && g.Mode != ModeFill {
		return Guise{}, fmt.Errorf("unknown mode %q", g.Mode)
	}
	if g.Kind != KindFile && g.Kind != KindDir {
		return Guise{}, fmt.Errorf("unknown kind %q", g.Kind)
	}
	if len(g.Vaults) == 0 {
		return Guise{}, errors.New("a guise needs at least one vault")
	}
	if err := g.Rules.Validate(); err != nil {
		return Guise{}, err
	}
	if _, dup := r.ByPath(g.Path); dup {
		return Guise{}, fmt.Errorf("%s is already a guise", g.Path)
	}
	if within(g.Path, g.Target) || within(g.Target, g.Path) {
		return Guise{}, errors.New("a guise cannot be inside its own target (or vice versa)")
	}
	g.ID = newID(g.Target)
	for {
		if _, dup := r.ByID(g.ID); !dup {
			break
		}
		g.ID = newID(g.Target)
	}
	if g.Created.IsZero() {
		g.Created = time.Now().UTC().Truncate(time.Second)
	}
	r.Guises = append(r.Guises, g)
	return g, nil
}

// Remove deletes the guise with the given symlink path.
func (r *Registry) Remove(path string) (Guise, bool) {
	for i, g := range r.Guises {
		if g.Path == path {
			r.Guises = append(r.Guises[:i], r.Guises[i+1:]...)
			return g, true
		}
	}
	return Guise{}, false
}

// within reports whether p is inside (or equal to) dir.
func within(p, dir string) bool {
	rel, err := filepath.Rel(dir, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// newID makes a readable, unique-enough ID: the target's base name plus a
// random suffix, e.g. "resume-3f9a".
func newID(target string) string {
	base := strings.TrimSuffix(filepath.Base(target), filepath.Ext(target))
	var b strings.Builder
	for _, c := range strings.ToLower(base) {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			b.WriteRune(c)
		case b.Len() > 0 && !strings.HasSuffix(b.String(), "-"):
			b.WriteByte('-')
		}
		if b.Len() >= 24 {
			break
		}
	}
	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		slug = "guise"
	}
	var rnd [2]byte
	rand.Read(rnd[:])
	return slug + "-" + hex.EncodeToString(rnd[:])
}
