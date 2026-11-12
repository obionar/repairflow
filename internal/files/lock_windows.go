//go:build windows

package files

import (
	"os"
	"syscall"
)

// tryLockPurgeFile opens the lock with no sharing. Windows closes the handle
// (and releases the lock) if the process exits.
func tryLockPurgeFile(path string) (*os.File, error) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := syscall.CreateFile(p,
		syscall.GENERIC_READ|syscall.GENERIC_WRITE,
		0, // deny all sharing while the lock is held
		nil, syscall.OPEN_ALWAYS, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		// ERROR_SHARING_VIOLATION=32, ERROR_LOCK_VIOLATION=33.
		if err == syscall.Errno(32) || err == syscall.Errno(33) {
			return nil, errPurgeLockBusy
		}
		return nil, err
	}
	return os.NewFile(uintptr(h), path), nil
}
