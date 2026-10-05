package control

import "golang.org/x/sys/unix"

func getPeerUID(fd int) (uint32, uint32, error) {
	cred, err := unix.GetsockoptXucred(fd, unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
	if err != nil {
		return 0, 0, err
	}
	gid := uint32(0)
	if cred.Ngroups > 0 {
		gid = cred.Groups[0]
	}
	return cred.Uid, gid, nil
}
