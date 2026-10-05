package guisefs

import (
	"errors"
	"fmt"
	"hash/fnv"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/go-git/go-billy/v5"

	"github.com/mwiltzius/guise/internal/registry"
)

var (
	_ billy.Filesystem = (*FS)(nil)
	_ billy.Change     = (*FS)(nil)
)

// statSys builds stat data for go-nfs: the real owner, and a file ID derived
// from the guise path so it stays stable when write-back replaces the
// target's inode.
func statSys(p string, real fs.FileInfo) *syscall.Stat_t {
	st := &syscall.Stat_t{}
	if real != nil {
		if s, ok := real.Sys().(*syscall.Stat_t); ok {
			*st = *s
		}
	}
	if real == nil {
		st.Uid, st.Gid = uint32(os.Getuid()), uint32(os.Getgid())
		st.Nlink = 1
	}
	h := fnv.New64a()
	h.Write([]byte(clean(p)))
	st.Ino = h.Sum64()
	return st
}

func dirInfo(p, name string, mod time.Time) fs.FileInfo {
	return fileInfo{name: name, mode: fs.ModeDir | 0o755, modTime: mod, sys: statSys(p, nil)}
}

// --- billy.Basic ---------------------------------------------------------

func (f *FS) Create(name string) (billy.File, error) {
	return f.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644)
}

func (f *FS) Open(name string) (billy.File, error) {
	return f.OpenFile(name, os.O_RDONLY, 0)
}

func (f *FS) OpenFile(name string, flag int, perm fs.FileMode) (billy.File, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n, err := f.resolveLocked(name)
	if err != nil {
		return nil, err
	}
	switch n.kind {
	case nRoot, nGuiseRoot:
		return nil, fmt.Errorf("%s: %w", name, syscall.EISDIR)
	case nScratch:
		if flag&os.O_CREATE != 0 {
			if err := os.MkdirAll(filepath.Dir(n.real), 0o700); err != nil {
				return nil, err
			}
		}
		file, err := os.OpenFile(n.real, flag, perm)
		if err != nil {
			return nil, err
		}
		return osFile{File: file, name: name}, nil
	}

	// Target file.
	if n.gone {
		if flag&os.O_CREATE == 0 {
			return nil, fs.ErrNotExist
		}
		// Recreating a hidden target (e.g. an editor writing a new file
		// after moving the original to a backup name) brings it back empty.
		delete(f.tombstones, n.real)
		n.gone = false
		flag |= os.O_TRUNC
		flag &^= os.O_EXCL
	}
	fi, statErr := os.Lstat(n.real)
	exists := statErr == nil
	if exists && fi.IsDir() {
		return nil, fmt.Errorf("%s: %w", name, syscall.EISDIR)
	}
	if !exists {
		if !errors.Is(statErr, fs.ErrNotExist) {
			return nil, statErr
		}
		if flag&os.O_CREATE == 0 {
			return nil, fs.ErrNotExist
		}
		if n.g.rec.Kind == registry.KindFile {
			return nil, fs.ErrNotExist // the target itself is gone
		}
		file, err := os.OpenFile(n.real, os.O_CREATE|os.O_EXCL|os.O_WRONLY, perm)
		if err != nil {
			return nil, err
		}
		file.Close()
		if _, err := f.newContentLocked(n); err != nil {
			return nil, err
		}
	} else {
		if flag&(os.O_CREATE|os.O_EXCL) == os.O_CREATE|os.O_EXCL {
			return nil, fs.ErrExist
		}
		if _, err := f.loadLocked(n); err != nil {
			return nil, err
		}
		if flag&os.O_TRUNC != 0 && flag&(os.O_WRONLY|os.O_RDWR) != 0 {
			if err := f.truncateLocked(n, 0); err != nil {
				return nil, err
			}
		}
	}
	return &handle{f: f, name: name, n: n, flag: flag}, nil
}

func (f *FS) Stat(name string) (fs.FileInfo, error) { return f.Lstat(name) }

func (f *FS) Lstat(name string) (fs.FileInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n, err := f.resolveLocked(name)
	if err != nil {
		return nil, err
	}
	return f.statLocked(clean(name), n)
}

