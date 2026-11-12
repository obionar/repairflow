package app

import (
	"context"
	"math"
	"strings"

	"repairflow/internal/errs"
)

// Repair parts are free-text lines with decimal quantities and integer-cent prices.
// Each line may include an optional acquisition cost and customer price. Lines
// are soft-deleted and audited; prices are snapshots that never change
// retroactively.

// Part is one used-part line on a repair.
type Part struct {
	ID             int64  `json:"id"`
	RepairID       int64  `json:"repair_id"`
	Description    string `json:"description"`
	QuantityMilli  int64  `json:"quantity_milli"`
	Unit           string `json:"unit"`
	UnitCostCents  *int64 `json:"unit_cost_cents"` // nil = unknown
	UnitPriceCents int64  `json:"unit_price_cents"`
	Currency       string `json:"currency"`
	Notes          string `json:"notes"`
	Revision       string `json:"revision"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
	DeletedAt      string `json:"deleted_at,omitempty"`
}

// PartInput is the create payload for a part line.
type PartInput struct {
	Description    string
	QuantityMilli  int64
	Unit           string
	UnitCostCents  *int64
	UnitPriceCents int64
	Notes          string
}

// PartPatch carries optional part updates; nil fields are unchanged.
type PartPatch struct {
	Description    *string
	QuantityMilli  *int64
	Unit           *string
	UnitCostCents  *int64
	ClearCost      bool
	UnitPriceCents *int64
	Notes          *string
}

const partCols = "id, repair_id, description, quantity_milli, unit, unit_cost_cents, unit_price_cents, notes, deleted_at, created_at, updated_at"

// AddRepairPart validates and stores one used-part line, auditing it.
func (s *Service) AddRepairPart(ctx context.Context, actor string, repairID int64, in PartInput) (Part, error) {
	actor, err := normalizeActor(actor)
	if err != nil {
		return Part{}, err
	}
	if repairID <= 0 {
		return Part{}, errs.New(errs.CodeValidation, "repair id must be positive")
	}
	p, err := validatePart(in.Description, in.QuantityMilli, in.Unit, in.UnitCostCents, in.UnitPriceCents, in.Notes)
	if err != nil {
		return Part{}, err
	}

	ts := s.timestamp()
	q, commit, rollback, err := s.beginWrite(ctx)
	if err != nil {
		return Part{}, err
	}
	defer rollback()
	var exists int
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM repairs WHERE id = ?`, repairID).Scan(&exists); err != nil {
		return Part{}, errs.Wrap(errs.CodeInternal, err, "cannot check repair %d", repairID)
	}
	if exists == 0 {
		return Part{}, notFound("repair", repairID)
	}

	var out Part
	err = q.QueryRowContext(ctx,
		`INSERT INTO repair_parts(repair_id, description, quantity_milli, unit, unit_cost_cents, unit_price_cents, notes, created_at, updated_at)
		 VALUES(?,?,?,?,?,?,?,?,?) RETURNING `+partCols,
		repairID, p.Description, p.QuantityMilli, p.Unit, p.UnitCostCents, p.UnitPriceCents, p.Notes, ts, ts).
		Scan(&out.ID, &out.RepairID, &out.Description, &out.QuantityMilli, &out.Unit, &out.UnitCostCents, &out.UnitPriceCents, &out.Notes, &out.DeletedAt, &out.CreatedAt, &out.UpdatedAt)
	if err != nil {
		return Part{}, errs.Wrap(errs.CodeInternal, err, "cannot create part line")
	}
	out.Currency = Currency
	out.Revision = partRevision(out)
	if _, err := q.ExecContext(ctx,
		`INSERT INTO repair_activity(repair_id, actor, action, detail, created_at) VALUES(?,?,?,?,?)`,
		repairID, actor, "part_add", detailJSON(map[string]any{"part_id": out.ID, "description": out.Description}), ts); err != nil {
		return Part{}, errs.Wrap(errs.CodeInternal, err, "cannot record repair activity")
	}
	if err := commit(); err != nil {
		return Part{}, errs.Wrap(errs.CodeInternal, err, "cannot commit part line")
	}
	return out, nil
}

