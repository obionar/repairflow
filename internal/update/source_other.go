//go:build !linux

package update

import (
	"os"

	"repairflow/internal/errs"
)

// openArtifactSource rejects a symlink or other non-regular final component
// before opening it, then the caller fstats the descriptor again. Linux uses
// O_NOFOLLOW/O_NONBLOCK for the stronger race and FIFO guarantees.
func openArtifactSource(path string) (*os.File, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, errs.Wrap(errs.CodeValidation, err, "cannot read artifact %q", path)
	}
	if !info.Mode().IsRegular() {
		return nil, errs.New(errs.CodeValidation, "artifact %q is not a regular file", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, errs.Wrap(errs.CodeValidation, err, "cannot read artifact %q", path)
	}
	return f, nil
}
