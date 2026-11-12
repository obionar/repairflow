//go:build linux

package update

import (
	"os"
	"syscall"

	"repairflow/internal/errs"
)

// openArtifactSource opens the final path component without following a
// symlink and without blocking on a FIFO. The caller still fstats the returned
// descriptor and requires a regular file before reading any bytes.
func openArtifactSource(path string) (*os.File, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, errs.Wrap(errs.CodeValidation, err, "cannot read artifact %q", path)
	}
	return os.NewFile(uintptr(fd), path), nil
}