// UpdateRepairPart patches a part line under an optional revision check;
// every change is audited and the price history is preserved in the audit
// detail (corrections never silently rewrite history).
func (s *Service) UpdateRepairPart(ctx context.Context, actor string, partID int64, patch PartPatch, wantRevision string) (Part, error) {
	actor, err := normalizeActor(actor)
	if err != nil {
		return Part{}, err
	}
	current, err := s.GetRepairPart(ctx, partID)
	if err != nil {
		return Part{}, err
	}
	if current.DeletedAt != "" {
		return Part{}, errs.New(errs.CodeConflict, "part %d is removed; restore it before editing", partID)
	}
	if err := checkRevision(wantRevision, current.Revision, "part"); err != nil {
		return Part{}, err
	}

	desc, qty, unit, cost, price, notes := current.Description, current.QuantityMilli, current.Unit, current.UnitCostCents, current.UnitPriceCents, current.Notes
	if patch.Description != nil {
		desc = *patch.Description
	}
	if patch.QuantityMilli != nil {
		qty = *patch.QuantityMilli
	}
	if patch.Unit != nil {
		unit = *patch.Unit
	}
	if patch.UnitCostCents != nil {
		cost = patch.UnitCostCents
	}
	if patch.ClearCost {
		cost = nil
	}
	if patch.UnitPriceCents != nil {
		price = *patch.UnitPriceCents
	}
	if patch.Notes != nil {
		notes = *patch.Notes
	}
	v, err := validatePart(desc, qty, unit, cost, price, notes)
	if err != nil {
		return Part{}, err
	}

	ts := s.timestamp()
	q, commit, rollback, err := s.beginWrite(ctx)
	if err != nil {
		return Part{}, err
	}
	defer rollback()
	if _, err := q.ExecContext(ctx,
		`UPDATE repair_parts SET description=?, quantity_milli=?, unit=?, unit_cost_cents=?, unit_price_cents=?, notes=?, updated_at=? WHERE id=?`,
		v.Description, v.QuantityMilli, v.Unit, v.UnitCostCents, v.UnitPriceCents, v.Notes, ts, partID); err != nil {
		return Part{}, errs.Wrap(errs.CodeInternal, err, "cannot update part %d", partID)
	}
	action := "part_update"
	detail := map[string]any{
		"part_id": partID,
		"from":    map[string]any{"description": current.Description, "quantity_milli": current.QuantityMilli, "unit": current.Unit, "unit_cost_cents": current.UnitCostCents, "unit_price_cents": current.UnitPriceCents},
		"to":      map[string]any{"description": v.Description, "quantity_milli": v.QuantityMilli, "unit": v.Unit, "unit_cost_cents": v.UnitCostCents, "unit_price_cents": v.UnitPriceCents},
	}
	if _, err := q.ExecContext(ctx,
		`INSERT INTO repair_activity(repair_id, actor, action, detail, created_at) VALUES(?,?,?,?,?)`,
		current.RepairID, actor, action, detailJSON(detail), ts); err != nil {
		return Part{}, errs.Wrap(errs.CodeInternal, err, "cannot record repair activity")
	}
	if err := commit(); err != nil {
		return Part{}, errs.Wrap(errs.CodeInternal, err, "cannot commit part update")
	}
	return s.GetRepairPart(ctx, partID)
}

// DeleteRepairPart soft-deletes a part line; RestoreRepairPart
// recovers it. Both are audited.
func (s *Service) DeleteRepairPart(ctx context.Context, actor string, partID int64) (Part, error) {
	return s.setPartDeleted(ctx, actor, partID, true)
}

// RestoreRepairPart recovers a soft-deleted part line.
func (s *Service) RestoreRepairPart(ctx context.Context, actor string, partID int64) (Part, error) {
	return s.setPartDeleted(ctx, actor, partID, false)
}

func (s *Service) setPartDeleted(ctx context.Context, actor string, partID int64, deleted bool) (Part, error) {
	actor, err := normalizeActor(actor)
	if err != nil {
		return Part{}, err
	}
	current, err := s.GetRepairPart(ctx, partID)
	if err != nil {
		return Part{}, err
	}
	if deleted == (current.DeletedAt != "") {
		return Part{}, errs.New(errs.CodeConflict, "part %d is already %s", partID, map[bool]string{true: "removed", false: "active"}[deleted])
	}
	ts := s.timestamp()
	value, action := "", "part_restore"
	if deleted {
		value, action = ts, "part_remove"
	}
	q, commit, rollback, err := s.beginWrite(ctx)
	if err != nil {
		return Part{}, err
	}
	defer rollback()
	if _, err := q.ExecContext(ctx, `UPDATE repair_parts SET deleted_at=?, updated_at=? WHERE id=?`, value, ts, partID); err != nil {
		return Part{}, errs.Wrap(errs.CodeInternal, err, "cannot update part %d", partID)
	}
	if _, err := q.ExecContext(ctx,
		`INSERT INTO repair_activity(repair_id, actor, action, detail, created_at) VALUES(?,?,?,?,?)`,
		current.RepairID, actor, action, detailJSON(map[string]any{"part_id": partID, "description": current.Description}), ts); err != nil {
		return Part{}, errs.Wrap(errs.CodeInternal, err, "cannot record repair activity")
	}
	if err := commit(); err != nil {
		return Part{}, errs.Wrap(errs.CodeInternal, err, "cannot commit part state change")
	}
	return s.GetRepairPart(ctx, partID)
}

