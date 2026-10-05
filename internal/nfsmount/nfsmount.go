// Package nfsmount serves a billy filesystem over NFSv3 on localhost and
// mounts it with the operating system's built-in NFS client, so no driver
// is needed. Only macOS is supported for now.
package nfsmount

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/go-git/go-billy/v5"
	nfs "github.com/willscott/go-nfs"
	nfshelper "github.com/willscott/go-nfs/helpers"
)

// Server is a running NFS server and its mount.
type Server struct {
	listener   net.Listener
	mountpoint string
	token      string
	done       chan error
}

// tokenHandler only allows mounts of the secret export path, so other local
// processes that find the port cannot mount the filesystem without it.
type tokenHandler struct {
	nfs.Handler
	export string
}

func (h tokenHandler) Mount(ctx context.Context, conn net.Conn, req nfs.MountRequest) (nfs.MountStatus, billy.Filesystem, []nfs.AuthFlavor) {
	if string(req.Dirpath) != h.export {
		return nfs.MountStatusErrAcces, nil, nil
	}
	return h.Handler.Mount(ctx, conn, req)
}

// Start serves fsys on a random localhost port and mounts it at mountpoint
// (which must exist, be empty, and be owned by the current user).
func Start(fsys billy.Filesystem, mountpoint string) (*Server, error) {
	if runtime.GOOS != "darwin" {
		return nil, fmt.Errorf("mounting is not supported on %s yet", runtime.GOOS)
	}
	nfs.Log.SetLevel(nfs.ErrorLevel)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	var rnd [16]byte
	rand.Read(rnd[:])
	s := &Server{listener: l, mountpoint: mountpoint, token: hex.EncodeToString(rnd[:]), done: make(chan error, 1)}
	h := nfshelper.NewCachingHandler(tokenHandler{Handler: nfshelper.NewNullAuthHandler(fsys), export: "/" + s.token}, 1<<16)
	go func() { s.done <- nfs.Serve(l, h) }()

	port := strconv.Itoa(l.Addr().(*net.TCPAddr).Port)
	opts := strings.Join([]string{
		"port=" + port, "mountport=" + port, "vers=3", "tcp",
		"nolocks", "locallocks", "noresvport", // no lockd/statd; unprivileged client port
		"soft", "timeo=10", "retrans=2", // fail fast instead of hanging if we die
		"actimeo=1", // see target changes quickly
		"nobrowse",  // keep it out of Finder's sidebar
	}, ",")
	out, err := exec.Command("/sbin/mount_nfs", "-o", opts, "localhost:/"+s.token, mountpoint).CombinedOutput()
	if err != nil {
		l.Close()
		return nil, fmt.Errorf("mount_nfs: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return s, nil
}

// Done is closed with the serve error when the server stops.
func (s *Server) Done() <-chan error { return s.done }

// Stop unmounts (forcing if needed) and stops serving.
func (s *Server) Stop() error {
	err := Unmount(s.mountpoint)
	s.listener.Close()
	return err
}

// Unmount unmounts mountpoint, forcing if a normal unmount fails.
func Unmount(mountpoint string) error {
	if !Mounted(mountpoint) {
		return nil
	}
	if exec.Command("/sbin/umount", mountpoint).Run() == nil {
		return nil
	}
	out, err := exec.Command("/sbin/umount", "-f", mountpoint).CombinedOutput()
	if err != nil {
		return fmt.Errorf("umount: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Mounted reports whether something is mounted at mountpoint.
func Mounted(mountpoint string) bool {
	out, err := exec.Command("/sbin/mount").Output()
	if err != nil {
		return false
	}
	real := mountpoint
	if r, err := filepath.EvalSymlinks(mountpoint); err == nil {
		real = r
	}
	for _, line := range strings.Split(string(out), "\n") {
		if i := strings.Index(line, " on "); i >= 0 {
			rest := line[i+4:]
			if j := strings.LastIndex(rest, " ("); j >= 0 {
				rest = rest[:j]
			}
			if rest == mountpoint || rest == real {
				return true
			}
		}
	}
	return false
}
