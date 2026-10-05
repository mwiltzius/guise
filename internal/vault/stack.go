package vault

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"sort"
	"time"

	"github.com/mwiltzius/guise/internal/rules"
	"github.com/mwiltzius/guise/internal/transform"
)

// Stack combines several vaults for one guise. The first vault is primary:
// it sets the placeholder syntax and stores unreviewed detections. When a
// name exists in more than one vault, the earliest vault wins.
type Stack struct {
	paths  []string
	vaults []*Vault
	mtimes []time.Time
	// owner maps each name to the index of the vault that provides it.
	owner      map[string]int
	values     map[string]string
	variants   *transform.Variants
	collisions []string
	gen        uint64 // incremented whenever the merged view is rebuilt
	// What is on disk as of the last load or save, so Reload can tell
	// entries added in memory from entries removed elsewhere.
	savedU map[string]string
	savedV map[string]map[int]string
}

// OpenStack loads the vaults at paths. passFor supplies a passphrase
// function per path (only called for encrypted vaults).
func OpenStack(paths []string, passFor func(path string) PassphraseFunc) (*Stack, error) {
	if len(paths) == 0 {
		return nil, errors.New("a guise needs at least one vault")
	}
	s := &Stack{paths: paths}
	for _, p := range paths {
		v, err := Load(p, passFor(p))
		if err != nil {
			return nil, err
		}
		s.vaults = append(s.vaults, v)
		s.mtimes = append(s.mtimes, mtime(p))
	}
	if err := s.rebuild(); err != nil {
		return nil, err
	}
	s.snapshot()
	return s, nil
}

func (s *Stack) snapshot() {
	s.savedU = s.Primary().unreviewed.Entries()
	s.savedV = s.variants.Entries()
}

// rebuild recomputes the merged view from the vaults.
func (s *Stack) rebuild() error {
	syn := s.Syntax()
	s.owner = map[string]int{}
	s.values = map[string]string{}
	s.collisions = nil
	variants := map[string]map[int]string{}
	for i, v := range s.vaults {
		for _, name := range v.Names() {
			if _, taken := s.owner[name]; taken {
				s.collisions = append(s.collisions, name)
				continue
			}
			value := v.values[name]
			if err := transform.CheckEntry(syn, name, value); err != nil {
				return fmt.Errorf("%s: %w", s.paths[i], err)
			}
			s.owner[name] = i
			s.values[name] = value
			if sp := v.variants.Entries()[name]; sp != nil {
				variants[name] = sp
			}
		}
	}
	sort.Strings(s.collisions)
	s.variants = transform.NewVariants(variants)
	s.gen++
	return nil
}

// Gen changes whenever the stack is rebuilt (e.g. after a reload). Engines
// built from an older generation must be rebuilt.
func (s *Stack) Gen() uint64 { return s.gen }

func mtime(path string) time.Time {
	fi, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return fi.ModTime()
}

// Paths returns the vault paths, primary first.
func (s *Stack) Paths() []string { return s.paths }

// Primary returns the first vault.
func (s *Stack) Primary() *Vault { return s.vaults[0] }

// Syntax is the primary vault's placeholder syntax.
func (s *Stack) Syntax() transform.Syntax { return s.vaults[0].Syntax() }

// Collisions lists names defined in more than one vault (earliest wins).
func (s *Stack) Collisions() []string { return s.collisions }

// Rules returns the vault layer of rules: patterns and ignore lists from all
// vaults (earlier vaults win on pattern names).
func (s *Stack) Rules() rules.Rules {
	var r rules.Rules
	for i := len(s.vaults) - 1; i >= 0; i-- {
		r = r.Merge(s.vaults[i].Rules())
	}
	return r
}