func (f *FS) statLocked(p string, n node) (fs.FileInfo, error) {
	switch n.kind {
	case nRoot:
		return dirInfo(p, "/", f.regMod), nil
	case nGuiseRoot:
		return dirInfo(p, n.g.rec.ID, n.g.rec.Created), nil
	case nScratch:
		fi, err := os.Lstat(n.real)
		if err != nil {
			return nil, err
		}
		return fileInfo{name: fi.Name(), size: fi.Size(), mode: fi.Mode(), modTime: fi.ModTime(), sys: statSys(p, fi)}, nil
	}
	if n.gone {
		return nil, fs.ErrNotExist
	}
	fi, err := os.Lstat(n.real)
	if err != nil {
		return nil, err
	}
	name := path.Base(p)
	if fi.IsDir() {
		return fileInfo{name: name, mode: fi.Mode(), modTime: fi.ModTime(), sys: statSys(p, fi)}, nil
	}
	c, err := f.loadLocked(n)
	if err != nil {
		return nil, err
	}
	return fileInfo{name: name, size: int64(len(c.data)), mode: fi.Mode(), modTime: c.modTime, sys: statSys(p, fi)}, nil
}

func (f *FS) Rename(from, to string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	src, err := f.resolveLocked(from)
	if err != nil {
		return err
	}
	dst, err := f.resolveLocked(to)
	if err != nil {
		return err
	}
	if src.kind < nTarget || dst.kind < nTarget {
		return fs.ErrPermission
	}
	if src.g != dst.g {
		return syscall.EXDEV
	}
	if src.gone {
		return fs.ErrNotExist
	}
	switch {
	case src.kind == nScratch && dst.kind == nScratch:
		return os.Rename(src.real, dst.real)

	case src.kind == nTarget && dst.kind == nTarget:
		// Directory guise: a real rename. Flush first so the moved file
		// carries its latest contents.
		if c := f.cache[src.real]; c != nil {
			if err := f.flushLocked(src.real, c); err != nil {
				return err
			}
		}
		if err := os.Rename(src.real, dst.real); err != nil {
			return err
		}
		f.moveCacheLocked(src.real, dst.real)
		delete(f.tombstones, dst.real)
		return nil

	case src.kind == nScratch && dst.kind == nTarget:
		// An editor's atomic save: the scratch file's contents become the
		// target's guise contents.
		data, err := os.ReadFile(src.real)
		if err != nil {
			return err
		}
		delete(f.tombstones, dst.real)
		c, err := f.loadLocked(dst)
		if errors.Is(err, fs.ErrNotExist) {
			c, err = f.newContentLocked(dst)
		}
		if err != nil {
			return err
		}
		c.data = data
		c.dirty = true
		c.modTime = time.Now()
		if err := f.flushLocked(dst.real, c); err != nil {
			return err
		}
		return os.Remove(src.real)

	default: // target → scratch, e.g. an editor moving the original to a backup name
		c, err := f.loadLocked(src)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(dst.real), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(dst.real, c.data, 0o600); err != nil {
			return err
		}
		if err := f.flushLocked(src.real, c); err != nil {
			return err
		}
		f.tombstones[src.real] = true // hidden, never deleted
		return nil
	}
}

func (f *FS) moveCacheLocked(from, to string) {
	prefix := from + string(filepath.Separator)
	for real, c := range f.cache {
		switch {
		case real == from:
			delete(f.cache, real)
			f.cache[to] = c
		case strings.HasPrefix(real, prefix):
			delete(f.cache, real)
			f.cache[to+real[len(from):]] = c
		}
	}
}

func (f *FS) Remove(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	n, err := f.resolveLocked(name)
	if err != nil {
		return err
	}
	switch n.kind {
	case nRoot, nGuiseRoot:
		return fs.ErrPermission
	case nScratch:
		return os.Remove(n.real)
	}
	if n.gone {
		return fs.ErrNotExist
	}
	fi, err := os.Lstat(n.real)
	if err != nil {
		return err
	}
	if n.g.rec.Kind == registry.KindFile {
		f.tombstones[n.real] = true
		return nil
	}
	if fi.IsDir() {
		return os.Remove(n.real) // only succeeds if empty, hidden files included
	}
	if c := f.cache[n.real]; c != nil {
		if err := f.flushLocked(n.real, c); err != nil {
			return err
		}
		delete(f.cache, n.real)
	}
	return f.opt.Trash(n.real)
}

