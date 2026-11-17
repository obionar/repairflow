package app

import (
	"context"

	"repairflow/internal/errs"
)

// queryPage reads one stable page without loading the remaining rows.
func queryPage[T any](ctx context.Context, q queryer, page, perPage int, countSQL, listSQL string, args []any, scan func(rowScanner) (T, error)) (Page[T], error) {
	page, perPage = normalizePage(page, perPage)
	var total int
	if err := q.QueryRowContext(ctx, countSQL, args...).Scan(&total); err != nil {
		return Page[T]{}, errs.Wrap(errs.CodeInternal, err, "cannot count repair detail records")
	}
	listArgs := append(append([]any{}, args...), perPage, (page-1)*perPage)
	rows, err := q.QueryContext(ctx, listSQL, listArgs...)
	if err != nil {
		return Page[T]{}, errs.Wrap(errs.CodeInternal, err, "cannot list repair detail records")
	}
	defer func() { _ = rows.Close() }()
	items := []T{}
	for rows.Next() {
		item, err := scan(rows)
		if err != nil {
			return Page[T]{}, errs.Wrap(errs.CodeInternal, err, "cannot read repair detail record")
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return Page[T]{}, errs.Wrap(errs.CodeInternal, err, "cannot iterate repair detail records")
	}
	return Page[T]{Items: items, Page: page, PerPage: perPage, Total: total}, nil
}

// ListRepairEntriesPage returns one page of active and optionally deleted entries.
func (s *Service) ListRepairEntriesPage(ctx context.Context, repairID int64, includeDeleted bool, page, perPage int) (Page[Entry], error) {
	if _, err := s.GetRepairRow(ctx, repairID); err != nil {
		return Page[Entry]{}, err
	}
	where := " AND deleted_at = ''"
	if includeDeleted {
		where = ""
	}
	return queryPage(ctx, s.q(ctx), page, perPage,
		`SELECT count(*) FROM repair_entries WHERE repair_id = ?`+where,
		`SELECT id, repair_id, kind, body, created_at, deleted_at FROM repair_entries WHERE repair_id = ?`+where+` ORDER BY id ASC LIMIT ? OFFSET ?`,
		[]any{repairID}, func(row rowScanner) (Entry, error) {
			var entry Entry
			err := row.Scan(&entry.ID, &entry.RepairID, &entry.Kind, &entry.Body, &entry.CreatedAt, &entry.DeletedAt)
			return entry, err
		})
}

// ListRepairActivityPage returns one page of the repair's activity trail.
func (s *Service) ListRepairActivityPage(ctx context.Context, repairID int64, page, perPage int) (Page[Activity], error) {
	if _, err := s.GetRepairRow(ctx, repairID); err != nil {
		return Page[Activity]{}, err
	}
	return queryPage(ctx, s.q(ctx), page, perPage,
		`SELECT count(*) FROM repair_activity WHERE repair_id = ?`,
		`SELECT id, repair_id, actor, action, detail, created_at FROM repair_activity WHERE repair_id = ? ORDER BY id ASC LIMIT ? OFFSET ?`,
		[]any{repairID}, func(row rowScanner) (Activity, error) {
			var activity Activity
			err := row.Scan(&activity.ID, &activity.RepairID, &activity.Actor, &activity.Action, &activity.Detail, &activity.CreatedAt)
			return activity, err
		})
}

// ListRepairPartsPage returns one page of active and optionally removed parts.
func (s *Service) ListRepairPartsPage(ctx context.Context, repairID int64, includeDeleted bool, page, perPage int) (Page[Part], error) {
	if _, err := s.GetRepairRow(ctx, repairID); err != nil {
		return Page[Part]{}, err
	}
	where := " AND deleted_at = ''"
	if includeDeleted {
		where = ""
	}
	return queryPage(ctx, s.q(ctx), page, perPage,
		`SELECT count(*) FROM repair_parts WHERE repair_id = ?`+where,
		`SELECT `+partCols+` FROM repair_parts WHERE repair_id = ?`+where+` ORDER BY id ASC LIMIT ? OFFSET ?`,
		[]any{repairID}, func(row rowScanner) (Part, error) {
			var part Part
			err := row.Scan(&part.ID, &part.RepairID, &part.Description, &part.QuantityMilli, &part.Unit, &part.UnitCostCents, &part.UnitPriceCents, &part.Notes, &part.DeletedAt, &part.CreatedAt, &part.UpdatedAt)
			part.Currency = Currency
			part.Revision = partRevision(part)
			return part, err
		})
}

// ListRepairAttachmentsPage returns one page of visible gallery records. Purged
// metadata is retained in the database but has no gallery bytes to manage.
func (s *Service) ListRepairAttachmentsPage(ctx context.Context, repairID int64, includeDetached bool, page, perPage int) (Page[Attachment], error) {
	if _, err := s.GetRepairRow(ctx, repairID); err != nil {
		return Page[Attachment]{}, err
	}
	where := " AND state = 'attached'"
	if includeDetached {
		where = " AND state != 'purged'"
	}
	result, err := queryPage(ctx, s.q(ctx), page, perPage,
		`SELECT count(*) FROM attachments WHERE repair_id = ?`+where,
		`SELECT `+attachmentCols+` FROM attachments WHERE repair_id = ?`+where+` ORDER BY id ASC LIMIT ? OFFSET ?`,
		[]any{repairID}, func(row rowScanner) (Attachment, error) {
			return scanAttachmentRow(row)
		})
	if err != nil {
		return Page[Attachment]{}, err
	}
	ids := make([]int64, len(result.Items))
	for i := range result.Items {
		ids[i] = result.Items[i].ID
	}
	links, err := s.repairAttachmentEntryIDs(ctx, repairID, ids)
	if err != nil {
		return Page[Attachment]{}, err
	}
	for i := range result.Items {
		result.Items[i].EntryIDs = links[result.Items[i].ID]
		if result.Items[i].EntryIDs == nil {
			result.Items[i].EntryIDs = []int64{}
		}
		setPreview(&result.Items[i])
	}
	return result, nil
}
