// Package guisefs is the virtual filesystem behind the guise mount.
//
// The root lists one directory per guise, named by guise ID. A directory
// guise maps its directory onto the target directory; a file guise's
// directory contains the target file (under its own name) plus scratch space
// for editor temp files. Reads return transformed text, writes are buffered
// and written back to the target after a short pause.
//
// Safety rules: targets are never deleted (directory guises move deleted
// files to the Trash, file guises only hide them); symlinks, vault files,
// .guise directories, and (in hide mode) binary files are never exposed;
// macOS metadata and editor swap/backup files live in scratch storage, never
// in target directories.
package guisefs

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"guise/internal/registry"
	"guise/internal/rules"
	"guise/internal/transform"
	"guise/internal/vault"
)

// Options configures an FS.
type Options struct {
	// ConfigDir holds the guise registry.
	ConfigDir string
	// ScratchDir holds per-guise scratch storage.
	ScratchDir string
	// PassFor supplies the passphrase function for a vault path.
	PassFor func(path string) vault.PassphraseFunc
	// Trash moves a deleted target file out of the way. Defaults to the
	// user's Trash.
	Trash func(path string) error
	// Logf receives warnings. Defaults to discarding them.
	Logf func(format string, args ...any)
	// FlushDelay is how long writes are buffered before being written back.
	FlushDelay time.Duration
	// SaveDelay is how long vault changes are batched before saving.
	SaveDelay time.Duration
}

// FS implements billy.Filesystem and billy.Change.
type FS struct {
	opt Options

	mu         sync.Mutex
	regMod     time.Time
	lastCheck  time.Time
	guises     map[string]*guiseState // by ID
	stacks     map[string]*vault.Stack
	cache      map[string]*content // by real path
	tombstones map[string]bool     // real paths hidden from the guise
	saveTimer  *time.Timer
}

type guiseState struct {
	rec     registry.Guise
	stack   *vault.Stack // nil if the vaults could not be opened
	project rules.Config
	engines map[string]*transform.Engine
	gen     uint64
}

// New returns an FS. It loads the registry and opens vaults immediately.
func New(opt Options) (*FS, error) {
	if opt.Logf == nil {
		opt.Logf = func(string, ...any) {}
	}
	if opt.Trash == nil {
		opt.Trash = MoveToTrash
	}
	if opt.FlushDelay == 0 {
		opt.FlushDelay = 300 * time.Millisecond
	}
	if opt.SaveDelay == 0 {
		opt.SaveDelay = 2 * time.Second
	}
	f := &FS{
		opt:        opt,
		guises:     map[string]*guiseState{},
		stacks:     map[string]*vault.Stack{},
		cache:      map[string]*content{},
		tombstones: map[string]bool{},
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f, f.refreshLocked(true)
}

// refresh reloads the registry, vaults, and project config if they changed.
// It checks at most once a second unless forced.
func (f *FS) refreshLocked(force bool) error {
	if !force && time.Since(f.lastCheck) < time.Second {
		return nil
	}
	f.lastCheck = time.Now()
	regPath := filepath.Join(f.opt.ConfigDir, registry.File)
	var mod time.Time
	if fi, err := os.Stat(regPath); err == nil {
		mod = fi.ModTime()
	}
	if force || !mod.Equal(f.regMod) {
		reg, err := registry.Load(f.opt.ConfigDir)
		if err != nil {
			return err
		}
		f.regMod = mod
		next := map[string]*guiseState{}
		for _, rec := range reg.Guises {
			if old, ok := f.guises[rec.ID]; ok && sameRecord(old.rec, rec) {
				next[rec.ID] = old
				continue
			}
			next[rec.ID] = f.openGuise(rec)
		}
		f.guises = next
	}
	for key, s := range f.stacks {
		if s.Changed() {
			if err := s.Reload(); err != nil {
				f.opt.Logf("reload vaults %s: %v", key, err)
			}
		}
	}
	for _, g := range f.guises {
		if p, _, err := rules.FindProject(filepath.Dir(g.rec.Target)); err == nil {
			if !sameJSON(p, g.project) {
				g.project = p
				g.engines = map[string]*transform.Engine{}
			}
		}
	}
	return nil
}

// Reopen re-reads the registry now and retries guises whose vaults could
// not be opened before (e.g. after a passphrase was supplied).
func (f *FS) Reopen() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for id, g := range f.guises {
		if g.stack == nil {
			delete(f.guises, id)
		}
	}
	return f.refreshLocked(true)
}

// Unavailable returns the paths of guises that cannot be served because
// their vaults could not be opened.
func (f *FS) Unavailable() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, g := range f.guises {
		if g.stack == nil {
			out = append(out, g.rec.Path)
		}
	}
	sort.Strings(out)
	return out
}

// Guises returns the number of registered guises.
func (f *FS) Guises() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.guises)
}

func sameRecord(a, b registry.Guise) bool { return sameJSON(a, b) }

func sameJSON(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}

func (f *FS) openGuise(rec registry.Guise) *guiseState {
	g := &guiseState{rec: rec, engines: map[string]*transform.Engine{}}
	key := strings.Join(rec.Vaults, "\n")
	s, ok := f.stacks[key]
	if !ok {
		var err error
		s, err = vault.OpenStack(rec.Vaults, f.opt.PassFor)
		if err != nil {
			f.opt.Logf("guise %s unavailable: %v", rec.ID, err)
			return g
		}
		for _, name := range s.Collisions() {
			f.opt.Logf("guise %s: %q is defined in more than one vault; the first wins", rec.ID, name)
		}
		f.stacks[key] = s
	}
	g.stack = s
	if p, _, err := rules.FindProject(filepath.Dir(rec.Target)); err == nil {
		g.project = p
	} else {
		f.opt.Logf("guise %s: %v", rec.ID, err)
	}
	return g
}

