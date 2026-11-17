//go:build linux

package backup

import (
	"errors"

	"repairflow/internal/errs"

	"golang.org/x/sys/unix"
)

// publishNoReplace publishes temp as dest without overwriting an existing file.
// renameat2 is atomic on filesystems that implement RENAME_NOREPLACE. Some
// external filesystems do not; fall back to an atomic hard-link publication.
func publishNoReplace(temp, dest string) error {
	err := unix.Renameat2(unix.AT_FDCWD, temp, unix.AT_FDCWD, dest, unix.RENAME_NOREPLACE)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, unix.EEXIST):
		return errs.New(errs.CodeConflict, "refusing to overwrite existing %q", dest)
	case errors.Is(err, unix.EINVAL), errors.Is(err, unix.ENOSYS), errors.Is(err, unix.EOPNOTSUPP), errors.Is(err, unix.ENOTSUP):
		return publishByLink(temp, dest)
	default:
		return errs.Wrap(errs.CodeInternal, err, "cannot publish backup")
	}
}
