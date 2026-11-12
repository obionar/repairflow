package app

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"repairflow/internal/errs"
)

// This file implements the current workflow rules: in-place entry edits with immutable audit revisions,
// soft deletion with recovery, archive/hide for customers and repairs, and
// pickup tracking separate from the terminal stage.

const maxPickupBy = 200

// SetRepairPickup records (or updates) customer collection. Pickup is
// independent of the repair stage; every change is audited. Both the pickup
// time and who collected are required together.
func (s *Service) SetRepairPickup(ctx context.Context, actor string, id int64, at, by, notes string) (Repair, error) {
	actor, err := normalizeActor(actor)
	if err != nil {
		return Repair{}, err
	}
	at, err = normalizeTimestamp("pickup time", at)
	if err != nil {
		return Repair{}, err
	}
	by, err = validateText("picked up by", by, maxPickupBy, true)
	if err != nil {
		return Repair{}, err
	}
	notes, err = validateText("pickup notes", notes, maxNotes, false)
	if err != nil {
		return Repair{}, err
	}
	current, err := s.GetRepairRow(ctx, id)
	if err != nil {
		return Repair{}, err
	}

	ts := s.timestamp()
	q, commit, rollback, err := s.beginWrite(ctx)
	if err != nil {
		return Repair{}, err
	}
	defer rollback()
	if _, err := q.ExecContext(ctx,
		`UPDATE repairs SET pickup_at=?, picked_up_by=?, pickup_notes=?, updated_at=? WHERE id=?`,
		at, by, notes, ts, id); err != nil {
		return Repair{}, errs.Wrap(errs.CodeInternal, err, "cannot set pickup for repair %d", id)
	}
	detail := map[string]any{"at": at, "by": by, "notes": notes, "previous": map[string]string{
		"at": current.PickupAt, "by": current.PickedUpBy, "notes": current.PickupNotes,
	}}
	if _, err := q.ExecContext(ctx,
		`INSERT INTO repair_activity(repair_id, actor, action, detail, created_at) VALUES(?,?,?,?,?)`,
		id, actor, "pickup", detailJSON(detail), ts); err != nil {
		return Repair{}, errs.Wrap(errs.CodeInternal, err, "cannot record repair activity")
	}
	if err := commit(); err != nil {
		return Repair{}, errs.Wrap(errs.CodeInternal, err, "cannot commit pickup")
	}
	return s.GetRepairRow(ctx, id)
}

// EditRepairEntry edits an entry in place, storing the previous content as an
// immutable audit revision first: corrections do not create
// extra visible log entries; history is never physically deleted).
func (s *Service) EditRepairEntry(ctx context.Context, actor string, entryID int64, kind, body *string) (Entry, error) {
	actor, err := normalizeActor(actor)
	if err != nil {
		return Entry{}, err
	}
	current, err := s.getEntry(ctx, entryID)
	if err != nil {
		return Entry{}, err
	}
	if current.DeletedAt != "" {
		return Entry{}, errs.New(errs.CodeConflict, "entry %d is deleted; restore it before editing", entryID)
	}

	newKind, newBody := current.Kind, current.Body
	if kind != nil {
		if !entryKindSet[*kind] {
			return Entry{}, errs.New(errs.CodeValidation, "unknown entry kind %q (valid: %v)", *kind, EntryKinds)
		}
		newKind = *kind
	}
	if body != nil {
		if newBody, err = validateText("body", *body, maxEntryBody, true); err != nil {
			return Entry{}, err
		}
	}
	if newKind == current.Kind && newBody == current.Body {
		return Entry{}, errs.New(errs.CodeConflict, "entry %d is unchanged; nothing to edit", entryID)
	}

	ts := s.timestamp()
	q, commit, rollback, err := s.beginWrite(ctx)
	if err != nil {
		return Entry{}, err
	}
	defer rollback()
	if _, err := q.ExecContext(ctx,
		`INSERT INTO repair_entry_revisions(entry_id, actor, prev_kind, prev_body, prev_created_at, created_at)
		 VALUES(?,?,?,?,?,?)`,
		entryID, actor, current.Kind, current.Body, current.CreatedAt, ts); err != nil {
		return Entry{}, errs.Wrap(errs.CodeInternal, err, "cannot store entry revision")
	}
	if _, err := q.ExecContext(ctx,
		`UPDATE repair_entries SET kind=?, body=? WHERE id=?`, newKind, newBody, entryID); err != nil {
		return Entry{}, errs.Wrap(errs.CodeInternal, err, "cannot edit entry %d", entryID)
	}
	detail := map[string]any{"entry_id": entryID, "from": map[string]string{"kind": current.Kind, "body": current.Body}, "to": map[string]string{"kind": newKind, "body": newBody}}
	if _, err := q.ExecContext(ctx,
		`INSERT INTO repair_activity(repair_id, actor, action, detail, created_at) VALUES(?,?,?,?,?)`,
		current.RepairID, actor, "entry_edit", detailJSON(detail), ts); err != nil {
		return Entry{}, errs.Wrap(errs.CodeInternal, err, "cannot record repair activity")
	}
	if err := commit(); err != nil {
		return Entry{}, errs.Wrap(errs.CodeInternal, err, "cannot commit entry edit")
	}
	return s.getEntry(ctx, entryID)
}

