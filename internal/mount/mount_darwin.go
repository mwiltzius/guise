package mount

import (
	"github.com/mwiltzius/guise/internal/guisefs"
	"github.com/mwiltzius/guise/internal/nfsmount"
)

// Start mounts fsys at mountpoint.
func Start(fsys *guisefs.FS, mountpoint string) (Server, error) {
	return nfsmount.Start(fsys, mountpoint)
}

// Mounted reports whether something is mounted at mountpoint.
func Mounted(mountpoint string) bool { return nfsmount.Mounted(mountpoint) }

// Unmount unmounts mountpoint, forcing if needed.
func Unmount(mountpoint string) error { return nfsmount.Unmount(mountpoint) }
