package fusemount

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/hanwen/go-fuse/v2/fuse/nodefs"
	"github.com/hanwen/go-fuse/v2/fuse/pathfs"
)

// Server is a running FUSE mount.
type Server struct {
	srv        *fuse.Server
	mountpoint string
	done       chan error
}

// Start mounts f at mountpoint using fusermount3 (no root needed).
func Start(f Filesystem, mountpoint string) (*Server, error) {
	root := pathfs.NewPathNodeFs(newAdapter(f), nil).Root()
	opts := &nodefs.Options{
		// Short caches: targets and vaults can change underneath us.
		EntryTimeout: time.Second,
		AttrTimeout:  time.Second,
	}
	srv, _, err := nodefs.Mount(mountpoint, root, &fuse.MountOptions{
		FsName: "guise",
		Name:   "guise",
	}, opts)
	if err != nil {
		return nil, fmt.Errorf("fuse mount (is the fuse3 package installed?): %w", err)
	}
	s := &Server{srv: srv, mountpoint: mountpoint, done: make(chan error, 1)}
	go func() {
		srv.Serve()
		s.done <- nil
	}()
	if err := srv.WaitMount(); err != nil {
		srv.Unmount()
		return nil, err
	}
	return s, nil
}

// Done receives when the server stops (e.g. after an external unmount).
func (s *Server) Done() <-chan error { return s.done }

// Stop unmounts.
func (s *Server) Stop() error {
	if err := s.srv.Unmount(); err != nil {
		return Unmount(s.mountpoint)
	}
	return nil
}

// Mounted reports whether a filesystem is mounted at mountpoint.
func Mounted(mountpoint string) bool {
	f, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) > 4 && unescapeMount(fields[4]) == mountpoint {
			return true
		}
	}
	return false
}

// unescapeMount decodes the octal escapes /proc uses for spaces etc.
func unescapeMount(s string) string {
	return strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`).Replace(s)
}

// Unmount unmounts mountpoint, lazily if it is busy.
func Unmount(mountpoint string) error {
	if !Mounted(mountpoint) {
		return nil
	}
	for _, bin := range []string{"fusermount3", "fusermount"} {
		if _, err := exec.LookPath(bin); err != nil {
			continue
		}
		if exec.Command(bin, "-u", mountpoint).Run() == nil {
			return nil
		}
		out, err := exec.Command(bin, "-u", "-z", mountpoint).CombinedOutput()
		if err != nil {
			return fmt.Errorf("%s: %v: %s", bin, err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	return fmt.Errorf("fusermount3 not found; install the fuse3 package")
}