// DeleteRepairEntry soft-deletes an entry: it disappears from normal views
// but stays recoverable (RestoreRepairEntry) and fully audited.
func (s *Service) DeleteRepairEntry(ctx context.Context, actor string, entryID int64) (Entry, error) {
	return s.setEntryDeleted(ctx, actor, entryID, true)
}

// RestoreRepairEntry recovers a soft-deleted entry.
func (s *Service) RestoreRepairEntry(ctx context.Context, actor string, entryID int64) (Entry, error) {
	return s.setEntryDeleted(ctx, actor, entryID, false)
}

func (s *Service) setEntryDeleted(ctx context.Context, actor string, entryID int64, deleted bool) (Entry, error) {
	actor, err := normalizeActor(actor)
	if err != nil {
		return Entry{}, err
	}
	current, err := s.getEntry(ctx, entryID)
	if err != nil {
		return Entry{}, err
	}
	if deleted == (current.DeletedAt != "") {
		return Entry{}, errs.New(errs.CodeConflict, "entry %d is already %s", entryID, map[bool]string{true: "deleted", false: "active"}[deleted])
	}

	ts := s.timestamp()
	value := ""
	action := "entry_restore"
	if deleted {
		value = ts
		action = "entry_delete"
	}
	q, commit, rollback, err := s.beginWrite(ctx)
	if err != nil {
		return Entry{}, err
	}
	defer rollback()
	if _, err := q.ExecContext(ctx,
		`UPDATE repair_entries SET deleted_at=? WHERE id=?`, value, entryID); err != nil {
		return Entry{}, errs.Wrap(errs.CodeInternal, err, "cannot update entry %d", entryID)
	}
	if _, err := q.ExecContext(ctx,
		`INSERT INTO repair_activity(repair_id, actor, action, detail, created_at) VALUES(?,?,?,?,?)`,
		current.RepairID, actor, action, detailJSON(map[string]any{"entry_id": entryID, "kind": current.Kind}), ts); err != nil {
		return Entry{}, errs.Wrap(errs.CodeInternal, err, "cannot record repair activity")
	}
	if err := commit(); err != nil {
		return Entry{}, errs.Wrap(errs.CodeInternal, err, "cannot commit entry state change")
	}
	return s.getEntry(ctx, entryID)
}

