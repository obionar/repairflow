package app

import (
	"context"

	"repairflow/internal/errs"
)

// DashboardQueues is the read-only operational work queue. Performance
// metrics are deliberately outside this projection.
type DashboardQueues struct {
	OpenCount          int
	AwaitingPickup     int
	Workbench          []Repair
	ReadyForCollection []Repair
}

// Dashboard returns open work in workflow order and closed repairs awaiting
// collection. Archived records never enter either queue.
func (s *Service) Dashboard(ctx context.Context) (DashboardQueues, error) {
	var out DashboardQueues
	if err := s.q(ctx).QueryRowContext(ctx,
		`SELECT count(*) FROM repairs WHERE archived_at = '' AND stage NOT IN ('repaired','no_repair')`).
		Scan(&out.OpenCount); err != nil {
		return out, errs.Wrap(errs.CodeInternal, err, "cannot count open repairs")
	}
	if err := s.q(ctx).QueryRowContext(ctx,
		`SELECT count(*) FROM repairs WHERE archived_at = '' AND stage IN ('repaired','no_repair') AND pickup_at = ''`).
		Scan(&out.AwaitingPickup); err != nil {
		return out, errs.Wrap(errs.CodeInternal, err, "cannot count repairs awaiting pickup")
	}

	workbench, err := s.queryRepairs(ctx,
		`SELECT `+repairCols+` FROM repairs t LEFT JOIN customers c ON c.id = t.customer_id
		 WHERE t.archived_at = '' AND t.stage NOT IN ('repaired','no_repair')
		 ORDER BY CASE t.stage
		   WHEN 'in_progress' THEN 0 WHEN 'lab_test' THEN 1 WHEN 'customer_test' THEN 2
		   WHEN 'new' THEN 3 WHEN 'waiting_parts' THEN 4 ELSE 5 END,
		 t.created_at ASC, t.id ASC LIMIT 8`)
	if err != nil {
		return out, err
	}
	ready, err := s.queryRepairs(ctx,
		`SELECT `+repairCols+` FROM repairs t LEFT JOIN customers c ON c.id = t.customer_id
		 WHERE t.archived_at = '' AND t.stage IN ('repaired','no_repair') AND t.pickup_at = ''
		 ORDER BY t.updated_at ASC, t.id ASC`)
	if err != nil {
		return out, err
	}
	out.Workbench, out.ReadyForCollection = workbench, ready
	return out, nil
}

func (s *Service) queryRepairs(ctx context.Context, query string) ([]Repair, error) {
	rows, err := s.q(ctx).QueryContext(ctx, query)
	if err != nil {
		return nil, errs.Wrap(errs.CodeInternal, err, "cannot query dashboard repairs")
	}
	defer func() { _ = rows.Close() }()
	out := []Repair{}
	for rows.Next() {
		repair, err := scanRepair(rows)
		if err != nil {
			return nil, errs.Wrap(errs.CodeInternal, err, "cannot read dashboard repair")
		}
		out = append(out, repair)
	}
	if err := rows.Err(); err != nil {
		return nil, errs.Wrap(errs.CodeInternal, err, "cannot iterate dashboard repairs")
	}
	return out, nil
}
