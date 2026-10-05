package mount

import (
	"github.com/mwiltzius/guise/internal/fusemount"
	"github.com/mwiltzius/guise/internal/guisefs"
)

// Start mounts fsys at mountpoint.
func Start(fsys *guisefs.FS, mountpoint string) (Server, error) {
	return fusemount.Start(fsys, mountpoint)
}

// Mounted reports whether something is mounted at mountpoint.
func Mounted(mountpoint string) bool { return fusemount.Mounted(mountpoint) }

// Unmount unmounts mountpoint, lazily if it is busy.
func Unmount(mountpoint string) error { return fusemount.Unmount(mountpoint) }
