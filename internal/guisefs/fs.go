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
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mwiltzius/guise/internal/docx"
	"github.com/mwiltzius/guise/internal/registry"
	"github.com/mwiltzius/guise/internal/rules"
	"github.com/mwiltzius/guise/internal/transform"
	"github.com/mwiltzius/guise/internal/vault"
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

	// mu guards everything below. Nothing slow (vault decryption or
	// encryption) ever runs while it is held: NFS clients time out and log
	// "server not responding" if a request waits too long.
	mu         sync.Mutex
	regMod     time.Time
	guises     map[string]*guiseState // by ID
	stacks     map[string]*vault.Stack
	cache      map[string]*content // by real path
	tombstones map[string]bool     // real paths hidden from the guise
	dirty      map[*vault.Stack]bool
	busy       map[*vault.Stack]bool // a save is writing this stack's files
	saveTimer  *time.Timer

	refreshMu sync.Mutex // serializes refreshes
	stop      chan struct{}
	stopped   chan struct{} // closed when loop exits
	stopOnce  sync.Once
}

type guiseState struct {
	rec     registry.Guise
	stack   *vault.Stack // nil if the vaults could not be opened
	project rules.Config
	engines map[string]*transform.Engine
	gen     uint64
}

// New returns an FS. It loads the registry and opens vaults immediately,
// then keeps them current in the background until Close.
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
		dirty:      map[*vault.Stack]bool{},
		busy:       map[*vault.Stack]bool{},
		stop:       make(chan struct{}),
		stopped:    make(chan struct{}),
	}
	if err := f.refresh(true, false); err != nil {
		return nil, err
	}
	go f.loop()
	return f, nil
}

// loop refreshes once a second.
func (f *FS) loop() {
	defer close(f.stopped)
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-f.stop:
			return
		case <-t.C:
			if err := f.refresh(false, false); err != nil {
				f.opt.Logf("refresh: %v", err)
			}
		}
	}
}

// Refresh picks up registry, vault, and project-config changes now.
func (f *FS) Refresh() error { return f.refresh(true, false) }

// Reopen re-reads the registry now and retries guises whose vaults could
// not be opened before (e.g. after a passphrase was supplied).
func (f *FS) Reopen() error { return f.refresh(true, true) }

// refresh reloads the registry, vaults, and project config if they changed.
// Slow work happens without f.mu; results are swapped in under it.
func (f *FS) refresh(force, retry bool) error {
	f.refreshMu.Lock()
	defer f.refreshMu.Unlock()

	// 1. Registry: open stacks for new or changed guises.
	var mod time.Time
	if fi, err := os.Stat(filepath.Join(f.opt.ConfigDir, registry.File)); err == nil {
		mod = fi.ModTime()
	}
	f.mu.Lock()
	regChanged := force || !mod.Equal(f.regMod)
	f.mu.Unlock()
	if regChanged {
		reg, err := registry.Load(f.opt.ConfigDir)
		if err != nil {
			return err
		}
		f.mu.Lock()
		need := map[string][]string{} // stack key → vault paths
		for _, rec := range reg.Guises {
			old, ok := f.guises[rec.ID]
			reuse := ok && sameRecord(old.rec, rec) && (old.stack != nil || !retry)
			if _, open := f.stacks[stackKey(rec)]; !reuse && !open {
				need[stackKey(rec)] = rec.Vaults
			}
		}
		f.mu.Unlock()

		opened := map[string]*vault.Stack{}
		failed := map[string]error{}
		for key, paths := range need {
			st, err := vault.OpenStack(paths, f.opt.PassFor)
			if err != nil {
				failed[key] = err
				continue
			}
			opened[key] = st
		}
		projects := map[string]rules.Config{}
		for _, rec := range reg.Guises {
			p, _, err := rules.FindProject(filepath.Dir(rec.Target))
			if err != nil {
				f.opt.Logf("guise %s: %v", rec.ID, err)
			}
			projects[rec.ID] = p
		}

		f.mu.Lock()
		for key, st := range opened {
			if _, exists := f.stacks[key]; !exists {
				f.stacks[key] = st
				for _, name := range st.Collisions() {
					f.opt.Logf("vaults %s: %q is defined in more than one vault; the first wins", key, name)
				}
			}
		}
		next := map[string]*guiseState{}
		for _, rec := range reg.Guises {
			if old, ok := f.guises[rec.ID]; ok && sameRecord(old.rec, rec) && (old.stack != nil || !retry) {
				next[rec.ID] = old
				continue
			}
			g := &guiseState{rec: rec, engines: map[string]*transform.Engine{}, project: projects[rec.ID]}
			g.stack = f.stacks[stackKey(rec)]
			if g.stack == nil {
				f.opt.Logf("guise %s unavailable: %v", rec.ID, failed[stackKey(rec)])
			}
			next[rec.ID] = g
		}
		f.guises = next
		f.regMod = mod
		f.mu.Unlock()
	}

	// 2. Vaults changed on disk (e.g. `guise vault set`).
	f.mu.Lock()
	type reload struct {
		st   *vault.Stack
		jobs []*vault.Job
	}
	var reloads []reload
	for _, st := range f.stacks {
		if !f.busy[st] {
			if jobs := st.ReloadJobs(); len(jobs) > 0 {
				reloads = append(reloads, reload{st, jobs})
			}
		}
	}
	f.mu.Unlock()
	for _, r := range reloads {
		vault.RunJobs(r.jobs)
	}
	f.mu.Lock()
	for _, r := range reloads {
		if err := r.st.ApplyReload(r.jobs); err != nil {
			f.opt.Logf("reload vaults: %v", err)
		}
	}
	f.mu.Unlock()

	// 3. Project config.
	f.mu.Lock()
	targets := map[*guiseState]string{}
	for _, g := range f.guises {
		targets[g] = filepath.Dir(g.rec.Target)
	}
	f.mu.Unlock()
	projects := map[*guiseState]rules.Config{}
	for g, dir := range targets {
		if p, _, err := rules.FindProject(dir); err == nil {
			projects[g] = p
		}
	}
	f.mu.Lock()
	for g, p := range projects {
		if !sameJSON(p, g.project) {
			g.project = p
			g.engines = map[string]*transform.Engine{}
		}
	}
	f.mu.Unlock()
	return nil
}

