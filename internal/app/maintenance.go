package app

import (
	"context"
	"database/sql"
	"errors"

	"repairflow/internal/errs"
	"repairflow/internal/files"
)

// Counts reports business-record totals (used by status/doctor/seed guards).
func (s *Service) Counts(ctx context.Context) (customers, repairs int, err error) {
	if err = s.q(ctx).QueryRowContext(ctx, `SELECT count(*) FROM customers`).Scan(&customers); err != nil {
		return 0, 0, errs.Wrap(errs.CodeInternal, err, "cannot count customers")
	}
	if err = s.q(ctx).QueryRowContext(ctx, `SELECT count(*) FROM repairs`).Scan(&repairs); err != nil {
		return 0, 0, errs.Wrap(errs.CodeInternal, err, "cannot count repairs")
	}
	return customers, repairs, nil
}

// BusinessCounts reports record totals used by demo provenance guards.
func (s *Service) BusinessCounts(ctx context.Context) (customers, repairs, entries, parts, estimates, attachments int, err error) {
	if err = s.q(ctx).QueryRowContext(ctx, `SELECT count(*) FROM customers`).Scan(&customers); err != nil {
		return 0, 0, 0, 0, 0, 0, errs.Wrap(errs.CodeInternal, err, "cannot count customers")
	}
	if err = s.q(ctx).QueryRowContext(ctx, `SELECT count(*) FROM repairs`).Scan(&repairs); err != nil {
		return 0, 0, 0, 0, 0, 0, errs.Wrap(errs.CodeInternal, err, "cannot count repairs")
	}
	if err = s.q(ctx).QueryRowContext(ctx, `SELECT count(*) FROM repair_entries`).Scan(&entries); err != nil {
		return 0, 0, 0, 0, 0, 0, errs.Wrap(errs.CodeInternal, err, "cannot count entries")
	}
	if err = s.q(ctx).QueryRowContext(ctx, `SELECT count(*) FROM repair_parts`).Scan(&parts); err != nil {
		return 0, 0, 0, 0, 0, 0, errs.Wrap(errs.CodeInternal, err, "cannot count parts")
	}
	if err = s.q(ctx).QueryRowContext(ctx, `SELECT count(*) FROM estimates`).Scan(&estimates); err != nil {
		return 0, 0, 0, 0, 0, 0, errs.Wrap(errs.CodeInternal, err, "cannot count estimates")
	}
	if err = s.q(ctx).QueryRowContext(ctx, `SELECT count(*) FROM attachments`).Scan(&attachments); err != nil {
		return 0, 0, 0, 0, 0, 0, errs.Wrap(errs.CodeInternal, err, "cannot count attachments")
	}
	return customers, repairs, entries, parts, estimates, attachments, nil
}

// ClearBusinessRecords deletes ALL business records and their attachment
// bytes as one recoverable operation. This is the destructive core of the
// verified demo-reset path and must only be called
// behind a provenance guard; users and sessions are intentionally preserved.
//
// Filesystem deletes are staged as hard-link recovery entries before the SQL
// transaction. A metadata failure restores every original byte; only after a
// successful commit are the recovery links finalized.
func (s *Service) ClearBusinessRecords(ctx context.Context) error {
	if inAmbientTx(ctx) {
		return errs.New(errs.CodeValidation,
			"demo reset/clear must own its transaction; do not call it inside Service.InTx")
	}
	store, err := s.requireStore()
	if err != nil {
		return err
	}
	unlock, err := store.LockPurge(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	if err := s.recoverPendingDeletesLocked(ctx, store); err != nil {
		return err
	}
	pending := []files.PendingDelete{}
	err = s.InTx(ctx, func(ctx context.Context) error {
		rows, err := s.q(ctx).QueryContext(ctx,
			`SELECT repair_id, storage_name FROM attachments WHERE storage_name != ''`)
		if err != nil {
			return errs.Wrap(errs.CodeInternal, err, "cannot list attachment bytes")
		}
		type ref struct {
			repair int64
			name   string
		}
		var refs []ref
		for rows.Next() {
			var r ref
			if err := rows.Scan(&r.repair, &r.name); err != nil {
				_ = rows.Close()
				return errs.Wrap(errs.CodeInternal, err, "cannot read attachment byte reference")
			}
			refs = append(refs, r)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return errs.Wrap(errs.CodeInternal, err, "cannot iterate attachment byte references")
		}
		if err := rows.Close(); err != nil {
			return errs.Wrap(errs.CodeInternal, err, "cannot close attachment byte references")
		}
		for _, r := range refs {
			pd, err := store.StageDelete(r.repair, r.name)
			if err != nil {
				return err
			}
			pending = append(pending, pd)
		}
		for _, table := range []string{
			"attachment_links", "attachments", "repair_entry_revisions", "repair_entries",
			"repair_parts", "estimate_lines", "estimates", "repair_activity", "repairs",
			"customer_history", "customers", "seed_markers",
		} {
			if _, err := s.q(ctx).ExecContext(ctx, `DELETE FROM `+table); err != nil {
				return errs.Wrap(errs.CodeInternal, err, "cannot clear %s", table)
			}
		}
		return nil
	})
	if err != nil {
		var restoreErr error
		for i := len(pending) - 1; i >= 0; i-- {
			if rerr := store.RestoreDelete(pending[i]); rerr != nil && restoreErr == nil {
				restoreErr = rerr
			}
		}
		if restoreErr != nil {
			return errs.New(errs.CodeInternal,
				"clear failed (%v) AND restoring attachment bytes failed (%v); recovery links remain in the workspace files/.purge area",
				err, restoreErr)
		}
		return err
	}
	// The destructive DB change committed. Recovery-link cleanup is
	// best-effort and must not abort demo re-seeding; the next open finalizes
	// any leftover links.
	for _, pd := range pending {
		_ = store.FinalizeDelete(pd)
	}
	return nil
}

// SeedMarker reports whether a seed marker exists and its checksum.
func (s *Service) SeedMarker(ctx context.Context, key string) (bool, string, error) {
	var checksum string
	err := s.q(ctx).QueryRowContext(ctx, `SELECT checksum FROM seed_markers WHERE key = ?`, key).Scan(&checksum)
	if errors.Is(err, sql.ErrNoRows) {
		return false, "", nil
	}
	if err != nil {
		return false, "", errs.Wrap(errs.CodeInternal, err, "cannot read seed marker %q", key)
	}
	return true, checksum, nil
}

// PutSeedMarker records a completed seed run.
func (s *Service) PutSeedMarker(ctx context.Context, key, checksum string) error {
	ts := s.timestamp()
	if _, err := s.q(ctx).ExecContext(ctx,
		`INSERT INTO seed_markers(key, checksum, created_at) VALUES(?,?,?)`, key, checksum, ts); err != nil {
		return errs.Wrap(errs.CodeInternal, err, "cannot write seed marker %q", key)
	}
	return nil
}