func (f *FS) Join(elem ...string) string { return path.Join(elem...) }

// --- billy.TempFile --------------------------------------------------------

func (f *FS) TempFile(dir, prefix string) (billy.File, error) {
	for i := 0; i < 100; i++ {
		name := path.Join(dir, fmt.Sprintf("%s%d", prefix, time.Now().UnixNano()+int64(i)))
		file, err := f.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
		if !errors.Is(err, fs.ErrExist) {
			return file, err
		}
	}
	return nil, fs.ErrExist
}

// --- billy.Dir -------------------------------------------------------------

func (f *FS) ReadDir(name string) ([]fs.FileInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n, err := f.resolveLocked(name)
	if err != nil {
		return nil, err
	}
	p := clean(name)
	var out []fs.FileInfo
	add := func(child string) {
		cp := path.Join(p, child)
		cn, err := f.resolveLocked(cp)
		if err != nil {
			return
		}
		if fi, err := f.statLocked(cp, cn); err == nil {
			out = append(out, fi)
		}
	}
	switch n.kind {
	case nRoot:
		ids := make([]string, 0, len(f.guises))
		for id := range f.guises {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			add(id)
		}
		return out, nil
	case nScratch:
		entries, err := os.ReadDir(n.real)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			add(e.Name())
		}
		return out, nil
	}

	seen := map[string]bool{}
	if n.kind == nGuiseRoot && n.g.rec.Kind == registry.KindFile {
		add(filepath.Base(n.g.rec.Target))
		seen[filepath.Base(n.g.rec.Target)] = true
	} else {
		dir := n.real
		if n.kind == nGuiseRoot {
			dir = n.g.rec.Target
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if isScratchName(e.Name()) || strings.HasPrefix(e.Name(), ".guise-tmp-") {
				continue // the guise's own copies live in scratch
			}
			seen[e.Name()] = true
			add(e.Name())
		}
	}
	// Overlay scratch entries for this directory.
	scratchDir := filepath.Join(f.opt.ScratchDir, n.g.rec.ID, filepath.FromSlash(n.rel))
	if entries, err := os.ReadDir(scratchDir); err == nil {
		for _, e := range entries {
			if !seen[e.Name()] {
				add(e.Name())
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out, nil
}

func (f *FS) MkdirAll(name string, perm fs.FileMode) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	n, err := f.resolveLocked(name)
	if err != nil {
		return err
	}
	switch n.kind {
	case nRoot, nGuiseRoot:
		return nil
	case nScratch:
		return os.MkdirAll(n.real, 0o700)
	}
	return os.MkdirAll(n.real, perm)
}

// --- billy.Symlink: symlinks are never exposed or created -----------------

func (f *FS) Symlink(target, link string) error    { return billy.ErrNotSupported }
func (f *FS) Readlink(link string) (string, error) { return "", fs.ErrInvalid }

// --- billy.Chroot -----------------------------------------------------------

func (f *FS) Chroot(string) (billy.Filesystem, error) { return nil, billy.ErrNotSupported }
func (f *FS) Root() string                            { return "/" }

// --- billy.Change ---------------------------------------------------------

func (f *FS) changeTarget(name string, fn func(real string) error) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	n, err := f.resolveLocked(name)
	if err != nil {
		return err
	}
	if n.kind < nTarget {
		return fs.ErrPermission
	}
	if n.gone {
		return fs.ErrNotExist
	}
	if c := f.cache[n.real]; c != nil && n.kind == nTarget {
		if err := f.flushLocked(n.real, c); err != nil {
			return err
		}
	}
	return fn(n.real)
}

func (f *FS) Chmod(name string, mode fs.FileMode) error {
	return f.changeTarget(name, func(real string) error { return os.Chmod(real, mode) })
}

func (f *FS) Chtimes(name string, atime, mtime time.Time) error {
	return f.changeTarget(name, func(real string) error { return os.Chtimes(real, atime, mtime) })
}

// Ownership changes are accepted and ignored: everything belongs to the
// user running guise.
func (f *FS) Lchown(name string, uid, gid int) error { return f.changeTarget(name, noop) }
func (f *FS) Chown(name string, uid, gid int) error  { return f.changeTarget(name, noop) }

func noop(string) error { return nil }
