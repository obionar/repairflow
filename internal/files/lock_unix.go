//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package files

import (
	"os"
	"syscall"
)

// tryLockPurgeFile takes a non-blocking exclusive lock on path. The returned
// file owns the lock until closed; the OS drops it if the process exits.
func tryLockPurgeFile(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o666)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if err == syscall.EWOULDBLOCK || err == syscall.EAGAIN {
			return nil, errPurgeLockBusy
		}
		return nil, err
	}
	return f, nil
}
