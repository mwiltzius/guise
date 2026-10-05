package guisefs

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"time"
)

// handle is an open target file. All reads and writes go through the
// shared content buffer.
type handle struct {
	f    *FS
	name string // as presented to Open
	n    node
	pos  int64
	flag int
}

func (h *handle) Name() string { return h.name }

func (h *handle) content() (*content, error) {
	if c := h.f.cache[h.n.real]; c != nil && c.dirty {
		return c, nil
	}
	return h.f.loadLocked(h.n)
}

func (h *handle) ReadAt(p []byte, off int64) (int, error) {
	h.f.mu.Lock()
	defer h.f.mu.Unlock()
	if h.flag&(os.O_WRONLY) != 0 {
		return 0, fs.ErrPermission
	}
	c, err := h.content()
	if err != nil {
		return 0, err
	}
	if off >= int64(len(c.data)) {
		return 0, io.EOF
	}
	n := copy(p, c.data[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func (h *handle) Read(p []byte) (int, error) {
	n, err := h.ReadAt(p, h.pos)
	h.pos += int64(n)
	return n, err
}

func (h *handle) WriteAt(p []byte, off int64) (int, error) {
	h.f.mu.Lock()
	defer h.f.mu.Unlock()
	if h.flag&(os.O_WRONLY|os.O_RDWR) == 0 {
		return 0, fs.ErrPermission
	}
	c, err := h.content()
	if err != nil {
		return 0, err
	}
	if end := off + int64(len(p)); end > int64(len(c.data)) {
		grown := make([]byte, end)
		copy(grown, c.data)
		c.data = grown
	}
	copy(c.data[off:], p)
	h.f.markDirtyLocked(h.n.real, c)
	return len(p), nil
}

func (h *handle) Write(p []byte) (int, error) {
	if h.flag&os.O_APPEND != 0 {
		h.f.mu.Lock()
		if c, err := h.content(); err == nil {
			h.pos = int64(len(c.data))
		}
		h.f.mu.Unlock()
	}
	n, err := h.WriteAt(p, h.pos)
	h.pos += int64(n)
	return n, err
}

func (h *handle) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
	case io.SeekCurrent:
		offset += h.pos
	case io.SeekEnd:
		h.f.mu.Lock()
		c, err := h.content()
		h.f.mu.Unlock()
		if err != nil {
			return 0, err
		}
		offset += int64(len(c.data))
	default:
		return 0, errors.New("invalid whence")
	}
	if offset < 0 {
		return 0, errors.New("negative position")
	}
	h.pos = offset
	return offset, nil
}

func (h *handle) Truncate(size int64) error {
	h.f.mu.Lock()
	defer h.f.mu.Unlock()
	return h.f.truncateLocked(h.n, size)
}

func (f *FS) truncateLocked(n node, size int64) error {
	c := f.cache[n.real]
	if c == nil || !c.dirty {
		var err error
		if c, err = f.loadLocked(n); err != nil {
			return err
		}
	}
	if size < int64(len(c.data)) {
		c.data = c.data[:size]
	} else if size > int64(len(c.data)) {
		grown := make([]byte, size)
		copy(grown, c.data)
		c.data = grown
	}
	f.markDirtyLocked(n.real, c)
	return nil
}

func (h *handle) Close() error  { return nil }
func (h *handle) Lock() error   { return nil }
func (h *handle) Unlock() error { return nil }

// osFile adapts *os.File (scratch storage) to billy.File.
type osFile struct {
	*os.File
	name string
}

func (o osFile) Name() string  { return o.name }
func (o osFile) Lock() error   { return nil }
func (o osFile) Unlock() error { return nil }

// fileInfo is a synthetic fs.FileInfo.
type fileInfo struct {
	name    string
	size    int64
	mode    fs.FileMode
	modTime time.Time
	sys     any
}

func (i fileInfo) Name() string       { return i.name }
func (i fileInfo) Size() int64        { return i.size }
func (i fileInfo) Mode() fs.FileMode  { return i.mode }
func (i fileInfo) ModTime() time.Time { return i.modTime }
func (i fileInfo) IsDir() bool        { return i.mode.IsDir() }
func (i fileInfo) Sys() any           { return i.sys }
