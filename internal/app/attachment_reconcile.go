package app

import (
	"context"
	"strconv"

	"repairflow/internal/errs"
	"repairflow/internal/files"
)

// ReconcileAttachments verifies metadata<->bytes consistency for diagnostics:
// rows whose bytes are missing and orphan files without rows.
func (s *Service) ReconcileAttachments(ctx context.Context) (missing, orphans []string, err error) {
	store, err := s.requireStore()
	if err != nil {
		return nil, nil, err
	}
	rows, err := s.q(ctx).QueryContext(ctx,
		`SELECT repair_id, storage_name FROM attachments WHERE state != 'purged'`)
	if err != nil {
		return nil, nil, errs.Wrap(errs.CodeInternal, err, "cannot list attachment records")
	}
	type ref struct {
		repair int64
		name   string
	}
	known := map[string]bool{}
	var refs []ref
	for rows.Next() {
		var r ref
		if err := rows.Scan(&r.repair, &r.name); err != nil {
			_ = rows.Close()
			return nil, nil, errs.Wrap(errs.CodeInternal, err, "cannot read attachment record")
		}
		refs = append(refs, r)
		known[r.name] = true
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, nil, errs.Wrap(errs.CodeInternal, err, "cannot iterate attachment records")
	}
	if err := rows.Close(); err != nil {
		return nil, nil, errs.Wrap(errs.CodeInternal, err, "cannot close attachment records")
	}
	for _, r := range refs {
		if r.name == "" {
			missing = append(missing, formatRef(r.repair, r.name))
			continue
		}
		exists, err := store.Exists(r.repair, r.name)
		if err != nil {
			return nil, nil, err
		}
		if !exists {
			missing = append(missing, formatRef(r.repair, r.name))
		}
	}
	// Orphans: stored files that no live row references.
	repairRows, err := s.q(ctx).QueryContext(ctx, `SELECT id FROM repairs`)
	if err != nil {
		return nil, nil, errs.Wrap(errs.CodeInternal, err, "cannot list repairs")
	}
	var repairIDs []int64
	for repairRows.Next() {
		var id int64
		if err := repairRows.Scan(&id); err != nil {
			_ = repairRows.Close()
			return nil, nil, errs.Wrap(errs.CodeInternal, err, "cannot read repair id")
		}
		repairIDs = append(repairIDs, id)
	}
	if err := repairRows.Err(); err != nil {
		_ = repairRows.Close()
		return nil, nil, errs.Wrap(errs.CodeInternal, err, "cannot iterate repair ids")
	}
	if err := repairRows.Close(); err != nil {
		return nil, nil, errs.Wrap(errs.CodeInternal, err, "cannot close repair ids")
	}
	for _, tid := range repairIDs {
		names, err := store.List(tid)
		if err != nil {
			return nil, nil, err
		}
		for _, n := range names {
			if !known[n] {
				orphans = append(orphans, formatRef(tid, n))
			}
		}
	}
	return missing, orphans, nil
}

// RecoverPendingDeletes resolves recovery links left by a process crash during
// attachment purge or demo reset. A link whose attachment row still exists is
// restored to its live path; a link with no row (or a purged row) belongs to a
// committed delete and is finalized. It runs at service open before any caller
// can observe a half-finished purge.
func (s *Service) RecoverPendingDeletes(ctx context.Context) error {
	store, err := s.requireStore()
	if err != nil {
		return err
	}
	unlock, err := store.LockPurge(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	return s.recoverPendingDeletesLocked(ctx, store)
}

// recoverPendingDeletesLocked is the body of recovery for callers that already
// hold Store.LockPurge (open, doctor, backup, purge, demo clear).
func (s *Service) recoverPendingDeletesLocked(ctx context.Context, store *files.Store) error {
	pending, err := store.ListPendingDeletes()
	if err != nil {
		return err
	}
	for _, pd := range pending {
		var state string
		err := s.q(ctx).QueryRowContext(ctx,
			`SELECT state FROM attachments WHERE repair_id = ? AND storage_name = ?`,
			pd.RepairID, pd.StorageName).Scan(&state)
		switch {
		case err == nil && state != "purged":
			if err := store.RestoreDelete(pd); err != nil {
				return err
			}
		case err == nil || isNoRows(err):
			if err := store.FinalizeDelete(pd); err != nil {
				return err
			}
		default:
			return errs.Wrap(errs.CodeInternal, err, "cannot resolve interrupted attachment delete")
		}
	}
	return nil
}

func (s *Service) scanAttachment(ctx context.Context, query string, id int64) (Attachment, error) {
	a, err := scanAttachmentRow(s.q(ctx).QueryRowContext(ctx, query, id))
	if err != nil {
		if isNoRows(err) {
			return Attachment{}, notFound("attachment", id)
		}
		return Attachment{}, errs.Wrap(errs.CodeInternal, err, "cannot read attachment %d", id)
	}
	return a, nil
}

func scanAttachmentRow(rows interface {
	Scan(dest ...any) error
}) (Attachment, error) {
	var a Attachment
	var storageName string
	if err := rows.Scan(&a.ID, &a.RepairID, &a.DisplayName, &storageName, &a.MediaType, &a.SizeBytes, &a.SHA256,
		&a.Caption, &a.Source, &a.State, &a.DetachedAt, &a.PurgeAfter, &a.CreatedBy, &a.CreatedAt, &a.UpdatedAt); err != nil {
		return Attachment{}, err
	}
	a.storageName = storageName
	a.EntryIDs = []int64{}
	return a, nil
}

func (s *Service) attachmentEntryIDs(ctx context.Context, id int64) ([]int64, error) {
	rows, err := s.q(ctx).QueryContext(ctx,
		`SELECT entry_id FROM attachment_links WHERE attachment_id = ? ORDER BY entry_id ASC`, id)
	if err != nil {
		return nil, errs.Wrap(errs.CodeInternal, err, "cannot list attachment links")
	}
	defer func() { _ = rows.Close() }()
	out := []int64{}
	for rows.Next() {
		var entryID int64
		if err := rows.Scan(&entryID); err != nil {
			return nil, err
		}
		out = append(out, entryID)
	}
	return out, rows.Err()
}

func formatRef(repairID int64, name string) string {
	if name == "" {
		name = "<none>"
	}
	return "repair " + strconv.FormatInt(repairID, 10) + "/" + name
}
