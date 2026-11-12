package app

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"

	"repairflow/internal/errs"
)

const repairCols = "t.id, COALESCE(t.customer_id, 0), COALESCE(c.name, ''), t.device, t.device_type, t.brand, t.model, t.serial, t.issue, t.stage, t.notes, t.pickup_at, t.picked_up_by, t.pickup_notes, t.archived_at, t.created_at, t.updated_at"

type rowScanner interface {
	Scan(dest ...any) error
}

func scanRepair(row rowScanner) (Repair, error) {
	var t Repair
	if err := row.Scan(&t.ID, &t.CustomerID, &t.CustomerName, &t.Device, &t.DeviceType, &t.Brand, &t.Model,
		&t.Serial, &t.Issue, &t.Stage, &t.Notes, &t.PickupAt, &t.PickedUpBy, &t.PickupNotes,
		&t.ArchivedAt, &t.CreatedAt, &t.UpdatedAt); err != nil {
		return Repair{}, err
	}
	t.ReportedIssue = t.Issue
	t.Unassigned = t.CustomerID == 0
	t.Revision = repairRevision(t)
	return t, nil
}

func repairRevision(t Repair) string {
	return revision(t.DeviceType, t.Brand, t.Model, t.Serial, t.Issue, t.Stage, t.Notes)
}

func deviceDisplay(deviceType, brand, model string) string {
	if named := strings.TrimSpace(brand + " " + model); named != "" {
		return named
	}
	return deviceType
}

func nullableCustomerID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

func normalizeRepairInput(in RepairInput) (RepairInput, error) {
	if in.CustomerID < 0 {
		return RepairInput{}, errs.New(errs.CodeValidation, "customer_id must be zero (unassigned) or a positive id")
	}
	if strings.TrimSpace(in.Device) != "" && strings.TrimSpace(in.DeviceType) != "" &&
		strings.TrimSpace(in.Device) != strings.TrimSpace(in.DeviceType) {
		return RepairInput{}, errs.New(errs.CodeValidation, "device and device_type aliases must not differ")
	}
	if strings.TrimSpace(in.DeviceType) == "" {
		in.DeviceType = in.Device
	}
	var err error
	if in.DeviceType, err = validateText("device", in.DeviceType, maxDevice, true); err != nil {
		return RepairInput{}, err
	}
	if in.Brand, err = validateText("brand", in.Brand, 200, false); err != nil {
		return RepairInput{}, err
	}
	if in.Model, err = validateText("model", in.Model, 200, false); err != nil {
		return RepairInput{}, err
	}
	if in.Serial, err = validateText("serial", in.Serial, 200, false); err != nil {
		return RepairInput{}, err
	}
	if in.Issue, err = validateText("issue", in.Issue, maxIssue, true); err != nil {
		return RepairInput{}, err
	}
	if in.Notes, err = validateText("notes", in.Notes, maxNotes, false); err != nil {
		return RepairInput{}, err
	}
	in.Device = deviceDisplay(in.DeviceType, in.Brand, in.Model)
	return in, nil
}

