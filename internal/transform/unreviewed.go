package transform

import (
	"strconv"
	"strings"
	"sync"
)

const unreviewedPrefix = "unreviewed."

// IsUnreviewedName reports whether name is in the reserved unreviewed
// namespace (unreviewed.<kind>.N).
func IsUnreviewedName(name string) bool { return strings.HasPrefix(name, unreviewedPrefix) }

// UnreviewedKind returns the detector kind of an unreviewed name, e.g.
// "email" for "unreviewed.email.3".
func UnreviewedKind(name string) string {
	kind, _ := splitUnreviewed(name)
	return kind
}

func splitUnreviewed(name string) (kind string, n int) {
	rest := strings.TrimPrefix(name, unreviewedPrefix)
	i := strings.LastIndexByte(rest, '.')
	if i < 0 {
		return "", 0
	}
	n, err := strconv.Atoi(rest[i+1:])
	if err != nil {
		return "", 0
	}
	return rest[:i], n
}

// Unreviewed maps detector-flagged text to stable placeholder names
// (unreviewed.email.1, unreviewed.phone.1, ...) so a guise can be written
// back verbatim. The same text always gets the same name. It is safe for
// concurrent use.
type Unreviewed struct {
	mu      sync.Mutex
	byName  map[string]string
	byValue map[string]string
	next    map[string]int // per kind
	dirty   bool
}

// NewUnreviewed returns a set seeded with existing name → value entries.
func NewUnreviewed(entries map[string]string) *Unreviewed {
	u := &Unreviewed{byName: map[string]string{}, byValue: map[string]string{}, next: map[string]int{}}
	for name, value := range entries {
		u.byName[name] = value
		u.byValue[value] = name
		if kind, n := splitUnreviewed(name); n >= u.next[kind] {
			u.next[kind] = n + 1
		}
	}
	return u
}

func (u *Unreviewed) assign(kind, value string) string {
	if !ValidName(kind) {
		kind = "other"
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if name, ok := u.byValue[value]; ok {
		return name
	}
	n := max(u.next[kind], 1)
	u.next[kind] = n + 1
	name := unreviewedPrefix + kind + "." + strconv.Itoa(n)
	u.byName[name] = value
	u.byValue[value] = name
	u.dirty = true
	return name
}

// Lookup returns the text an unreviewed name stands for.
func (u *Unreviewed) Lookup(name string) (string, bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	v, ok := u.byName[name]
	return v, ok
}

// Remove drops an entry, e.g. after it has been promoted to the vault or
// marked as not sensitive.
func (u *Unreviewed) Remove(name string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if v, ok := u.byName[name]; ok {
		delete(u.byName, name)
		delete(u.byValue, v)
		u.dirty = true
	}
}

// Entries returns a copy of all name → value entries.
func (u *Unreviewed) Entries() map[string]string {
	u.mu.Lock()
	defer u.mu.Unlock()
	out := make(map[string]string, len(u.byName))
	for k, v := range u.byName {
		out[k] = v
	}
	return out
}

// TakeDirty reports whether entries changed since the last call, and resets
// the flag. Callers use it to decide when to persist.
func (u *Unreviewed) TakeDirty() bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	d := u.dirty
	u.dirty = false
	return d
}