// Engine builds an engine for the given mode and effective rules.
func (s *Stack) Engine(mode Mode, r rules.Rules) (*transform.Engine, error) {
	cfg := transform.Config{Syntax: s.Syntax(), Values: s.values, Variants: s.variants}
	if mode == Hide {
		ds, err := Detectors(r)
		if err != nil {
			return nil, err
		}
		cfg.Detectors = ds
		cfg.Unreviewed = s.Primary().unreviewed
		cfg.Ignore = r.Ignore
		cfg.Expose = r.ExposedNames()
	}
	return transform.New(cfg)
}

// Detectors returns the built-in and user-pattern detectors enabled by r.
func Detectors(r rules.Rules) ([]transform.Detector, error) {
	var out []transform.Detector
	for _, d := range transform.BuiltinDetectors() {
		if r.DetectorEnabled(d.Kind()) {
			out = append(out, d)
		}
	}
	names := make([]string, 0, len(r.Patterns))
	for name := range r.Patterns {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if !r.DetectorEnabled(name) {
			continue
		}
		d, err := transform.NewPatternDetector(name, r.Patterns[name])
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, nil
}

// Dirty reports whether engines recorded new variants or unreviewed entries
// since the last call.
func (s *Stack) Dirty() bool {
	a := s.variants.TakeDirty()
	b := s.Primary().unreviewed.TakeDirty()
	return a || b
}

// Changed reports whether any vault file was modified on disk since it was
// loaded or saved by this stack.
func (s *Stack) Changed() bool {
	for i, p := range s.paths {
		if !mtime(p).Equal(s.mtimes[i]) {
			return true
		}
	}
	return false
}

// Reloading and saving are split into phases so a caller that guards the
// stack with a lock (the guise filesystem) can do the slow part — scrypt,
// about a second per encrypted vault by design — without holding it:
//
//	jobs := s.ReloadJobs()   // under the lock
//	RunJobs(jobs)            // without it
//	s.ApplyReload(jobs)      // under the lock
//
// and likewise SaveJobs / RunJobs / FinishSave. Reload and Save do all three
// for callers without such a lock.

// Job is one vault file to read or write.
type Job struct {
	i     int
	path  string
	pass  []byte
	run   func(*Job) error
	err   error
	mtime time.Time // file mtime after the job ran

	loaded *Vault // reload: the vault read from disk
	plain  []byte // save: the TOML to write
	enc    bool
}

// RunJobs runs the slow part of each job. It touches no stack state.
func RunJobs(jobs []*Job) {
	for _, j := range jobs {
		j.err = j.run(j)
		j.mtime = mtime(j.path)
	}
}

// ReloadJobs returns jobs to re-read vault files changed on disk.
func (s *Stack) ReloadJobs() []*Job {
	var jobs []*Job
	for i, p := range s.paths {
		if mtime(p).Equal(s.mtimes[i]) {
			continue
		}
		jobs = append(jobs, &Job{i: i, path: p, pass: s.vaults[i].passphrase, run: func(j *Job) error {
			v, err := Load(j.path, func() ([]byte, error) { return j.pass, nil })
			j.loaded = v
			return err
		}})
	}
	return jobs
}

// ApplyReload swaps in reloaded vaults, keeping variants and unreviewed
// entries recorded in memory since the last save. Entries removed on disk
// (e.g. promoted or dismissed via the CLI) stay removed.
func (s *Stack) ApplyReload(jobs []*Job) error {
	pending := map[string]map[int]string{}
	for name, spellings := range s.variants.Entries() {
		for n, spelling := range spellings {
			if _, saved := s.savedV[name][n]; !saved {
				if pending[name] == nil {
					pending[name] = map[int]string{}
				}
				pending[name][n] = spelling
			}
		}
	}
	pendingU := map[string]string{}
	for name, value := range s.Primary().unreviewed.Entries() {
		if _, saved := s.savedU[name]; !saved {
			pendingU[name] = value
		}
	}
	var errs []error
	for _, j := range jobs {
		if j.err != nil {
			errs = append(errs, j.err)
			continue
		}
		s.vaults[j.i] = j.loaded
		s.mtimes[j.i] = j.mtime
	}
	if err := s.rebuild(); err != nil {
		return err
	}
	s.snapshot() // what is on disk now
	// Re-apply in-memory additions that the reloaded files lack.
	u := s.Primary().unreviewed
	have := u.Entries()
	taken := map[string]bool{}
	for _, value := range have {
		taken[value] = true
	}
	for name, value := range pendingU {
		if _, exists := have[name]; !exists && !taken[value] {
			u.Restore(name, value)
		}
	}
	for i, v := range s.vaults {
		for name, spellings := range pending {
			value, ok := v.values[name]
			if !ok || s.ownerIndex(name) != i {
				continue
			}
			for n, spelling := range spellings {
				if _, exists := v.variants.Lookup(name, n); !exists && transform.CheckVariant(value, n, spelling) == nil {
					v.variants.Restore(name, n, spelling)
				}
			}
		}
	}
	if err := s.rebuild(); err != nil {
		return err
	}
	return errors.Join(errs...)
}

// Reload re-reads vaults modified on disk (see ApplyReload).
func (s *Stack) Reload() error {
	jobs := s.ReloadJobs()
	RunJobs(jobs)
	return s.ApplyReload(jobs)
}

// ownerIndex finds the first vault (in the current vaults) defining name.
func (s *Stack) ownerIndex(name string) int {
	for i, v := range s.vaults {
		if _, ok := v.values[name]; ok {
			return i
		}
	}
	return -1
}

// SaveJobs prepares writes of recorded variants to the vaults that own them
// and unreviewed entries to the primary vault, for vaults whose contents
// changed since the last save. It returns ErrChangedOnDisk if a vault file
// was modified elsewhere: reload first so those edits are not lost.
func (s *Stack) SaveJobs() ([]*Job, error) {
	if s.Changed() {
		return nil, ErrChangedOnDisk
	}
	combined := s.variants.Entries()
	unreviewed := s.Primary().unreviewed.Entries()
	var jobs []*Job
	for i, v := range s.vaults {
		before := v.variants.Entries()
		after := v.variants.Entries()
		for name, spellings := range combined {
			if s.owner[name] == i {
				after[name] = spellings
			}
		}
		changed := !reflect.DeepEqual(before, after)
		if i == 0 && !reflect.DeepEqual(unreviewed, s.savedU) {
			changed = true
		}
		if !changed {
			continue
		}
		v.variants = transform.NewVariants(after)
		plain, err := v.encode()
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, &Job{i: i, path: s.paths[i], pass: v.passphrase, plain: plain, enc: v.Encrypted,
			run: func(j *Job) error { return writeVault(j.path, j.plain, j.enc, j.pass) }})
	}
	return jobs, nil
}

