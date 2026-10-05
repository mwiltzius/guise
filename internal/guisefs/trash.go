package guisefs

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// MoveToTrash moves path to the user's Trash: ~/.Trash on macOS, the
// freedesktop.org trash elsewhere.
func MoveToTrash(path string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	dir := filepath.Join(home, ".Trash")
	if runtime.GOOS != "darwin" {
		data := os.Getenv("XDG_DATA_HOME")
		if data == "" {
			data = filepath.Join(home, ".local", "share")
		}
		dir = filepath.Join(data, "Trash", "files")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	base := filepath.Base(path)
	dst := filepath.Join(dir, base)
	for i := 1; ; i++ {
		if _, err := os.Lstat(dst); errors.Is(err, fs.ErrNotExist) {
			break
		}
		ext := filepath.Ext(base)
		dst = filepath.Join(dir, fmt.Sprintf("%s %s-%d%s", strings.TrimSuffix(base, ext),
			time.Now().Format("15.04.05"), i, ext))
	}
	return os.Rename(path, dst)
}
