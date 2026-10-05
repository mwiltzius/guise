package control

import "golang.org/x/sys/unix"

func getPeerUID(fd int) (uint32, uint32, error) {
	cred, err := unix.GetsockoptUcred(fd, unix.SOL_SOCKET, unix.SO_PEERCRED)
	if err != nil {
		return 0, 0, err
	}
	return cred.Uid, cred.Gid, nil
}