// ListEntryRevisions returns the immutable edit history of one entry.
func (s *Service) ListEntryRevisions(ctx context.Context, entryID int64) ([]EntryRevision, error) {
	if _, err := s.getEntry(ctx, entryID); err != nil {
		return nil, err
	}
	rows, err := s.q(ctx).QueryContext(ctx,
		`SELECT id, entry_id, actor, prev_kind, prev_body, prev_created_at, created_at
		 FROM repair_entry_revisions WHERE entry_id = ? ORDER BY id ASC`, entryID)
	if err != nil {
		return nil, errs.Wrap(errs.CodeInternal, err, "cannot read entry revisions")
	}
	defer func() { _ = rows.Close() }()
	out := []EntryRevision{}
	for rows.Next() {
		var r EntryRevision
		if err := rows.Scan(&r.ID, &r.EntryID, &r.Actor, &r.PrevKind, &r.PrevBody, &r.PrevCreatedAt, &r.CreatedAt); err != nil {
			return nil, errs.Wrap(errs.CodeInternal, err, "cannot read entry revision")
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ArchiveCustomer hides a customer (and shows nothing new); UnarchiveCustomer
// restores it. Deletion does not exist in v0.1.
func (s *Service) ArchiveCustomer(ctx context.Context, actor string, id int64, archived bool) (Customer, error) {
	actor, err := normalizeActor(actor)
	if err != nil {
		return Customer{}, err
	}
	current, err := s.GetCustomer(ctx, id)
	if err != nil {
		return Customer{}, err
	}
	if archived == (current.ArchivedAt != "") {
		return Customer{}, errs.New(errs.CodeConflict, "customer %d is already %s", id, map[bool]string{true: "archived", false: "active"}[archived])
	}
	ts := s.timestamp()
	value := ""
	action := "unarchive"
	if archived {
		value = ts
		action = "archive"
	}
	q, commit, rollback, err := s.beginWrite(ctx)
	if err != nil {
		return Customer{}, err
	}
	defer rollback()
	if _, err := q.ExecContext(ctx, `UPDATE customers SET archived_at=?, updated_at=? WHERE id=?`, value, ts, id); err != nil {
		return Customer{}, errs.Wrap(errs.CodeInternal, err, "cannot archive customer %d", id)
	}
	if _, err := q.ExecContext(ctx,
		`INSERT INTO customer_history(customer_id, actor, action, detail, created_at) VALUES(?,?,?,?,?)`,
		id, actor, action, detailJSON(map[string]string{"name": current.Name}), ts); err != nil {
		return Customer{}, errs.Wrap(errs.CodeInternal, err, "cannot record customer history")
	}
	if err := commit(); err != nil {
		return Customer{}, errs.Wrap(errs.CodeInternal, err, "cannot commit archive")
	}
	return s.GetCustomer(ctx, id)
}

// ArchiveRepair / UnarchiveRepair hide or restore a repair. Archived repairs
// and their history remain intact and auditable.
func (s *Service) ArchiveRepair(ctx context.Context, actor string, id int64, archived bool) (Repair, error) {
	actor, err := normalizeActor(actor)
	if err != nil {
		return Repair{}, err
	}
	current, err := s.GetRepairRow(ctx, id)
	if err != nil {
		return Repair{}, err
	}
	if archived == (current.ArchivedAt != "") {
		return Repair{}, errs.New(errs.CodeConflict, "repair %d is already %s", id, map[bool]string{true: "archived", false: "active"}[archived])
	}
	ts := s.timestamp()
	value := ""
	action := "unarchive"
	if archived {
		value = ts
		action = "archive"
	}
	q, commit, rollback, err := s.beginWrite(ctx)
	if err != nil {
		return Repair{}, err
	}
	defer rollback()
	if _, err := q.ExecContext(ctx, `UPDATE repairs SET archived_at=?, updated_at=? WHERE id=?`, value, ts, id); err != nil {
		return Repair{}, errs.Wrap(errs.CodeInternal, err, "cannot archive repair %d", id)
	}
	if _, err := q.ExecContext(ctx,
		`INSERT INTO repair_activity(repair_id, actor, action, detail, created_at) VALUES(?,?,?,?,?)`,
		id, actor, action, detailJSON(map[string]string{"device": current.Device}), ts); err != nil {
		return Repair{}, errs.Wrap(errs.CodeInternal, err, "cannot record repair activity")
	}
	if err := commit(); err != nil {
		return Repair{}, errs.Wrap(errs.CodeInternal, err, "cannot commit archive")
	}
	return s.GetRepairRow(ctx, id)
}

// getEntry reads one entry row of any state.
func (s *Service) getEntry(ctx context.Context, entryID int64) (Entry, error) {
	var e Entry
	err := s.q(ctx).QueryRowContext(ctx,
		`SELECT id, repair_id, kind, body, created_at, deleted_at FROM repair_entries WHERE id = ?`, entryID).
		Scan(&e.ID, &e.RepairID, &e.Kind, &e.Body, &e.CreatedAt, &e.DeletedAt)
	if err != nil {
		if isNoRows(err) {
			return Entry{}, notFound("entry", entryID)
		}
		return Entry{}, errs.Wrap(errs.CodeInternal, err, "cannot read entry %d", entryID)
	}
	return e, nil
}

// isNoRows reports sql.ErrNoRows regardless of wrapping.
func isNoRows(err error) bool { return errors.Is(err, sql.ErrNoRows) }

// normalizeTimestamp accepts RFC3339 or YYYY-MM-DD and stores RFC3339
// (date-only values become midnight UTC).
func normalizeTimestamp(field, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errs.New(errs.CodeValidation, "%s must not be empty", field)
	}
	if t, err := time.Parse(time.RFC3339, value); err == nil {
		return t.UTC().Format(time.RFC3339), nil
	}
	if t, err := time.Parse("2006-01-02", value); err == nil {
		return t.UTC().Format(time.RFC3339), nil
	}
	return "", errs.New(errs.CodeValidation, "%s must be RFC3339 or YYYY-MM-DD (got %q)", field, value)
}
