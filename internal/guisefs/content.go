package guisefs

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"time"
	"unicode/utf8"

	"github.com/mwiltzius/guise/internal/transform"
)

// content is the guise view of one target file.
type content struct {
	data    []byte // guise bytes
	text    bool   // target is text (binary files are never transformed)
	eng     *transform.Engine
	srcMod  time.Time // target mtime/size the data was derived from
	srcSize int64
	modTime time.Time // mtime reported to clients
	dirty   bool      // data has writes not yet written back
	timer   *time.Timer
	g       *guiseState
	name    string // guise-relative name, for logs
}

func isText(b []byte) bool { return utf8.Valid(b) && bytes.IndexByte(b, 0) < 0 }

// loadLocked returns the guise view of a target file, re-deriving it when
// the target or the engine changed. Binary files are hidden in hide mode
// and passed through unchanged in fill mode.
func (f *FS) loadLocked(n node) (*content, error) {
	e, err := f.engine(n.g, n.rel)
	if err != nil {
		return nil, err
	}
	c := f.cache[n.real]
	if c != nil && c.dirty {
		return c, nil
	}
	fi, err := os.Lstat(n.real)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, errHidden
	}
	if c != nil && c.eng == e && c.srcMod.Equal(fi.ModTime()) && c.srcSize == fi.Size() {
		if !c.text && n.g.hide() {
			return nil, errHidden
		}
		return c, nil
	}
	raw, err := os.ReadFile(n.real)
	if err != nil {
		return nil, err
	}
	next := &content{text: isText(raw), eng: e, srcMod: fi.ModTime(), srcSize: fi.Size(),
		modTime: fi.ModTime(), g: n.g, name: n.g.rec.ID + "/" + n.rel}
	if next.text {
		next.data = []byte(f.forward(n.g, e, string(raw)))
	} else {
		next.data = raw
	}
	// Same target but different output (e.g. the vault changed): bump the
	// mtime so NFS clients drop their cached copy.
	if c != nil && c.srcMod.Equal(fi.ModTime()) && !bytes.Equal(c.data, next.data) {
		next.modTime = time.Now()
	}
	f.cache[n.real] = next
	if !next.text && n.g.hide() {
		return nil, errHidden
	}
	return next, nil
}

// newContentLocked starts an empty view for a file being created.
func (f *FS) newContentLocked(n node) (*content, error) {
	e, err := f.engine(n.g, n.rel)
	if err != nil {
		return nil, err
	}
	c := &content{text: true, eng: e, modTime: time.Now(), g: n.g, name: n.g.rec.ID + "/" + n.rel}
	f.cache[n.real] = c
	return c, nil
}

// markDirtyLocked records a write and schedules write-back.
func (f *FS) markDirtyLocked(real string, c *content) {
	c.dirty = true
	c.modTime = time.Now()
	if c.timer != nil {
		c.timer.Stop()
	}
	c.timer = time.AfterFunc(f.opt.FlushDelay, func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.cache[real] == c {
			if err := f.flushLocked(real, c); err != nil {
				f.opt.Logf("write %s: %v", c.name, err)
			}
		}
	})
}

// flushLocked writes buffered guise data back to the target, converting it
// with the inverse transform. The guise data itself is kept, so clients
// keep seeing exactly what they wrote.
func (f *FS) flushLocked(real string, c *content) error {
	if !c.dirty {
		return nil
	}
	if c.timer != nil {
		c.timer.Stop()
		c.timer = nil
	}
	out := c.data
	if isText(c.data) {
		out = []byte(f.inverse(c.g, c.eng, string(c.data), c.name))
	}
	if err := writeAtomic(real, out); err != nil {
		return err
	}
	fi, err := os.Lstat(real)
	if err != nil {
		return err
	}
	c.srcMod, c.srcSize, c.dirty = fi.ModTime(), fi.Size(), false
	c.text = isText(c.data)
	return nil
}

// writeAtomic replaces path's contents, keeping its permissions.
func writeAtomic(path string, data []byte) error {
	mode := fs.FileMode(0o644)
	if fi, err := os.Lstat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".guise-tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
