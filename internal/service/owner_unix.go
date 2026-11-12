//go:build unix

package service

import (
	"os"
	"os/user"
	"strconv"
	"syscall"
)

// ownerUser returns "user:group" for the owner of dir, the account an OpenRC
// script hands the service over to. It is empty when the owner cannot be
// determined, in which case the script has no command_user line.
func ownerUser(dir string) string {
	info, err := os.Stat(dir)
	if err != nil {
		return ""
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return ""
	}
	uid := strconv.FormatUint(uint64(st.Uid), 10)
	name := uid
	if u, lerr := user.LookupId(uid); lerr == nil && u.Username != "" {
		name = u.Username
	}
	gid := strconv.FormatUint(uint64(st.Gid), 10)
	group := gid
	if g, lerr := user.LookupGroupId(gid); lerr == nil && g.Name != "" {
		group = g.Name
	}
	return name + ":" + group
}
