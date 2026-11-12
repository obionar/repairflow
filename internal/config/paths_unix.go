//go:build unix

package config

import (
	"os"
	"syscall"

	"repairflow/internal/errs"
)

// fileLinks reports the hard-link count of an existing file, when knowable.
func fileLinks(info os.FileInfo) (uint64, bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return uint64(st.Nlink), true
}

// assertOwned refuses a workspace directory owned by another user.
func assertOwned(path string, info os.FileInfo) error {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	if int(st.Uid) != os.Geteuid() {
		return errs.New(errs.CodePermission, "refusing workspace %q: owned by uid %d, not the current user (uid %d)", path, st.Uid, os.Geteuid())
	}
	return nil
}