// CreateRepair validates and stores a new repair in stage "new", recording
// the creation in the repair activity trail.
func (s *Service) CreateRepair(ctx context.Context, actor string, in RepairInput) (Repair, error) {
	actor, err := normalizeActor(actor)
	if err != nil {
		return Repair{}, err
	}
	in, err = normalizeRepairInput(in)
	if err != nil {
		return Repair{}, err
	}

	ts := s.timestamp()
	tx, commit, rollback, err := s.beginWrite(ctx)
	if err != nil {
		return Repair{}, err
	}
	defer rollback()

	var t Repair
	err = tx.QueryRowContext(ctx,
		`INSERT INTO repairs(customer_id, device, device_type, brand, model, serial, issue, stage, notes, created_at, updated_at)
		 VALUES(?,?,?,?,?,?,?,'new',?,?,?) RETURNING id`,
		nullableCustomerID(in.CustomerID), in.Device, in.DeviceType, in.Brand, in.Model, in.Serial, in.Issue, in.Notes, ts, ts).Scan(&t.ID)
	if err != nil {
		if isForeignKeyViolation(err) {
			return Repair{}, errs.Wrap(errs.CodeValidation, err, "customer %d does not exist", in.CustomerID)
		}
		return Repair{}, errs.Wrap(errs.CodeInternal, err, "cannot create repair")
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO repair_activity(repair_id, actor, action, detail, created_at) VALUES(?,?,?,?,?)`,
		t.ID, actor, "create", detailJSON(map[string]string{
			"device": in.Device, "device_type": in.DeviceType, "brand": in.Brand, "model": in.Model,
			"serial": in.Serial, "stage": "new",
		}), ts); err != nil {
		return Repair{}, errs.Wrap(errs.CodeInternal, err, "cannot record repair activity")
	}
	if err := commit(); err != nil {
		return Repair{}, errs.Wrap(errs.CodeInternal, err, "cannot commit repair")
	}
	return s.GetRepairRow(ctx, t.ID)
}

// GetRepairRow reads the repair record alone.
func (s *Service) GetRepairRow(ctx context.Context, id int64) (Repair, error) {
	t, err := scanRepair(s.q(ctx).QueryRowContext(ctx,
		`SELECT `+repairCols+` FROM repairs t LEFT JOIN customers c ON c.id = t.customer_id WHERE t.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Repair{}, notFound("repair", id)
	}
	if err != nil {
		return Repair{}, errs.Wrap(errs.CodeInternal, err, "cannot read repair %d", id)
	}
	return t, nil
}

// GetRepair reads one repair with its work log and activity trail. Soft-deleted
// entries are included only when includeDeleted is set (recoverable, audited).
func (s *Service) GetRepair(ctx context.Context, id int64, includeDeleted bool) (RepairDetail, error) {
	t, err := s.GetRepairRow(ctx, id)
	if err != nil {
		return RepairDetail{}, err
	}
	entries, err := s.listEntries(ctx, id, includeDeleted)
	if err != nil {
		return RepairDetail{}, err
	}
	activity, err := s.listActivity(ctx, id)
	if err != nil {
		return RepairDetail{}, err
	}
	return RepairDetail{Repair: t, Entries: entries, Activity: activity}, nil
}

