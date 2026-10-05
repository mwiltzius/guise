package transform

import (
	"fmt"
	"strings"
	"sync"
)

// Variants records alternative spellings of vault values that upper/lower
// cannot reproduce, e.g. "WiLtZiUs" for lastname = "Wiltzius". Each spelling
// gets a stable number starting at 2 (the vault value itself is 1), written
// as {{pi.lastname:2}}. It is safe for concurrent use.
type Variants struct {
	mu     sync.Mutex
	byName map[string]map[int]string
	dirty  bool
}

// NewVariants returns a set seeded with name → number → spelling entries.
func NewVariants(entries map[string]map[int]string) *Variants {
	v := &Variants{byName: map[string]map[int]string{}}
	for name, spellings := range entries {
		m := make(map[int]string, len(spellings))
		for n, s := range spellings {
			m[n] = s
		}
		v.byName[name] = m
	}
	return v
}

// CheckVariant reports whether spelling can be recorded as variant n of value.
func CheckVariant(value string, n int, spelling string) error {
	if n < 2 {
		return fmt.Errorf("variant number %d must be 2 or more", n)
	}
	if spelling == value || !strings.EqualFold(spelling, value) {
		return fmt.Errorf("variant %q is not a different-case spelling of the value", spelling)
	}
	return nil
}

// find returns the number of an existing spelling, or 0.
func (v *Variants) find(name, spelling string) int {
	v.mu.Lock()
	defer v.mu.Unlock()
	for n, s := range v.byName[name] {
		if s == spelling {
			return n
		}
	}
	return 0
}

// assign returns the number for spelling, recording it if new.
func (v *Variants) assign(name, spelling string) int {
	v.mu.Lock()
	defer v.mu.Unlock()
	next := 2
	for n, s := range v.byName[name] {
		if s == spelling {
			return n
		}
		next = max(next, n+1)
	}
	if v.byName[name] == nil {
		v.byName[name] = map[int]string{}
	}
	v.byName[name][next] = spelling
	v.dirty = true
	return next
}

// Lookup returns variant n of name.
func (v *Variants) Lookup(name string, n int) (string, bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	s, ok := v.byName[name][n]
	return s, ok
}

// Remove drops all variants of name, e.g. when its value changes.
func (v *Variants) Remove(name string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if _, ok := v.byName[name]; ok {
		delete(v.byName, name)
		v.dirty = true
	}
}

// Entries returns a copy of all entries.
func (v *Variants) Entries() map[string]map[int]string {
	v.mu.Lock()
	defer v.mu.Unlock()
	out := make(map[string]map[int]string, len(v.byName))
	for name, spellings := range v.byName {
		m := make(map[int]string, len(spellings))
		for n, s := range spellings {
			m[n] = s
		}
		out[name] = m
	}
	return out
}

// TakeDirty reports whether entries changed since the last call, and resets
// the flag.
func (v *Variants) TakeDirty() bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	d := v.dirty
	v.dirty = false
	return d
}
