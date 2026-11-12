package app

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"repairflow/internal/errs"
)

const customerCols = "id, name, phone, email, address, notes, archived_at, created_at, updated_at"

// CreateCustomer validates and stores a new customer, recording history.
func (s *Service) CreateCustomer(ctx context.Context, actor string, in CustomerInput) (Customer, error) {
	actor, err := normalizeActor(actor)
	if err != nil {
		return Customer{}, err
	}
	name, err := validateText("name", in.Name, maxName, true)
	if err != nil {
		return Customer{}, err
	}
	phone, err := validateText("phone", in.Phone, maxPhone, false)
	if err != nil {
		return Customer{}, err
	}
	email, err := validateText("email", in.Email, maxEmail, false)
	if err != nil {
		return Customer{}, err
	}
	address, err := validateText("address", in.Address, maxAddress, false)
	if err != nil {
		return Customer{}, err
	}
	notes, err := validateText("notes", in.Notes, maxNotes, false)
	if err != nil {
		return Customer{}, err
	}
	ts := s.timestamp()
	rev := revision(name, phone, email, address, notes)

	tx, commit, rollback, err := s.beginWrite(ctx)
	if err != nil {
		return Customer{}, err
	}
	defer rollback()

	var c Customer
	err = tx.QueryRowContext(ctx,
		`INSERT INTO customers(name, phone, email, address, notes, created_at, updated_at)
		 VALUES(?,?,?,?,?,?,?) RETURNING `+customerCols,
		name, phone, email, address, notes, ts, ts).
		Scan(&c.ID, &c.Name, &c.Phone, &c.Email, &c.Address, &c.Notes, &c.ArchivedAt, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return Customer{}, errs.Wrap(errs.CodeInternal, err, "cannot create customer")
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO customer_history(customer_id, actor, action, detail, created_at) VALUES(?,?,?,?,?)`,
		c.ID, actor, "create", detailJSON(map[string]string{"name": name}), ts); err != nil {
		return Customer{}, errs.Wrap(errs.CodeInternal, err, "cannot record customer history")
	}
	if err := commit(); err != nil {
		return Customer{}, errs.Wrap(errs.CodeInternal, err, "cannot commit customer")
	}
	c.Revision = rev
	return c, nil
}

// GetCustomer returns one customer or not_found.
func (s *Service) GetCustomer(ctx context.Context, id int64) (Customer, error) {
	var c Customer
	err := s.q(ctx).QueryRowContext(ctx,
		`SELECT `+customerCols+` FROM customers WHERE id = ?`, id).
		Scan(&c.ID, &c.Name, &c.Phone, &c.Email, &c.Address, &c.Notes, &c.ArchivedAt, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Customer{}, notFound("customer", id)
	}
	if err != nil {
		return Customer{}, errs.Wrap(errs.CodeInternal, err, "cannot read customer %d", id)
	}
	c.Revision = revision(c.Name, c.Phone, c.Email, c.Address, c.Notes)
	return c, nil
}

// UpdateCustomer applies a patch under an optional stale-revision check and
// records the exact changes in history.
func (s *Service) UpdateCustomer(ctx context.Context, actor string, id int64, patch CustomerPatch, wantRevision string) (Customer, error) {
	actor, err := normalizeActor(actor)
	if err != nil {
		return Customer{}, err
	}
	current, err := s.GetCustomer(ctx, id)
	if err != nil {
		return Customer{}, err
	}
	if err := checkRevision(wantRevision, current.Revision, "customer"); err != nil {
		return Customer{}, err
	}

	name, phone, email, address, notes := current.Name, current.Phone, current.Email, current.Address, current.Notes
	changes := map[string]map[string]string{}
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
	if err := set("name", &name, patch.Name, maxName, true); err != nil {
		return Customer{}, err
	}
	if err := set("phone", &phone, patch.Phone, maxPhone, false); err != nil {
		return Customer{}, err
	}
	if err := set("email", &email, patch.Email, maxEmail, false); err != nil {
		return Customer{}, err
	}
	if err := set("address", &address, patch.Address, maxAddress, false); err != nil {
		return Customer{}, err
	}
	if err := set("notes", &notes, patch.Notes, maxNotes, false); err != nil {
		return Customer{}, err
	}

	ts := s.timestamp()
	tx, commit, rollback, err := s.beginWrite(ctx)
	if err != nil {
		return Customer{}, err
	}
	defer rollback()

	if _, err := tx.ExecContext(ctx,
		`UPDATE customers SET name=?, phone=?, email=?, address=?, notes=?, updated_at=? WHERE id=?`,
		name, phone, email, address, notes, ts, id); err != nil {
		return Customer{}, errs.Wrap(errs.CodeInternal, err, "cannot update customer %d", id)
	}
	action := "update"
	if len(changes) == 0 {
		action = "update_noop"
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO customer_history(customer_id, actor, action, detail, created_at) VALUES(?,?,?,?,?)`,
		id, actor, action, detailJSON(changes), ts); err != nil {
		return Customer{}, errs.Wrap(errs.CodeInternal, err, "cannot record customer history")
	}
	if err := commit(); err != nil {
		return Customer{}, errs.Wrap(errs.CodeInternal, err, "cannot commit customer update")
	}
	return s.GetCustomer(ctx, id)
}