// GetRepairPart reads one part line of any state.
func (s *Service) GetRepairPart(ctx context.Context, partID int64) (Part, error) {
	var p Part
	err := s.q(ctx).QueryRowContext(ctx,
		`SELECT `+partCols+` FROM repair_parts WHERE id = ?`, partID).
		Scan(&p.ID, &p.RepairID, &p.Description, &p.QuantityMilli, &p.Unit, &p.UnitCostCents, &p.UnitPriceCents, &p.Notes, &p.DeletedAt, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		if isNoRows(err) {
			return Part{}, notFound("part", partID)
		}
		return Part{}, errs.Wrap(errs.CodeInternal, err, "cannot read part %d", partID)
	}
	p.Currency = Currency
	p.Revision = partRevision(p)
	return p, nil
}

// ListRepairParts lists a repair's part lines (soft-deleted excluded unless
// requested), in stable id order.
func (s *Service) ListRepairParts(ctx context.Context, repairID int64, includeDeleted bool) ([]Part, error) {
	if _, err := s.GetRepairRow(ctx, repairID); err != nil {
		return nil, err
	}
	where := " AND deleted_at = ''"
	if includeDeleted {
		where = ""
	}
	rows, err := s.q(ctx).QueryContext(ctx,
		`SELECT `+partCols+` FROM repair_parts WHERE repair_id = ?`+where+` ORDER BY id ASC`, repairID)
	if err != nil {
		return nil, errs.Wrap(errs.CodeInternal, err, "cannot list parts")
	}
	defer func() { _ = rows.Close() }()
	out := []Part{}
	for rows.Next() {
		var p Part
		if err := rows.Scan(&p.ID, &p.RepairID, &p.Description, &p.QuantityMilli, &p.Unit, &p.UnitCostCents, &p.UnitPriceCents, &p.Notes, &p.DeletedAt, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, errs.Wrap(errs.CodeInternal, err, "cannot read part row")
		}
		p.Currency = Currency
		p.Revision = partRevision(p)
		out = append(out, p)
	}
	return out, rows.Err()
}

// validatePart normalizes and bounds one part line.
func validatePart(desc string, qtyMilli int64, unit string, cost *int64, price int64, notes string) (PartInput, error) {
	desc, err := validateText("description", desc, 300, true)
	if err != nil {
		return PartInput{}, err
	}
	if qtyMilli <= 0 {
		return PartInput{}, errs.New(errs.CodeValidation, "quantity must be greater than zero")
	}
	if qtyMilli > maxMilli {
		return PartInput{}, errs.New(errs.CodeValidation, "quantity is out of range")
	}
	unit = strings.TrimSpace(unit)
	if unit == "" {
		unit = UnitPieces
	}
	if len(unit) > 20 {
		return PartInput{}, errs.New(errs.CodeValidation, "unit must be at most 20 characters")
	}
	if cost != nil && *cost < 0 {
		return PartInput{}, errs.New(errs.CodeValidation, "unit cost must not be negative")
	}
	if cost != nil && *cost > maxCents {
		return PartInput{}, errs.New(errs.CodeValidation, "unit cost is out of range")
	}
	if price < 0 {
		return PartInput{}, errs.New(errs.CodeValidation, "unit price must not be negative")
	}
	if price > maxCents {
		return PartInput{}, errs.New(errs.CodeValidation, "unit price is out of range")
	}
	if price > 0 && qtyMilli > (math.MaxInt64-500)/price {
		return PartInput{}, errs.New(errs.CodeValidation, "quantity and unit price exceed the line-total range")
	}
	notes, err = validateText("notes", notes, 1000, false)
	if err != nil {
		return PartInput{}, err
	}
	return PartInput{Description: desc, QuantityMilli: qtyMilli, Unit: unit, UnitCostCents: cost, UnitPriceCents: price, Notes: notes}, nil
}

func partRevision(p Part) string {
	cost := "unknown"
	if p.UnitCostCents != nil {
		cost = FormatCents(*p.UnitCostCents)
	}
	return revision(p.Description, FormatQuantityMilli(p.QuantityMilli), p.Unit, cost, FormatCents(p.UnitPriceCents), p.Notes)
}