func stackKey(rec registry.Guise) string { return strings.Join(rec.Vaults, "\n") }

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
			f.dirty[g.stack] = true
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

// forwardDocx and inverseDocx convert Word documents. In hide mode the
// result is checked for any hidden value left in parts the converter does
// not handle; if one is found the document is not shown at all.
func (f *FS) forwardDocx(g *guiseState, e *transform.Engine, raw []byte) ([]byte, error) {
	opt := docx.Options{Edit: func(s string) []transform.Edit { ed, _ := e.FillEdits(s); return ed }}
	if g.hide() {
		opt = docx.Options{Edit: e.HideEdits, Leaks: e.Leaks}
	}
	out, err := docx.Transform(raw, opt)
	if g.hide() && g.stack.Dirty() {
		f.dirty[g.stack] = true
		f.scheduleSaveLocked()
	}
	return out, err
}

func (f *FS) inverseDocx(g *guiseState, e *transform.Engine, data []byte, name string) ([]byte, error) {
	if !g.hide() {
		return docx.Transform(data, docx.Options{Edit: e.UnfillEdits})
	}
	var unknown []string
	out, err := docx.Transform(data, docx.Options{Edit: func(s string) []transform.Edit {
		ed, rep := e.RevealEdits(s)
		unknown = append(unknown, rep.Unknown...)
		return ed
	}})
	if len(unknown) > 0 {
		f.opt.Logf("%s: unknown placeholders written verbatim: %v", name, unknown)
	}
	return out, err
}

func (f *FS) scheduleSaveLocked() {
	if f.saveTimer != nil {
		return
	}
	f.saveTimer = time.AfterFunc(f.opt.SaveDelay, func() {
		f.mu.Lock()
		f.saveTimer = nil
		f.mu.Unlock()
		f.saveStacks()
	})
}