// ErrChangedOnDisk means a vault was modified elsewhere since it was loaded.
var ErrChangedOnDisk = errors.New("vault changed on disk; reload before saving")

// FinishSave records which files were written. savedU/savedV describe what
// SaveJobs captured, not entries recorded while the jobs ran, so those are
// still saved next time.
func (s *Stack) FinishSave(jobs []*Job, capturedU map[string]string, capturedV map[string]map[int]string) error {
	var errs []error
	for _, j := range jobs {
		if j.err != nil {
			errs = append(errs, j.err)
			continue
		}
		s.mtimes[j.i] = j.mtime
	}
	if len(errs) == 0 {
		s.savedU, s.savedV = capturedU, capturedV
	}
	return errors.Join(errs...)
}

// Capture returns what SaveJobs is about to write, for FinishSave.
func (s *Stack) Capture() (map[string]string, map[string]map[int]string) {
	return s.Primary().unreviewed.Entries(), s.variants.Entries()
}

// Save writes recorded variants and unreviewed entries (see SaveJobs),
// reloading first if a vault changed on disk.
func (s *Stack) Save() error {
	if s.Changed() {
		if err := s.Reload(); err != nil {
			return err
		}
	}
	u, v := s.Capture()
	jobs, err := s.SaveJobs()
	if err != nil {
		return err
	}
	RunJobs(jobs)
	return s.FinishSave(jobs, u, v)
}