// ListCustomers returns a stable page of customers (id descending), with an
// optional substring search over name/phone/email.
func (s *Service) ListCustomers(ctx context.Context, f CustomerFilter) (Page[Customer], error) {
	search, err := validateText("search", f.Search, maxSearch, false)
	if err != nil {
		return Page[Customer]{}, err
	}
	page, perPage := normalizePage(f.Page, f.PerPage)

	conds := []string{}
	args := []any{}
	if !f.IncludeArchived {
		conds = append(conds, "archived_at = ''")
	}
	if search != "" {
		p := likePattern(search)
		conds = append(conds, `(name LIKE ? ESCAPE '\' OR phone LIKE ? ESCAPE '\' OR email LIKE ? ESCAPE '\' OR address LIKE ? ESCAPE '\')`)
		args = append(args, p, p, p, p)
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}

	var total int
	if err := s.q(ctx).QueryRowContext(ctx, `SELECT count(*) FROM customers`+where, args...).Scan(&total); err != nil {
		return Page[Customer]{}, errs.Wrap(errs.CodeInternal, err, "cannot count customers")
	}

	rows, err := s.q(ctx).QueryContext(ctx,
		`SELECT `+customerCols+` FROM customers`+where+` ORDER BY id DESC LIMIT ? OFFSET ?`,
		append(args, perPage, (page-1)*perPage)...)
	if err != nil {
		return Page[Customer]{}, errs.Wrap(errs.CodeInternal, err, "cannot list customers")
	}
	defer func() { _ = rows.Close() }()

	items := []Customer{}
	for rows.Next() {
		var c Customer
		if err := rows.Scan(&c.ID, &c.Name, &c.Phone, &c.Email, &c.Address, &c.Notes, &c.ArchivedAt, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return Page[Customer]{}, errs.Wrap(errs.CodeInternal, err, "cannot read customer row")
		}
		c.Revision = revision(c.Name, c.Phone, c.Email, c.Address, c.Notes)
		items = append(items, c)
	}
	if err := rows.Err(); err != nil {
		return Page[Customer]{}, errs.Wrap(errs.CodeInternal, err, "cannot iterate customers")
	}
	return Page[Customer]{Items: items, Page: page, PerPage: perPage, Total: total}, nil
}

// CustomerHistory returns the append-only audit trail of one customer.
func (s *Service) CustomerHistory(ctx context.Context, id int64) ([]CustomerHistory, error) {
	if _, err := s.GetCustomer(ctx, id); err != nil {
		return nil, err
	}
	rows, err := s.q(ctx).QueryContext(ctx,
		`SELECT id, customer_id, actor, action, detail, created_at
		 FROM customer_history WHERE customer_id = ? ORDER BY id ASC`, id)
	if err != nil {
		return nil, errs.Wrap(errs.CodeInternal, err, "cannot read customer history")
	}
	defer func() { _ = rows.Close() }()
	out := []CustomerHistory{}
	for rows.Next() {
		var h CustomerHistory
		if err := rows.Scan(&h.ID, &h.CustomerID, &h.Actor, &h.Action, &h.Detail, &h.CreatedAt); err != nil {
			return nil, errs.Wrap(errs.CodeInternal, err, "cannot read history row")
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// normalizePage bounds pagination inputs to a sane, documented range.
func normalizePage(page, perPage int) (int, int) {
	if page < 1 {
		page = 1
	}
	if perPage < 1 {
		perPage = 20
	}
	if perPage > 100 {
		perPage = 100
	}
	return page, perPage
}

// likePattern escapes LIKE metacharacters so user search text is literal.
func likePattern(search string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + r.Replace(search) + "%"
}
