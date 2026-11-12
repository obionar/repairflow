package files

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"repairflow/internal/errs"
)

// PendingDelete identifies one attachment byte file that is staged for
// deletion. The bytes are removed from their live path immediately, but a
// same-filesystem hard link with a per-attempt name is kept until the database
// transaction commits. RestoreDelete puts the bytes back after a rollback;
// FinalizeDelete discards that exact recovery link after a successful commit.
//
// The hard link makes the delete reversible without copying up to 256 MiB.
// StageDelete refuses filesystems that cannot create it rather than proceeding
// with an unrecoverable delete.
type PendingDelete struct {
	RepairID    int64
	StorageName string
	BackupName  string
}

var pendingNameRe = regexp.MustCompile(`^([a-f0-9]{32})\.([a-f0-9]{32})\.bak$`)

// LockPurge serializes every destructive attachment delete and every recovery
// scan across processes. The OS releases the lock when the process exits, so a
// crash cannot leave a stale lock. ctx bounds the wait for another live
// process's purge/demo reset.
func (s *Store) LockPurge(ctx context.Context) (func(), error) {
	if err := os.MkdirAll(s.runDir(), 0o700); err != nil {
		return nil, errs.Wrap(errs.CodeInternal, err, "cannot create workspace run directory")
	}
	lockPath := filepath.Join(s.runDir(), "purge.lock")
	for {
		f, err := tryLockPurgeFile(lockPath)
		if err == nil {
			return func() { _ = f.Close() }, nil
		}
		if !errors.Is(err, errPurgeLockBusy) {
			return nil, errs.Wrap(errs.CodeInternal, err, "cannot lock attachment purge")
		}
		select {
		case <-ctx.Done():
			return nil, errs.Wrap(errs.CodeInternal, ctx.Err(), "waiting for attachment purge lock")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

var errPurgeLockBusy = errors.New("attachment purge lock is held by another process")

// StageDelete hard-links the live attachment bytes into the workspace recovery
// area under a unique name, then removes the live name. A missing live file is
// an error: recovery must resolve any staged delete before a new destructive
// operation, rather than silently treating an absent original as success.
func (s *Store) StageDelete(repairID int64, storageName string) (PendingDelete, error) {
	live, err := s.path(repairID, storageName)
	if err != nil {
		return PendingDelete{}, err
	}
	info, statErr := os.Lstat(live)
	if statErr != nil {
		if os.IsNotExist(statErr) {
			return PendingDelete{}, errs.New(errs.CodeInternal,
				"attachment bytes for %s are absent; recovery must run before a destructive operation", storageName)
		}
		return PendingDelete{}, errs.Wrap(errs.CodeInternal, statErr, "cannot inspect attachment file for purge")
	}
	if !info.Mode().IsRegular() {
		return PendingDelete{}, errs.New(errs.CodeInternal,
			"attachment storage %s is not a regular file; refusing purge", storageName)
	}
	token, err := randomStorageName()
	if err != nil {
		return PendingDelete{}, err
	}
	pd := PendingDelete{
		RepairID:    repairID,
		StorageName: storageName,
		BackupName:  storageName + "." + token + ".bak",
	}
	backup, err := s.pendingDeletePath(pd)
	if err != nil {
		return PendingDelete{}, err
	}
	if err := os.MkdirAll(filepath.Dir(backup), 0o700); err != nil {
		return PendingDelete{}, errs.Wrap(errs.CodeInternal, err, "cannot create purge recovery directory")
	}
	if err := os.Link(live, backup); err != nil {
		return PendingDelete{}, errs.Wrap(errs.CodeInternal, err,
			"cannot create purge recovery link (filesystem must support hard links)")
	}
	if err := os.Remove(live); err != nil {
		_ = os.Remove(backup) // remove only this attempt's link
		return PendingDelete{}, errs.Wrap(errs.CodeInternal, err, "cannot remove attachment file")
	}
	return pd, nil
}

// RestoreDelete returns the exact staged bytes to the live path. If the live
// path already exists, this attempt's recovery link is redundant and is
// discarded. If neither name exists, recovery fails loudly: reporting success
// would hide permanent byte loss.
func (s *Store) RestoreDelete(pd PendingDelete) error {
	live, err := s.path(pd.RepairID, pd.StorageName)
	if err != nil {
		return err
	}
	backup, err := s.pendingDeletePath(pd)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(backup); err != nil {
		if os.IsNotExist(err) {
			_, liveErr := os.Lstat(live)
			if liveErr == nil {
				return nil
			} else if os.IsNotExist(liveErr) {
				return errs.New(errs.CodeInternal,
					"attachment %d/%s has neither live bytes nor its recovery link", pd.RepairID, pd.StorageName)
			}
			return errs.Wrap(errs.CodeInternal, liveErr, "cannot inspect attachment restore path")
		}
		return errs.Wrap(errs.CodeInternal, err, "cannot inspect purge recovery link")
	}
	if _, err := os.Lstat(live); err == nil {
		if err := os.Remove(backup); err != nil {
			return errs.Wrap(errs.CodeInternal, err, "cannot discard redundant purge recovery link")
		}
		return nil
	} else if !os.IsNotExist(err) {
		return errs.Wrap(errs.CodeInternal, err, "cannot inspect attachment restore path")
	}
	if err := os.MkdirAll(filepath.Dir(live), 0o700); err != nil {
		return errs.Wrap(errs.CodeInternal, err, "cannot recreate attachment directory")
	}
	if err := os.Rename(backup, live); err != nil {
		return errs.Wrap(errs.CodeInternal, err, "cannot restore attachment bytes from purge recovery link")
	}
	return nil
}

// FinalizeDelete discards this attempt's recovery link after the metadata
// commit. The bytes are already unreachable through the live path.
func (s *Store) FinalizeDelete(pd PendingDelete) error {
	backup, err := s.pendingDeletePath(pd)
	if err != nil {
		return err
	}
	if err := os.Remove(backup); err != nil && !os.IsNotExist(err) {
		return errs.Wrap(errs.CodeInternal, err, "cannot remove purge recovery link")
	}
	_ = os.Remove(filepath.Dir(backup)) // empty only; safe no-op otherwise
	return nil
}

// ListPendingDeletes reports recovery links left by interrupted purge or demo
// reset operations. The application decides whether each belongs to a live
// row (restore) or a committed purge/deleted row (finalize).
func (s *Store) ListPendingDeletes() ([]PendingDelete, error) {
	root := s.purgeRoot()
	dirs, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return []PendingDelete{}, nil
		}
		return nil, errs.Wrap(errs.CodeInternal, err, "cannot read purge recovery directory")
	}
	out := []PendingDelete{}
	for _, dir := range dirs {
		if !dir.IsDir() {
			continue
		}
		repairID, err := strconv.ParseInt(dir.Name(), 10, 64)
		if err != nil || repairID <= 0 {
			continue
		}
		entries, err := os.ReadDir(filepath.Join(root, dir.Name()))
		if err != nil {
			return nil, errs.Wrap(errs.CodeInternal, err, "cannot read purge recovery queue")
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			match := pendingNameRe.FindStringSubmatch(entry.Name())
			if match == nil {
				continue
			}
			out = append(out, PendingDelete{
				RepairID: repairID, StorageName: match[1], BackupName: entry.Name(),
			})
		}
	}
	return out, nil
}

func (s *Store) pendingDeletePath(pd PendingDelete) (string, error) {
	if _, err := s.path(pd.RepairID, pd.StorageName); err != nil {
		return "", err
	}
	if !pendingNameRe.MatchString(pd.BackupName) || !strings.HasPrefix(pd.BackupName, pd.StorageName+".") {
		return "", errs.New(errs.CodeInternal, "invalid purge recovery link name %q", pd.BackupName)
	}
	return filepath.Join(s.purgeRoot(), strconv.FormatInt(pd.RepairID, 10), pd.BackupName), nil
}

func (s *Store) purgeRoot() string { return filepath.Join(s.home, "files", ".purge") }
