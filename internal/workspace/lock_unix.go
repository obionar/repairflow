//go:build unix

package workspace

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"repairflow/internal/errs"
)

// Lock holds a shared workspace writer lock, or an exclusive migration lock.
// The OS releases it when the process exits, so a crash cannot leave a stale
// lock. Context cancellation bounds a wait for another process.
func Lock(ctx context.Context, home string, exclusive bool) (func(), error) {
	dir := filepath.Join(home, "run")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, errs.Wrap(errs.CodeInternal, err, "cannot create workspace lock directory")
	}
	f, err := os.OpenFile(filepath.Join(dir, "workspace.lock"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, errs.Wrap(errs.CodeInternal, err, "cannot open workspace lock")
	}
	flag := syscall.LOCK_SH
	if exclusive {
		flag = syscall.LOCK_EX
	}
	for {
		err = syscall.Flock(int(f.Fd()), flag|syscall.LOCK_NB)
		if err == nil {
			return func() { _ = f.Close() }, nil
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN {
			_ = f.Close()
			return nil, errs.Wrap(errs.CodeInternal, err, "cannot lock workspace")
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, errs.Wrap(errs.CodeConflict, ctx.Err(),
				"workspace is busy; stop the service or wait for the other command to finish")
		case <-time.After(25 * time.Millisecond):
		}
	}
}