// UpdateRepairDetails applies a patch under an optional stale-revision check.
func (s *Service) UpdateRepairDetails(ctx context.Context, actor string, id int64, patch RepairPatch, wantRevision string) (Repair, error) {
	actor, err := normalizeActor(actor)
	if err != nil {
		return Repair{}, err
	}
	current, err := s.GetRepairRow(ctx, id)
	if err != nil {
		return Repair{}, err
	}
	if err := checkRevision(wantRevision, current.Revision, "repair"); err != nil {
		return Repair{}, err
	}

	if patch.Device != nil && patch.DeviceType != nil &&
		strings.TrimSpace(*patch.Device) != strings.TrimSpace(*patch.DeviceType) {
		return Repair{}, errs.New(errs.CodeValidation, "device and device_type aliases must not differ")
	}
	deviceType, brand, model, serial := current.DeviceType, current.Brand, current.Model, current.Serial
	issue, notes := current.Issue, current.Notes
	customerID := current.CustomerID
	changes := map[string]map[string]string{}
	if patch.CustomerID != nil {
		if *patch.CustomerID < 0 {
			return Repair{}, errs.New(errs.CodeValidation, "customer_id must be zero (unassigned) or a positive id")
		}
		if *patch.CustomerID != customerID {
			changes["customer_id"] = map[string]string{
				"from": strconv.FormatInt(customerID, 10), "to": strconv.FormatInt(*patch.CustomerID, 10),
			}
			customerID = *patch.CustomerID
		}
	}
	set := func(field string, target *string, value *string, max int, required bool) error {
		if value == nil {
			return nil
		}
		cleaned, err := validateText(field, *value, max, required)
		if err != nil {
			return err
		}
		if cleaned != *target {
			changes[field] = map[string]string{"from": *target, "to": cleaned}
			*target = cleaned
		}
		return nil
	}
	if patch.DeviceType != nil {
		if err := set("device_type", &deviceType, patch.DeviceType, maxDevice, true); err != nil {
			return Repair{}, err
		}
	}
	if patch.Device != nil {
		if err := set("device", &deviceType, patch.Device, maxDevice, true); err != nil {
			return Repair{}, err
		}
	}
	for _, field := range []struct {
		name   string
		target *string
		value  *string
	}{
		{"brand", &brand, patch.Brand}, {"model", &model, patch.Model},
		{"serial", &serial, patch.Serial},
		{"issue", &issue, patch.Issue}, {"notes", &notes, patch.Notes},
	} {
		required := field.name == "issue"
		if err := set(field.name, field.target, field.value, maxIssue, required); err != nil {
			return Repair{}, err
		}
	}
	in, err := normalizeRepairInput(RepairInput{
		CustomerID: customerID, DeviceType: deviceType, Brand: brand, Model: model,
		Serial: serial, Issue: issue, Notes: notes,
	})
	if err != nil {
		return Repair{}, err
	}

	ts := s.timestamp()
	tx, commit, rollback, err := s.beginWrite(ctx)
	if err != nil {
		return Repair{}, err
	}
	defer rollback()

	if _, err := tx.ExecContext(ctx,
		`UPDATE repairs SET customer_id=?, device=?, device_type=?, brand=?, model=?, serial=?, issue=?, notes=?, updated_at=? WHERE id=?`,
		nullableCustomerID(in.CustomerID), in.Device, in.DeviceType, in.Brand, in.Model, in.Serial, in.Issue, in.Notes, ts, id); err != nil {
		if isForeignKeyViolation(err) {
			return Repair{}, errs.Wrap(errs.CodeValidation, err, "customer %d does not exist", in.CustomerID)
		}
		return Repair{}, errs.Wrap(errs.CodeInternal, err, "cannot update repair %d", id)
	}
	action := "update"
	if len(changes) == 0 {
		action = "update_noop"
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO repair_activity(repair_id, actor, action, detail, created_at) VALUES(?,?,?,?,?)`,
		id, actor, action, detailJSON(changes), ts); err != nil {
		return Repair{}, errs.Wrap(errs.CodeInternal, err, "cannot record repair activity")
	}
	if err := commit(); err != nil {
		return Repair{}, errs.Wrap(errs.CodeInternal, err, "cannot commit repair update")
	}
	return s.GetRepairRow(ctx, id)
}

// SetRepairStage validates and applies a permitted stage transition.
// Reopening a terminal stage requires explicit reopen=true; that intent is
// recorded in the activity trail.
func (s *Service) SetRepairStage(ctx context.Context, actor string, id int64, stage string, reopen bool) (Repair, error) {
	actor, err := normalizeActor(actor)
	if err != nil {
		return Repair{}, err
	}
	if !stageSet[stage] {
		return Repair{}, errs.New(errs.CodeValidation, "unknown stage %q (valid: %v)", stage, Stages)
	}
	current, err := s.GetRepairRow(ctx, id)
	if err != nil {
		return Repair{}, err
	}
	if stage == current.Stage {
		return Repair{}, errs.New(errs.CodeConflict, "repair %d is already in stage %q", id, stage)
	}
	allowed := transitions[current.Stage]
	if !contains(allowed, stage) {
		return Repair{}, errs.New(errs.CodeValidation,
			"stage transition %q -> %q is not allowed (from %q: %v); reopening a terminal stage requires --reopen",
			current.Stage, stage, current.Stage, allowed)
	}
	if terminalStages[current.Stage] && !reopen {
		return Repair{}, errs.New(errs.CodeValidation,
			"repair %d is in terminal stage %q; reopening to %q requires explicit --reopen", id, current.Stage, stage)
	}

	ts := s.timestamp()
	tx, commit, rollback, err := s.beginWrite(ctx)
	if err != nil {
		return Repair{}, err
	}
	defer rollback()

	if _, err := tx.ExecContext(ctx,
		`UPDATE repairs SET stage=?, updated_at=? WHERE id=?`, stage, ts, id); err != nil {
		return Repair{}, errs.Wrap(errs.CodeInternal, err, "cannot set repair stage")
	}
	action := "stage_change"
	if terminalStages[current.Stage] {
		action = "stage_reopen"
	}
	detail := map[string]any{"from": current.Stage, "to": stage}
	if reopen {
		detail["reopen"] = true
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO repair_activity(repair_id, actor, action, detail, created_at) VALUES(?,?,?,?,?)`,
		id, actor, action, detailJSON(detail), ts); err != nil {
		return Repair{}, errs.Wrap(errs.CodeInternal, err, "cannot record repair activity")
	}
	if err := commit(); err != nil {
		return Repair{}, errs.Wrap(errs.CodeInternal, err, "cannot commit stage change")
	}
	return s.GetRepairRow(ctx, id)
}

