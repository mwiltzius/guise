package control

import (
	"io/fs"
	"net"
	"os"
	"syscall"
)

func umask(m int) int { return syscall.Umask(m) }

func ownedByMe(fi fs.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Getuid()
}

// peerIsMe checks the connecting process's uid, in addition to the socket's
// file permissions.
func peerIsMe(conn net.Conn) bool {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return false
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return false
	}
	me := false
	raw.Control(func(fd uintptr) {
		uid, _, err := getPeerUID(int(fd))
		me = err == nil && int(uid) == os.Getuid()
	})
	return me
}
