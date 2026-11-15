//go:build linux

package update

import (
	"os"

	"repairflow/internal/errs"

	"golang.org/x/sys/unix"
)

// lockInstallTarget serializes updater invocations for one executable. The OS
// releases the lock when the process exits, so a crash cannot leave a stale
// lock. Creating the lock file also checks that this process may replace the
// binary; updates to a root-owned installation must run with privilege.
func lockInstallTarget(target string) (func(), error) {
	f, err := os.OpenFile(target+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, errs.Wrap(errs.CodePermission, err,
			"cannot open update lock for %q; run the update with permission to replace the installed binary", target)
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, errs.Wrap(errs.CodeInternal, err, "cannot lock %q for update", target)
	}
	return func() {
		_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
		_ = f.Close()
	}, nil
}
