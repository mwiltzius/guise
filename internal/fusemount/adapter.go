// Package fusemount serves a guise filesystem through FUSE (Linux). It
// adapts the path-based billy filesystem used by the NFS backend to
// go-fuse's path-based API, so both backends share every guise rule.
package fusemount

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"syscall"
	"time"

	"github.com/go-git/go-billy/v5"
	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/hanwen/go-fuse/v2/fuse/nodefs"
	"github.com/hanwen/go-fuse/v2/fuse/pathfs"
)

// Filesystem is what the adapter needs: a billy filesystem that also
// supports attribute changes.
type Filesystem interface {
	billy.Filesystem
	billy.Change
}

type adapter struct {
	pathfs.FileSystem // defaults: ENOSYS for anything not overridden
	fs                Filesystem
}

func newAdapter(f Filesystem) *adapter {
	return &adapter{FileSystem: pathfs.NewDefaultFileSystem(), fs: f}
}

func (a *adapter) String() string { return "guise" }

// status maps Go errors to FUSE status codes.
func status(err error) fuse.Status {
	var errno syscall.Errno
	switch {
	case err == nil:
		return fuse.OK
	case errors.As(err, &errno):
		return fuse.Status(errno)
	case errors.Is(err, fs.ErrNotExist):
		return fuse.ENOENT
	case errors.Is(err, fs.ErrExist):
		return fuse.Status(syscall.EEXIST)
	case errors.Is(err, fs.ErrPermission):
		return fuse.EACCES
	case errors.Is(err, billy.ErrNotSupported):
		return fuse.ENOSYS
	case errors.Is(err, fs.ErrInvalid):
		return fuse.EINVAL
	}
	return fuse.EIO
}

func toAttr(fi fs.FileInfo) *fuse.Attr {
	a := &fuse.Attr{Size: uint64(fi.Size()), Nlink: 1}
	a.Mode = uint32(fi.Mode().Perm())
	if fi.IsDir() {
		a.Mode |= syscall.S_IFDIR
		a.Nlink = 2
	} else {
		a.Mode |= syscall.S_IFREG
	}
	a.Blocks = (a.Size + 511) / 512
	a.Owner = fuse.Owner{Uid: uint32(os.Getuid()), Gid: uint32(os.Getgid())}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		a.Ino = uint64(st.Ino)
		a.Owner = fuse.Owner{Uid: st.Uid, Gid: st.Gid}
	}
	mt := fi.ModTime()
	a.SetTimes(&mt, &mt, &mt)
	return a
}

func (a *adapter) GetAttr(name string, _ *fuse.Context) (*fuse.Attr, fuse.Status) {
	fi, err := a.fs.Lstat(name)
	if err != nil {
		return nil, status(err)
	}
	return toAttr(fi), fuse.OK
}

func (a *adapter) Access(name string, _ uint32, _ *fuse.Context) fuse.Status {
	_, err := a.fs.Lstat(name)
	return status(err)
}

func (a *adapter) Chmod(name string, mode uint32, _ *fuse.Context) fuse.Status {
	return status(a.fs.Chmod(name, fs.FileMode(mode&0o7777)))
}

func (a *adapter) Chown(name string, uid, gid uint32, _ *fuse.Context) fuse.Status {
	return status(a.fs.Chown(name, int(uid), int(gid)))
}

func (a *adapter) Utimens(name string, atime, mtime *time.Time, _ *fuse.Context) fuse.Status {
	now := time.Now()
	if atime == nil {
		atime = &now
	}
	if mtime == nil {
		mtime = &now
	}
	return status(a.fs.Chtimes(name, *atime, *mtime))
}

func (a *adapter) Truncate(name string, size uint64, _ *fuse.Context) fuse.Status {
	f, err := a.fs.OpenFile(name, os.O_RDWR, 0)
	if err != nil {
		return status(err)
	}
	defer f.Close()
	return status(f.Truncate(int64(size)))
}

func (a *adapter) Mkdir(name string, mode uint32, _ *fuse.Context) fuse.Status {
	if _, err := a.fs.Lstat(name); err == nil {
		return fuse.Status(syscall.EEXIST)
	}
	return status(a.fs.MkdirAll(name, fs.FileMode(mode&0o777)))
}

func (a *adapter) Rename(from, to string, _ *fuse.Context) fuse.Status {
	return status(a.fs.Rename(from, to))
}

func (a *adapter) Rmdir(name string, _ *fuse.Context) fuse.Status  { return status(a.fs.Remove(name)) }
func (a *adapter) Unlink(name string, _ *fuse.Context) fuse.Status { return status(a.fs.Remove(name)) }

func (a *adapter) Open(name string, flags uint32, _ *fuse.Context) (nodefs.File, fuse.Status) {
	f, err := a.fs.OpenFile(name, int(flags)&^os.O_CREATE, 0)
	if err != nil {
		return nil, status(err)
	}
	return newFile(f), fuse.OK
}

func (a *adapter) Create(name string, flags, mode uint32, _ *fuse.Context) (nodefs.File, fuse.Status) {
	f, err := a.fs.OpenFile(name, int(flags)|os.O_CREATE, fs.FileMode(mode&0o777))
	if err != nil {
		return nil, status(err)
	}
	return newFile(f), fuse.OK
}

func (a *adapter) OpenDir(name string, _ *fuse.Context) ([]fuse.DirEntry, fuse.Status) {
	infos, err := a.fs.ReadDir(name)
	if err != nil {
		return nil, status(err)
	}
	out := make([]fuse.DirEntry, 0, len(infos))
	for _, fi := range infos {
		out = append(out, fuse.DirEntry{Name: fi.Name(), Mode: toAttr(fi).Mode})
	}
	return out, fuse.OK
}

// Symlinks are never exposed or created through a guise.
func (a *adapter) Symlink(string, string, *fuse.Context) fuse.Status { return fuse.EPERM }

func (a *adapter) StatFs(string) *fuse.StatfsOut {
	return &fuse.StatfsOut{Blocks: 1 << 30, Bfree: 1 << 29, Bavail: 1 << 29, Files: 1 << 20, Ffree: 1 << 19, Bsize: 4096, NameLen: 255, Frsize: 4096}
}

// file adapts a billy.File to go-fuse.
type file struct {
	nodefs.File // defaults
	f           billy.File
}

func newFile(f billy.File) nodefs.File { return &file{File: nodefs.NewDefaultFile(), f: f} }

func (h *file) Read(dest []byte, off int64) (fuse.ReadResult, fuse.Status) {
	n, err := h.f.ReadAt(dest, off)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, status(err)
	}
	return fuse.ReadResultData(dest[:n]), fuse.OK
}

func (h *file) Write(data []byte, off int64) (uint32, fuse.Status) {
	w, ok := h.f.(io.WriterAt)
	if !ok {
		return 0, fuse.ENOSYS
	}
	n, err := w.WriteAt(data, off)
	return uint32(n), status(err)
}

func (h *file) Truncate(size uint64) fuse.Status { return status(h.f.Truncate(int64(size))) }
func (h *file) Flush() fuse.Status               { return fuse.OK }
func (h *file) Fsync(int) fuse.Status            { return fuse.OK }
func (h *file) Release()                         { h.f.Close() }