// engine returns the engine for a path inside a guise.
func (f *FS) engine(g *guiseState, rel string) (*transform.Engine, error) {
	if g.stack == nil {
		return nil, fs.ErrPermission
	}
	if g.gen != g.stack.Gen() {
		g.engines = map[string]*transform.Engine{}
		g.gen = g.stack.Gen()
	}
	cfg := rules.Config{Rules: g.stack.Rules()}.Overlay(g.project).Overlay(g.rec.Rules)
	r := cfg.For(rel)
	key, _ := json.Marshal(r)
	if e, ok := g.engines[string(key)]; ok {
		return e, nil
	}
	mode := vault.Hide
	if g.rec.Mode == registry.ModeFill {
		mode = vault.Fill
	}
	e, err := g.stack.Engine(mode, r)
	if err != nil {
		return nil, err
	}
	g.engines[string(key)] = e
	return e, nil
}

func (g *guiseState) hide() bool { return g.rec.Mode != registry.ModeFill }

// forward converts target text to guise text; inverse converts back.
func (f *FS) forward(g *guiseState, e *transform.Engine, s string) string {
	if g.hide() {
		out := e.Hide(s)
		if g.stack.Dirty() {
			f.scheduleSaveLocked()
		}
		return out
	}
	out, _ := e.Fill(s)
	return out
}

func (f *FS) inverse(g *guiseState, e *transform.Engine, s, name string) string {
	if !g.hide() {
		return e.Unfill(s)
	}
	out, rep := e.Reveal(s)
	if len(rep.Unknown) > 0 {
		f.opt.Logf("%s: unknown placeholders written verbatim: %v", name, rep.Unknown)
	}
	return out
}

func (f *FS) scheduleSaveLocked() {
	if f.saveTimer != nil {
		return
	}
	f.saveTimer = time.AfterFunc(f.opt.SaveDelay, func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.saveTimer = nil
		f.saveStacksLocked()
	})
}

func (f *FS) saveStacksLocked() {
	for key, s := range f.stacks {
		if err := s.Save(); err != nil {
			f.opt.Logf("save vaults %s: %v", key, err)
		}
	}
}

// Close flushes pending writes and vault changes.
func (f *FS) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	var first error
	for real, c := range f.cache {
		if err := f.flushLocked(real, c); err != nil && first == nil {
			first = err
		}
	}
	if f.saveTimer != nil {
		f.saveTimer.Stop()
		f.saveTimer = nil
	}
	f.saveStacksLocked()
	return first
}

// --- path resolution ---------------------------------------------------

type nodeKind int

const (
	nRoot      nodeKind = iota // the mount root
	nGuiseRoot                 // /<id>
	nTarget                    // a target file or directory
	nScratch                   // scratch storage
)

type node struct {
	kind nodeKind
	g    *guiseState
	rel  string // slash path relative to the guise root
	real string // path on disk (target or scratch)
	gone bool   // target is tombstoned: absent from the guise until recreated
}

var errHidden = fs.ErrNotExist

// isScratchName reports whether a file name belongs in scratch storage:
// macOS metadata and editor swap/backup files.
func isScratchName(name string) bool {
	switch {
	case strings.HasPrefix(name, "._"), name == ".DS_Store", name == "4913",
		strings.HasSuffix(name, "~"),
		strings.HasPrefix(name, ".") && (strings.HasSuffix(name, ".swp") ||
			strings.HasSuffix(name, ".swo") || strings.HasSuffix(name, ".swx")):
		return true
	}
	return false
}

func clean(p string) string { return path.Clean("/" + filepath.ToSlash(p)) }

func (f *FS) resolveLocked(p string) (node, error) {
	_ = f.refreshLocked(false)
	p = clean(p)
	if p == "/" {
		return node{kind: nRoot}, nil
	}
	parts := strings.Split(p[1:], "/")
	g, ok := f.guises[parts[0]]
	if !ok {
		return node{}, fs.ErrNotExist
	}
	rel := strings.Join(parts[1:], "/")
	if rel == "" {
		return node{kind: nGuiseRoot, g: g}, nil
	}
	scratch := node{kind: nScratch, g: g, rel: rel,
		real: filepath.Join(f.opt.ScratchDir, g.rec.ID, filepath.FromSlash(rel))}
	if isScratchName(path.Base(rel)) {
		return scratch, nil
	}
	if g.rec.Kind == registry.KindFile {
		if rel == filepath.Base(g.rec.Target) {
			return node{kind: nTarget, g: g, rel: rel, real: g.rec.Target, gone: f.tombstones[g.rec.Target]}, nil
		}
		return scratch, nil
	}
	real := filepath.Join(g.rec.Target, filepath.FromSlash(rel))
	if err := f.checkExposed(g, real); err != nil {
		return node{}, err
	}
	return node{kind: nTarget, g: g, rel: rel, real: real, gone: f.tombstones[real]}, nil
}

// checkExposed fails for paths a directory guise must never expose: under a
// .guise directory, through a symlink, and any known vault file.
func (f *FS) checkExposed(g *guiseState, real string) error {
	for _, st := range f.stacks { // any known vault, not just this guise's
		for _, v := range st.Paths() {
			if real == v {
				return errHidden
			}
		}
	}
	rel, _ := filepath.Rel(g.rec.Target, real)
	cur := g.rec.Target
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == ".guise" {
			return errHidden
		}
		cur = filepath.Join(cur, part)
		fi, err := os.Lstat(cur)
		if errors.Is(err, fs.ErrNotExist) {
			return nil // may be created
		}
		if err != nil {
			return err
		}
		if fi.Mode()&fs.ModeSymlink != 0 {
			return errHidden
		}
	}
	return nil
}
