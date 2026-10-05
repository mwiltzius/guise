package nfsmount

import (
	"errors"
	"io/fs"
	"log"
	"time"

	"github.com/go-git/go-billy/v5"
)

// changeFS is what the NFS server serves: a filesystem that also supports
// attribute changes.
type changeFS interface {
	billy.Filesystem
	billy.Change
}

// debugFS logs every failing operation (GUISE_DEBUG=1); go-nfs turns errors
// into NFS replies without logging them.
type debugFS struct{ changeFS }

func logErr(op, path string, err error) error {
	if err != nil {
		log.Printf("debug: %s %q: %v", op, path, err)
	}
	return err
}

func (d debugFS) OpenFile(name string, flag int, perm fs.FileMode) (billy.File, error) {
	f, err := d.changeFS.OpenFile(name, flag, perm)
	return f, logErr("open", name, err)
}

func (d debugFS) Lstat(name string) (fs.FileInfo, error) {
	fi, err := d.changeFS.Lstat(name)
	if err != nil && !isNotExist(err) {
		logErr("lstat", name, err)
	}
	return fi, err
}

func (d debugFS) Rename(from, to string) error {
	return logErr("rename", from+" -> "+to, d.changeFS.Rename(from, to))
}
func (d debugFS) Remove(name string) error { return logErr("remove", name, d.changeFS.Remove(name)) }
func (d debugFS) MkdirAll(name string, perm fs.FileMode) error {
	return logErr("mkdir", name, d.changeFS.MkdirAll(name, perm))
}
func (d debugFS) ReadDir(name string) ([]fs.FileInfo, error) {
	fi, err := d.changeFS.ReadDir(name)
	return fi, logErr("readdir", name, err)
}
func (d debugFS) Chmod(name string, mode fs.FileMode) error {
	return logErr("chmod "+mode.String(), name, d.changeFS.Chmod(name, mode))
}
func (d debugFS) Chown(name string, uid, gid int) error {
	return logErr("chown", name, d.changeFS.Chown(name, uid, gid))
}
func (d debugFS) Lchown(name string, uid, gid int) error {
	return logErr("lchown", name, d.changeFS.Lchown(name, uid, gid))
}
func (d debugFS) Chtimes(name string, a, m time.Time) error {
	return logErr("chtimes", name, d.changeFS.Chtimes(name, a, m))
}

func isNotExist(err error) bool { return errors.Is(err, fs.ErrNotExist) }