// AddRepairEntry appends one work-log entry to a repair.
func (s *Service) AddRepairEntry(ctx context.Context, actor string, id int64, kind, body string) (Entry, error) {
	actor, err := normalizeActor(actor)
	if err != nil {
		return Entry{}, err
	}
	if !entryKindSet[kind] {
		return Entry{}, errs.New(errs.CodeValidation, "unknown entry kind %q (valid: %v)", kind, EntryKinds)
	}
	body, err = validateText("body", body, maxEntryBody, true)
	if err != nil {
		return Entry{}, err
	}

	ts := s.timestamp()
	tx, commit, rollback, err := s.beginWrite(ctx)
	if err != nil {
		return Entry{}, err
	}
	defer rollback()

	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM repairs WHERE id = ?`, id).Scan(&exists); err != nil {
		return Entry{}, errs.Wrap(errs.CodeInternal, err, "cannot check repair %d", id)
	}
	if exists == 0 {
		return Entry{}, notFound("repair", id)
	}

	var e Entry
	err = tx.QueryRowContext(ctx,
		`INSERT INTO repair_entries(repair_id, kind, body, created_at) VALUES(?,?,?,?) RETURNING id`,
		id, kind, body, ts).Scan(&e.ID)
	if err != nil {
		return Entry{}, errs.Wrap(errs.CodeInternal, err, "cannot create entry")
	}
	e.RepairID, e.Kind, e.Body, e.CreatedAt = id, kind, body, ts

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO repair_activity(repair_id, actor, action, detail, created_at) VALUES(?,?,?,?,?)`,
		id, actor, "entry_add", detailJSON(map[string]any{"entry_id": e.ID, "kind": kind}), ts); err != nil {
		return Entry{}, errs.Wrap(errs.CodeInternal, err, "cannot record repair activity")
	}
	if err := commit(); err != nil {
		return Entry{}, errs.Wrap(errs.CodeInternal, err, "cannot commit entry")
	}
	return e, nil
}

