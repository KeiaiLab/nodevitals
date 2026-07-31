//go:build linux

package nodecompat

import "golang.org/x/sys/unix"

// realUname calls uname(2). The fields are NUL-padded fixed arrays, so each is
// trimmed at the first NUL rather than converted whole.
func realUname() (unameInfo, error) {
	var u unix.Utsname
	if err := unix.Uname(&u); err != nil {
		return unameInfo{}, err
	}
	return unameInfo{
		Sysname:    nulString(u.Sysname[:]),
		Nodename:   nulString(u.Nodename[:]),
		Release:    nulString(u.Release[:]),
		Version:    nulString(u.Version[:]),
		Machine:    nulString(u.Machine[:]),
		Domainname: nulString(u.Domainname[:]),
	}, nil
}
