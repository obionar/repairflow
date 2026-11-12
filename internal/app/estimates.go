package app

import (
	"context"
	"math"

	"repairflow/internal/errs"
)

// Versioned estimates keep accepted snapshots immutable. Each
// estimate is an immutable snapshot of the repair's used-part lines at
// creation time with a repair-level discount. Totals are computed once with
// the deterministic rounding rule and never recomputed from live data, so an
// accepted quote can never change retroactively.

// EstimateLine is one immutable line inside an estimate snapshot.
type EstimateLine struct {
	ID             int64  `json:"id"`
	EstimateID     int64  `json:"estimate_id"`
	Description    string `json:"description"`
	QuantityMilli  int64  `json:"quantity_milli"`
	Unit           string `json:"unit"`
	UnitPriceCents int64  `json:"unit_price_cents"`
	LineTotalCents int64  `json:"line_total_cents"`
	CreatedAt      string `json:"created_at"`
}

// Estimate is one versioned quote snapshot for a repair.
type Estimate struct {
	ID            int64          `json:"id"`
	RepairID      int64          `json:"repair_id"`
	Version       int            `json:"version"`
	Status        string         `json:"status"` // draft | accepted | superseded
	Note          string         `json:"note"`
	SubtotalCents int64          `json:"subtotal_cents"`
	DiscountKind  string         `json:"discount_kind"` // none | amount | percent
	DiscountValue int64          `json:"discount_value"`
	DiscountCents int64          `json:"discount_cents"`
	TotalCents    int64          `json:"total_cents"`
	Currency      string         `json:"currency"`
	CreatedBy     string         `json:"created_by"`
	CreatedAt     string         `json:"created_at"`
	Lines         []EstimateLine `json:"lines"`
}

// EstimateInput is the create payload for an estimate snapshot.
type EstimateInput struct {
	Note          string
	DiscountKind  string // none | amount | percent
	DiscountValue int64  // cents for amount, percent points for percent
}

const (
	estimateCols     = "id, repair_id, version, status, note, subtotal_cents, discount_kind, discount_value, discount_cents, total_cents, currency, created_by, created_at"
	estimateLineCols = "id, estimate_id, description, quantity_milli, unit, unit_price_cents, line_total_cents, created_at"
)

