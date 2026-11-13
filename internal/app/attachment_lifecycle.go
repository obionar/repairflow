package app

import (
	"context"
	"io"
	"time"

	"repairflow/internal/errs"
	"repairflow/internal/files"
)

// Detach moves an attachment to the recoverable archive: hidden from normal
// views, bytes retained, purge_after set to at least now+PurgeGrace.
func (s *Service) Detach(ctx context.Context, actor string, id int64) (Attachment, error) {
	actor, err := normalizeActor(actor)
	if err != nil {
		return Attachment{}, err
	}
	ts := s.timestamp()
	purgeAfter := s.now().Add(PurgeGrace).Format(time.RFC3339)
	err = s.InTx(ctx, func(ctx context.Context) error {
		// The state check is part of the transaction: two concurrent detaches must
		// not both write an audit entry and a purge deadline.
		a, err := s.GetAttachment(ctx, id)
		if err != nil {
			return err
		}
		if a.State != "attached" {
			return errs.New(errs.CodeConflict, "attachment %d is %s, not attached", id, a.State)
		}
		if _, err := s.q(ctx).ExecContext(ctx,
			`UPDATE attachments SET state='detached', detached_at=?, purge_after=?, updated_at=? WHERE id=?`,
			ts, purgeAfter, ts, id); err != nil {
			return errs.Wrap(errs.CodeInternal, err, "cannot detach attachment")
		}
		_, err = s.q(ctx).ExecContext(ctx,
			`INSERT INTO repair_activity(repair_id, actor, action, detail, created_at) VALUES(?,?,?,?,?)`,
			a.RepairID, actor, "attachment_detach", detailJSON(map[string]any{"attachment_id": id, "purge_after": purgeAfter}), ts)
		return err
	})
	if err != nil {
		return Attachment{}, err
	}
	return s.GetAttachment(ctx, id)
}

// Restore returns a detached attachment to the repair.
func (s *Service) Restore(ctx context.Context, actor string, id int64) (Attachment, error) {
	actor, err := normalizeActor(actor)
	if err != nil {
		return Attachment{}, err
	}
	ts := s.timestamp()
	err = s.InTx(ctx, func(ctx context.Context) error {
		a, err := s.GetAttachment(ctx, id)
		if err != nil {
			return err
		}
		if a.State != "detached" {
			return errs.New(errs.CodeConflict, "attachment %d is %s, not detached", id, a.State)
		}
		if _, err := s.q(ctx).ExecContext(ctx,
			`UPDATE attachments SET state='attached', detached_at='', purge_after='', updated_at=? WHERE id=?`, ts, id); err != nil {
			return errs.Wrap(errs.CodeInternal, err, "cannot restore attachment")
		}
		_, err = s.q(ctx).ExecContext(ctx,
			`INSERT INTO repair_activity(repair_id, actor, action, detail, created_at) VALUES(?,?,?,?,?)`,
			a.RepairID, actor, "attachment_restore", detailJSON(map[string]any{"attachment_id": id}), ts)
		return err
	})
	if err != nil {
		return Attachment{}, err
	}
	return s.GetAttachment(ctx, id)
}