// ListRepairs returns a stable page of repairs (id descending) with optional
// search, stage and customer filters.
func (s *Service) ListRepairs(ctx context.Context, f RepairFilter) (Page[Repair], error) {
	search, err := validateText("search", f.Search, maxSearch, false)
	if err != nil {
		return Page[Repair]{}, err
	}
	if f.Stage != "" && !stageSet[f.Stage] {
		return Page[Repair]{}, errs.New(errs.CodeValidation, "unknown stage %q (valid: %v)", f.Stage, Stages)
	}
	page, perPage := normalizePage(f.Page, f.PerPage)

	conds := []string{}
	args := []any{}
	if !f.IncludeArchived {
		conds = append(conds, "t.archived_at = ''")
	}
	if search != "" {
		conds = append(conds, `(t.device LIKE ? ESCAPE '\' OR t.device_type LIKE ? ESCAPE '\' OR t.brand LIKE ? ESCAPE '\' OR t.model LIKE ? ESCAPE '\' OR t.serial LIKE ? ESCAPE '\' OR t.issue LIKE ? ESCAPE '\' OR t.notes LIKE ? ESCAPE '\' OR c.name LIKE ? ESCAPE '\')`)
		p := likePattern(search)
		for range 8 {
			args = append(args, p)
		}
	}
	if f.Stage != "" {
		conds = append(conds, "t.stage = ?")
		args = append(args, f.Stage)
	}
	if f.CustomerID > 0 {
		conds = append(conds, "t.customer_id = ?")
		args = append(args, f.CustomerID)
	}
	if f.PickedUp != nil {
		if *f.PickedUp {
			conds = append(conds, "t.pickup_at <> ''")
		} else {
			conds = append(conds, "t.pickup_at = ''")
		}
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}

	var total int
	countSQL := `SELECT count(*) FROM repairs t LEFT JOIN customers c ON c.id = t.customer_id` + where
	if err := s.q(ctx).QueryRowContext(ctx, countSQL, args...).Scan(&total); err != nil {
		return Page[Repair]{}, errs.Wrap(errs.CodeInternal, err, "cannot count repairs")
	}

	rows, err := s.q(ctx).QueryContext(ctx,
		`SELECT `+repairCols+` FROM repairs t LEFT JOIN customers c ON c.id = t.customer_id`+where+
			` ORDER BY t.id DESC LIMIT ? OFFSET ?`,
		append(args, perPage, (page-1)*perPage)...)
	if err != nil {
		return Page[Repair]{}, errs.Wrap(errs.CodeInternal, err, "cannot list repairs")
	}
	defer func() { _ = rows.Close() }()

	items := []Repair{}
	for rows.Next() {
		t, err := scanRepair(rows)
		if err != nil {
			return Page[Repair]{}, errs.Wrap(errs.CodeInternal, err, "cannot read repair row")
		}
		items = append(items, t)
	}
	if err := rows.Err(); err != nil {
		return Page[Repair]{}, errs.Wrap(errs.CodeInternal, err, "cannot iterate repairs")
	}
	return Page[Repair]{Items: items, Page: page, PerPage: perPage, Total: total}, nil
}

func (s *Service) listEntries(ctx context.Context, repairID int64, includeDeleted bool) ([]Entry, error) {
	where := " AND deleted_at = ''"
	if includeDeleted {
		where = ""
	}
	rows, err := s.q(ctx).QueryContext(ctx,
		`SELECT id, repair_id, kind, body, created_at, deleted_at FROM repair_entries
		 WHERE repair_id = ?`+where+` ORDER BY id ASC`, repairID)
	if err != nil {
		return nil, errs.Wrap(errs.CodeInternal, err, "cannot read repair entries")
	}
	defer func() { _ = rows.Close() }()
	out := []Entry{}
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.ID, &e.RepairID, &e.Kind, &e.Body, &e.CreatedAt, &e.DeletedAt); err != nil {
			return nil, errs.Wrap(errs.CodeInternal, err, "cannot read entry row")
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Service) listActivity(ctx context.Context, repairID int64) ([]Activity, error) {
	rows, err := s.q(ctx).QueryContext(ctx,
		`SELECT id, repair_id, actor, action, detail, created_at FROM repair_activity
		 WHERE repair_id = ? ORDER BY id ASC`, repairID)
	if err != nil {
		return nil, errs.Wrap(errs.CodeInternal, err, "cannot read repair activity")
	}
	defer func() { _ = rows.Close() }()
	out := []Activity{}
	for rows.Next() {
		var a Activity
		if err := rows.Scan(&a.ID, &a.RepairID, &a.Actor, &a.Action, &a.Detail, &a.CreatedAt); err != nil {
			return nil, errs.Wrap(errs.CodeInternal, err, "cannot read activity row")
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func contains(items []string, want string) bool {
	for _, it := range items {
		if it == want {
			return true
		}
	}
	return false
}

// isForeignKeyViolation matches SQLite foreign-key constraint errors.
func isForeignKeyViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "foreign key") || strings.Contains(msg, "constraint failed")
}