// saveStacks writes new variants and detections to the vaults, encrypting
// without holding f.mu.
func (f *FS) saveStacks() {
	type save struct {
		st   *vault.Stack
		jobs []*vault.Job
		u    map[string]string
		v    map[string]map[int]string
	}
	f.mu.Lock()
	var saves []save
	for st := range f.dirty {
		if f.busy[st] {
			continue
		}
		u, v := st.Capture()
		jobs, err := st.SaveJobs()
		if errors.Is(err, vault.ErrChangedOnDisk) {
			f.scheduleSaveLocked() // the refresher reloads it first
			continue
		}
		delete(f.dirty, st)
		if err != nil {
			f.opt.Logf("save vaults: %v", err)
			continue
		}
		f.busy[st] = true
		saves = append(saves, save{st, jobs, u, v})
	}
	f.mu.Unlock()
	for _, sv := range saves {
		vault.RunJobs(sv.jobs)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, sv := range saves {
		if err := sv.st.FinishSave(sv.jobs, sv.u, sv.v); err != nil {
			f.opt.Logf("save vaults: %v", err)
			f.dirty[sv.st] = true
		}
		delete(f.busy, sv.st)
	}
}

// Close stops background work and flushes pending writes and vault changes.
func (f *FS) Close() error {
	f.stopOnce.Do(func() { close(f.stop) })
	<-f.stopped
	f.mu.Lock()
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
	f.mu.Unlock()
	if err := f.refresh(false, false); err != nil && first == nil { // merge edits made elsewhere
		first = err
	}
	f.saveStacks()
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

// sandboxStaging matches the folders macOS creates next to a document so a
// sandboxed app (e.g. Word) can stage a save: "cv.docx.sb-d4785637-AgmyQ4".
var sandboxStaging = regexp.MustCompile(`\.sb-[0-9a-f]{8}-[A-Za-z0-9]{6}$`)

// isScratchPath reports whether a guise-relative path belongs in scratch
// storage: any component with a scratch name puts its whole subtree there.
func isScratchPath(rel string) bool {
	for _, part := range strings.Split(rel, "/") {
		if isScratchName(part) {
			return true
		}
	}
	return false
}

// isScratchName reports whether a file name belongs in scratch storage:
// macOS metadata and sandbox staging folders, editor swap/backup files, and
// office lock/temp files.
func isScratchName(name string) bool {
	switch {
	case sandboxStaging.MatchString(name):
		return true
	case strings.HasPrefix(name, "._"), name == ".DS_Store", name == "4913",
		strings.HasSuffix(name, "~"),
		// Word and LibreOffice lock and temp files.
		strings.HasPrefix(name, "~$"), strings.HasPrefix(name, "~WRL"), strings.HasPrefix(name, "~WRD"),
		strings.HasPrefix(name, ".~WR"), strings.HasPrefix(name, ".~lock."), strings.HasPrefix(name, "Word Work File"),
		strings.HasPrefix(name, ".") && (strings.HasSuffix(name, ".swp") ||
			strings.HasSuffix(name, ".swo") || strings.HasSuffix(name, ".swx")):
		return true
	}
	return false
}

func clean(p string) string { return path.Clean("/" + filepath.ToSlash(p)) }

func (f *FS) resolveLocked(p string) (node, error) {
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
	if isScratchPath(rel) {
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

// HealLinks repairs file guises whose symlink was replaced by a regular
// file, as tools like GNU `sed -i` (without --follow-symlinks) do. The
// file's contents were derived from the guise, so they are written through
// the guise (reaching the target with real values), and the symlink into
// mountpoint is restored. It returns the repaired guise paths.
func (f *FS) HealLinks(mountpoint string) ([]string, error) {
	f.mu.Lock()
	var todo []*guiseState
	for _, g := range f.guises {
		if g.rec.Kind == registry.KindFile && g.stack != nil {
			todo = append(todo, g)
		}
	}
	f.mu.Unlock()

	var healed []string
	var errs []error
	for _, g := range todo {
		fi, err := os.Lstat(g.rec.Path)
		if err != nil || !fi.Mode().IsRegular() {
			continue // still a symlink, or gone
		}
		if err := f.heal(g, mountpoint); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", g.rec.Path, err))
			continue
		}
		healed = append(healed, g.rec.Path)
	}
	return healed, errors.Join(errs...)
}

func (f *FS) heal(g *guiseState, mountpoint string) error {
	data, err := os.ReadFile(g.rec.Path)
	if err != nil {
		return err
	}
	base := filepath.Base(g.rec.Target)
	h, err := f.OpenFile(path.Join("/", g.rec.ID, base), os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := h.Write(data); err != nil {
		return err
	}
	h.Close()
	f.mu.Lock()
	if c := f.cache[g.rec.Target]; c != nil {
		err = f.flushLocked(g.rec.Target, c)
	}
	f.mu.Unlock()
	if err != nil {
		return err
	}
	tmp := g.rec.Path + ".guise-relink"
	os.Remove(tmp)
	if err := os.Symlink(filepath.Join(mountpoint, g.rec.ID, base), tmp); err != nil {
		return err
	}
	return os.Rename(tmp, g.rec.Path)
}