// Purge permanently destroys the bytes of a detached attachment after the
// grace period. The metadata row and audit trail survive as state
// 'purged'; this is the only destructive attachment operation and requires
// explicit confirmation. There is no automatic purge.
func (s *Service) Purge(ctx context.Context, actor string, id int64, confirm bool) (Attachment, error) {
	if inAmbientTx(ctx) {
		return Attachment{}, errs.New(errs.CodeValidation,
			"attachment purge must own its transaction; do not call it inside Service.InTx")
	}
	actor, err := normalizeActor(actor)
	if err != nil {
		return Attachment{}, err
	}
	if !confirm {
		return Attachment{}, errs.New(errs.CodeValidation, "purge permanently destroys file bytes and requires explicit confirmation")
	}
	store, err := s.requireStore()
	if err != nil {
		return Attachment{}, err
	}
	unlock, err := store.LockPurge(ctx)
	if err != nil {
		return Attachment{}, err
	}
	defer unlock()
	if err := s.recoverPendingDeletesLocked(ctx, store); err != nil {
		return Attachment{}, err
	}
	a, err := s.GetAttachment(ctx, id)
	if err != nil {
		return Attachment{}, err
	}
	if a.State != "detached" {
		return Attachment{}, errs.New(errs.CodeConflict, "only detached attachments can be purged (attachment %d is %s)", id, a.State)
	}
	if a.PurgeAfter == "" {
		return Attachment{}, errs.New(errs.CodeInternal, "attachment %d has no purge_after timestamp", id)
	}
	purgeAfter, err := time.Parse(time.RFC3339, a.PurgeAfter)
	if err != nil {
		return Attachment{}, errs.Wrap(errs.CodeInternal, err,
			"attachment %d has invalid purge_after metadata; refusing purge", id)
	}
	if s.now().Before(purgeAfter) {
		return Attachment{}, errs.New(errs.CodeConflict,
			"attachment %d is still within the %s retention grace (purge allowed after %s)", id, PurgeGrace, a.PurgeAfter)
	}

	ts := s.timestamp()
	var pending files.PendingDelete
	staged := false
	err = s.InTx(ctx, func(ctx context.Context) error {
		var repairID int64
		var storageName string
		if err := s.q(ctx).QueryRowContext(ctx,
			`SELECT repair_id, storage_name FROM attachments WHERE id = ? AND state = 'detached'`, id).Scan(&repairID, &storageName); err != nil {
			if isNoRows(err) {
				return errs.New(errs.CodeConflict, "attachment %d changed before purge; retry after reading it again", id)
			}
			return errs.Wrap(errs.CodeInternal, err, "cannot read attachment for purge")
		}
		var err error
		pending, err = store.StageDelete(repairID, storageName)
		if err != nil {
			return err
		}
		staged = true
		res, err := s.q(ctx).ExecContext(ctx,
			`UPDATE attachments SET state='purged', storage_name='', updated_at=? WHERE id=? AND state='detached'`, ts, id)
		if err != nil {
			return errs.Wrap(errs.CodeInternal, err, "cannot mark attachment purged")
		}
		n, err := res.RowsAffected()
		if err != nil {
			return errs.Wrap(errs.CodeInternal, err, "cannot read purge update count")
		}
		if n != 1 {
			return errs.New(errs.CodeConflict, "attachment %d changed before purge; retry after reading it again", id)
		}
		_, err = s.q(ctx).ExecContext(ctx,
			`INSERT INTO repair_activity(repair_id, actor, action, detail, created_at) VALUES(?,?,?,?,?)`,
			a.RepairID, actor, "attachment_purge", detailJSON(map[string]any{"attachment_id": id, "sha256": a.SHA256}), ts)
		return err
	})
	if err != nil {
		if staged {
			if restoreErr := store.RestoreDelete(pending); restoreErr != nil {
				return Attachment{}, errs.New(errs.CodeInternal,
					"purge failed (%v) AND restoring original bytes failed (%v); the recovery bytes remain in the workspace files/.purge area",
					err, restoreErr)
			}
		}
		return Attachment{}, err
	}
	// The database change is committed; a recovery-link cleanup failure must
	// not turn a successful purge into an error (or abort demo re-seeding).
	// The next service open finalizes any leftover link.
	_ = store.FinalizeDelete(pending)
	return s.GetAttachment(ctx, id)
}

// AttachmentStorageName returns the opaque on-disk name for byte operations
// (internal detail; not part of the machine-readable JSON contract).
func (s *Service) AttachmentStorageName(ctx context.Context, id int64) (string, error) {
	a, err := s.GetAttachment(ctx, id)
	if err != nil {
		return "", err
	}
	if a.storageName == "" {
		return "", errs.New(errs.CodeNotFound, "attachment %d has no stored bytes", id)
	}
	return a.storageName, nil
}

// OpenBytes returns the attachment's original bytes for retrieval (export /
// future web download), refusing purged or missing content.
func (s *Service) OpenBytes(ctx context.Context, id int64) (io.ReadCloser, Attachment, error) {
	store, err := s.requireStore()
	if err != nil {
		return nil, Attachment{}, err
	}
	a, err := s.GetAttachment(ctx, id)
	if err != nil {
		return nil, Attachment{}, err
	}
	if a.State == "purged" {
		return nil, Attachment{}, errs.New(errs.CodeNotFound, "attachment %d is purged; its bytes are gone", id)
	}
	f, err := store.Open(a.RepairID, a.storageName)
	if err != nil {
		return nil, Attachment{}, err
	}
	return f, a, nil
}