// CreateEstimate snapshots the repair's current non-deleted part lines into a
// new immutable estimate version. The snapshot requires at least one part
// line; discounts are repair-level.
func (s *Service) CreateEstimate(ctx context.Context, actor string, repairID int64, in EstimateInput) (Estimate, error) {
	actor, err := normalizeActor(actor)
	if err != nil {
		return Estimate{}, err
	}
	note, err := validateText("note", in.Note, 2000, false)
	if err != nil {
		return Estimate{}, err
	}
	kind := in.DiscountKind
	if kind == "" {
		kind = "none"
	}
	if kind == "percent" && (in.DiscountValue < 0 || in.DiscountValue > 100) {
		return Estimate{}, errs.New(errs.CodeValidation, "discount percent must be 0-100 (got %d)", in.DiscountValue)
	}
	if kind == "amount" && in.DiscountValue < 0 {
		return Estimate{}, errs.New(errs.CodeValidation, "discount amount must not be negative")
	}

	ts := s.timestamp()
	var out Estimate
	err = s.InTx(ctx, func(ctx context.Context) error {
		if _, err := s.GetRepairRow(ctx, repairID); err != nil {
			return err
		}
		parts, err := s.ListRepairParts(ctx, repairID, false)
		if err != nil {
			return err
		}
		if len(parts) == 0 {
			return errs.New(errs.CodeValidation, "estimate requires at least one active part line on repair %d", repairID)
		}

		subtotal := int64(0)
		type snap struct {
			desc  string
			qty   int64
			unit  string
			price int64
			total int64
		}
		snaps := make([]snap, 0, len(parts))
		for _, p := range parts {
			total := LineTotalCents(p.QuantityMilli, p.UnitPriceCents)
			if total > math.MaxInt64-subtotal {
				return errs.New(errs.CodeValidation, "estimate subtotal is out of range")
			}
			subtotal += total
			snaps = append(snaps, snap{p.Description, p.QuantityMilli, p.Unit, p.UnitPriceCents, total})
		}
		discount, err := DiscountCents(kind, in.DiscountValue, subtotal)
		if err != nil {
			return err
		}

		var version int
		if err := s.q(ctx).QueryRowContext(ctx,
			`SELECT coalesce(max(version), 0) + 1 FROM estimates WHERE repair_id = ?`, repairID).Scan(&version); err != nil {
			return errs.Wrap(errs.CodeInternal, err, "cannot determine estimate version")
		}

		err = s.q(ctx).QueryRowContext(ctx,
			`INSERT INTO estimates(repair_id, version, status, note, subtotal_cents, discount_kind, discount_value, discount_cents, total_cents, currency, created_by, created_at)
			 VALUES(?,?, 'draft', ?,?,?,?,?,?,?,?,?) RETURNING `+estimateCols,
			repairID, version, note, subtotal, kind, in.DiscountValue, discount, subtotal-discount, Currency, actor, ts).
			Scan(&out.ID, &out.RepairID, &out.Version, &out.Status, &out.Note, &out.SubtotalCents,
				&out.DiscountKind, &out.DiscountValue, &out.DiscountCents, &out.TotalCents,
				&out.Currency, &out.CreatedBy, &out.CreatedAt)
		if err != nil {
			return errs.Wrap(errs.CodeInternal, err, "cannot create estimate")
		}
		out.Lines = make([]EstimateLine, 0, len(snaps))
		for _, sn := range snaps {
			var l EstimateLine
			err := s.q(ctx).QueryRowContext(ctx,
				`INSERT INTO estimate_lines(estimate_id, description, quantity_milli, unit, unit_price_cents, line_total_cents, created_at)
				 VALUES(?,?,?,?,?,?,?) RETURNING `+estimateLineCols,
				out.ID, sn.desc, sn.qty, sn.unit, sn.price, sn.total, ts).
				Scan(&l.ID, &l.EstimateID, &l.Description, &l.QuantityMilli, &l.Unit, &l.UnitPriceCents, &l.LineTotalCents, &l.CreatedAt)
			if err != nil {
				return errs.Wrap(errs.CodeInternal, err, "cannot create estimate line")
			}
			out.Lines = append(out.Lines, l)
		}
		_, err = s.q(ctx).ExecContext(ctx,
			`INSERT INTO repair_activity(repair_id, actor, action, detail, created_at) VALUES(?,?,?,?,?)`,
			repairID, actor, "estimate_create", detailJSON(map[string]any{
				"estimate_id": out.ID, "version": out.Version, "total_cents": out.TotalCents,
			}), ts)
		if err != nil {
			return errs.Wrap(errs.CodeInternal, err, "cannot record repair activity")
		}
		return nil
	})
	if err != nil {
		return Estimate{}, err
	}
	return out, nil
}

// AcceptEstimate marks one estimate as the customer-agreed quote and
// supersedes any previously accepted version. Nothing is rewritten: older
// snapshots keep their rows and totals immutable.
func (s *Service) AcceptEstimate(ctx context.Context, actor string, estimateID int64) (Estimate, error) {
	actor, err := normalizeActor(actor)
	if err != nil {
		return Estimate{}, err
	}
	current, err := s.GetEstimate(ctx, estimateID)
	if err != nil {
		return Estimate{}, err
	}
	if current.Status == "accepted" {
		return Estimate{}, errs.New(errs.CodeConflict, "estimate %d (v%d) is already accepted", estimateID, current.Version)
	}

	ts := s.timestamp()
	err = s.InTx(ctx, func(ctx context.Context) error {
		if _, err := s.q(ctx).ExecContext(ctx,
			`UPDATE estimates SET status = 'superseded' WHERE repair_id = ? AND status = 'accepted'`, current.RepairID); err != nil {
			return errs.Wrap(errs.CodeInternal, err, "cannot supersede earlier accepted estimate")
		}
		if _, err := s.q(ctx).ExecContext(ctx,
			`UPDATE estimates SET status = 'accepted' WHERE id = ?`, estimateID); err != nil {
			return errs.Wrap(errs.CodeInternal, err, "cannot accept estimate")
		}
		_, err := s.q(ctx).ExecContext(ctx,
			`INSERT INTO repair_activity(repair_id, actor, action, detail, created_at) VALUES(?,?,?,?,?)`,
			current.RepairID, actor, "estimate_accept", detailJSON(map[string]any{
				"estimate_id": estimateID, "version": current.Version, "total_cents": current.TotalCents,
			}), ts)
		if err != nil {
			return errs.Wrap(errs.CodeInternal, err, "cannot record repair activity")
		}
		return nil
	})
	if err != nil {
		return Estimate{}, err
	}
	return s.GetEstimate(ctx, estimateID)
}

