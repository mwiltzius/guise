package guisefs

import (
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// MoveToTrash moves path to the user's Trash: ~/.Trash on macOS, the
// freedesktop.org trash (with a .trashinfo record, so file managers can
// restore it) elsewhere.
func MoveToTrash(path string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	if runtime.GOOS == "darwin" {
		dir := filepath.Join(home, ".Trash")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		dst, err := freeName(dir, filepath.Base(path))
		if err != nil {
			return err
		}
		return os.Rename(path, dst)
	}

	data := os.Getenv("XDG_DATA_HOME")
	if data == "" {
		data = filepath.Join(home, ".local", "share")
	}
	files := filepath.Join(data, "Trash", "files")
	info := filepath.Join(data, "Trash", "info")
	for _, d := range []string{files, info} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	// Reserve the name by creating its .trashinfo exclusively first, as the
	// spec requires, then move the file.
	for i := 0; ; i++ {
		name := filepath.Base(path)
		if i > 0 {
			ext := filepath.Ext(name)
			name = fmt.Sprintf("%s.%d%s", strings.TrimSuffix(name, ext), i, ext)
		}
		rec, err := os.OpenFile(filepath.Join(info, name+".trashinfo"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return err
		}
		abs, _ := filepath.Abs(path)
		fmt.Fprintf(rec, "[Trash Info]\nPath=%s\nDeletionDate=%s\n",
			(&url.URL{Path: abs}).EscapedPath(), time.Now().Format("2006-01-02T15:04:05"))
		rec.Close()
		if err := os.Rename(path, filepath.Join(files, name)); err != nil {
			os.Remove(filepath.Join(info, name+".trashinfo"))
			return err
		}
		return nil
	}
}

// freeName returns dir/base, or a timestamped variant if that exists.
func freeName(dir, base string) (string, error) {
	dst := filepath.Join(dir, base)
	for i := 1; ; i++ {
		if _, err := os.Lstat(dst); errors.Is(err, fs.ErrNotExist) {
			return dst, nil
		}
		ext := filepath.Ext(base)
		dst = filepath.Join(dir, fmt.Sprintf("%s %s-%d%s", strings.TrimSuffix(base, ext), time.Now().Format("15.04.05"), i, ext))
	}
}