// GetEstimate reads one estimate snapshot with its lines.
func (s *Service) GetEstimate(ctx context.Context, estimateID int64) (Estimate, error) {
	var e Estimate
	err := s.q(ctx).QueryRowContext(ctx,
		`SELECT `+estimateCols+` FROM estimates WHERE id = ?`, estimateID).
		Scan(&e.ID, &e.RepairID, &e.Version, &e.Status, &e.Note, &e.SubtotalCents,
			&e.DiscountKind, &e.DiscountValue, &e.DiscountCents, &e.TotalCents,
			&e.Currency, &e.CreatedBy, &e.CreatedAt)
	if err != nil {
		if isNoRows(err) {
			return Estimate{}, notFound("estimate", estimateID)
		}
		return Estimate{}, errs.Wrap(errs.CodeInternal, err, "cannot read estimate %d", estimateID)
	}
	lines, err := s.listEstimateLines(ctx, estimateID)
	if err != nil {
		return Estimate{}, err
	}
	e.Lines = lines
	return e, nil
}

// ListEstimates returns all estimate versions of a repair, oldest first.
func (s *Service) ListEstimates(ctx context.Context, repairID int64) ([]Estimate, error) {
	if _, err := s.GetRepairRow(ctx, repairID); err != nil {
		return nil, err
	}
	rows, err := s.q(ctx).QueryContext(ctx,
		`SELECT `+estimateCols+` FROM estimates WHERE repair_id = ? ORDER BY version ASC`, repairID)
	if err != nil {
		return nil, errs.Wrap(errs.CodeInternal, err, "cannot list estimates")
	}
	defer func() { _ = rows.Close() }()
	out := []Estimate{}
	for rows.Next() {
		var e Estimate
		if err := rows.Scan(&e.ID, &e.RepairID, &e.Version, &e.Status, &e.Note, &e.SubtotalCents,
			&e.DiscountKind, &e.DiscountValue, &e.DiscountCents, &e.TotalCents,
			&e.Currency, &e.CreatedBy, &e.CreatedAt); err != nil {
			return nil, errs.Wrap(errs.CodeInternal, err, "cannot read estimate row")
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		lines, err := s.listEstimateLines(ctx, out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].Lines = lines
	}
	return out, nil
}

func (s *Service) listEstimateLines(ctx context.Context, estimateID int64) ([]EstimateLine, error) {
	rows, err := s.q(ctx).QueryContext(ctx,
		`SELECT `+estimateLineCols+` FROM estimate_lines WHERE estimate_id = ? ORDER BY id ASC`, estimateID)
	if err != nil {
		return nil, errs.Wrap(errs.CodeInternal, err, "cannot list estimate lines")
	}
	defer func() { _ = rows.Close() }()
	out := []EstimateLine{}
	for rows.Next() {
		var l EstimateLine
		if err := rows.Scan(&l.ID, &l.EstimateID, &l.Description, &l.QuantityMilli, &l.Unit, &l.UnitPriceCents, &l.LineTotalCents, &l.CreatedAt); err != nil {
			return nil, errs.Wrap(errs.CodeInternal, err, "cannot read estimate line")
		}
		out = append(out, l)
	}
	return out, rows.Err()
}
