// Package app is the application service layer: the single authority for
// business rules and writes. CLI and web adapters call these functions; they
// never touch SQL themselves. Domain rules and decisions are documented in
// docs/ARCHITECTURE.md.
package app

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"repairflow/internal/errs"
	"repairflow/internal/files"
)

// Service exposes the domain operations. Construct one per open database.
type Service struct {
	db    *sql.DB
	now   func() time.Time
	store *files.Store // bound via WithStore for attachment byte operations
}

// DB exposes the underlying handle for diagnostic queries (web auth audit
// tests); business code must use the service methods.
func (s *Service) DB() *sql.DB { return s.db }

// New builds a service over an open database handle.
func New(db *sql.DB) *Service {
	return &Service{db: db, now: func() time.Time { return time.Now().UTC() }}
}

func (s *Service) timestamp() string { return s.now().Format(time.RFC3339) }

// Stage vocabulary:
var (
	// Stages lists all repair stages in workflow order.
	Stages = []string{"new", "in_progress", "waiting_parts", "lab_test", "customer_test", "repaired", "no_repair"}
	// EntryKinds lists all work-log entry kinds.
	EntryKinds = []string{"work", "test", "contact", "handover", "note"}

	stageSet     = toSet(Stages)
	entryKindSet = toSet(EntryKinds)

	// terminalStages cannot transition without an explicit reopen.
	terminalStages = toSet([]string{"repaired", "no_repair"})

	// transitions lists allowed stage targets. Terminal stages only allow an
	// explicit reopen back to in_progress.
	transitions = map[string][]string{
		"new":           {"in_progress", "waiting_parts", "lab_test", "no_repair"},
		"in_progress":   {"waiting_parts", "lab_test", "customer_test", "repaired", "no_repair"},
		"waiting_parts": {"in_progress", "lab_test", "no_repair"},
		"lab_test":      {"in_progress", "waiting_parts", "customer_test", "repaired", "no_repair"},
		"customer_test": {"in_progress", "repaired", "no_repair"},
		"repaired":      {"in_progress"},
		"no_repair":     {"in_progress"},
	}
)

func toSet(items []string) map[string]bool {
	m := make(map[string]bool, len(items))
	for _, it := range items {
		m[it] = true
	}
	return m
}

// IsTerminalStage reports whether a stage is terminal.
func IsTerminalStage(stage string) bool { return terminalStages[stage] }

// Bounded input sizes (mirror the schema CHECKs so failures are clean errors,
// not constraint violations).
const (
	maxName      = 200
	maxPhone     = 100
	maxEmail     = 200
	maxAddress   = 300
	maxNotes     = 5000
	maxDevice    = 300
	maxIssue     = 5000
	maxEntryBody = 10000
	maxActor     = 100
	maxSearch    = 200
)

// normalizeActor trims and bounds the actor identity recorded in audit rows.
func normalizeActor(actor string) (string, error) {
	actor = strings.TrimSpace(actor)
	if actor == "" {
		actor = "operator"
	}
	if len(actor) > maxActor {
		return "", errs.New(errs.CodeValidation, "actor must be at most %d characters", maxActor)
	}
	return actor, nil
}

// validateText trims and bounds one text field, requiring non-blank when
// required.
func validateText(field, value string, max int, required bool) (string, error) {
	value = strings.TrimSpace(value)
	if required && value == "" {
		return "", errs.New(errs.CodeValidation, "%s must not be empty", field)
	}
	if len(value) > max {
		return "", errs.New(errs.CodeValidation, "%s must be at most %d characters (got %d)", field, max, len(value))
	}
	return value, nil
}

// revision derives the optimistic-concurrency revision from content, matching
// the proven Python approach: a form/agent carries the revision it read, and a
// stale write is refused instead of silently overwriting.
func revision(fields ...string) string {
	h := sha256.New()
	for _, f := range fields {
		h.Write([]byte(f))
		h.Write([]byte{0x1f})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// checkRevision refuses stale writes when the caller supplied a revision.
func checkRevision(want, got, entity string) error {
	if want == "" {
		return nil // explicit last-write-wins when caller omits the revision
	}
	if want != got {
		return errs.New(errs.CodeConflict, "stale %s revision: record changed since it was read (expected %s)", entity, want)
	}
	return nil
}

// detailJSON renders an audit detail object; encoding errors cannot occur for
// these input types, but never silently drop audit data.
func detailJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return `{"error":"unencodable audit detail"}`
	}
	return string(b)
}

// notFound wraps sql.ErrNoRows into the stable not_found code.
func notFound(entity string, id int64) error {
	return errs.Wrap(errs.CodeNotFound, errors.New("not found"), "%s %d does not exist", entity, id)
}

type txKey struct{}

// queryer abstracts *sql.DB and *sql.Tx.
type queryer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// q returns the ambient transaction if present, else the database handle.
func (s *Service) q(ctx context.Context) queryer {
	if tx, ok := ctx.Value(txKey{}).(*sql.Tx); ok {
		return tx
	}
	return s.db
}

// busyError reports SQLite's lock-wait timeout: another process held the write
// lock longer than busy_timeout. It is a retryable conflict, not an internal
// failure, and must not read as a bug.
func busyError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "SQLITE_BUSY")
}

// beginWrite starts a write transaction or joins the ambient one. rollback is
// safe to defer in both cases; commit only performs a real commit for the
// transaction this call owns.
func (s *Service) beginWrite(ctx context.Context) (queryer, func() error, func(), error) {
	if tx, ok := ctx.Value(txKey{}).(*sql.Tx); ok {
		return tx, func() error { return nil }, func() {}, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		if busyError(err) {
			return nil, nil, nil, errs.New(errs.CodeConflict, "workspace is busy: another process is writing; retry")
		}
		return nil, nil, nil, errs.Wrap(errs.CodeInternal, err, "cannot begin transaction")
	}
	return tx, tx.Commit, func() { _ = tx.Rollback() }, nil
}

// InTx runs fn as one atomic unit; nested service calls join its transaction.
func (s *Service) InTx(ctx context.Context, fn func(ctx context.Context) error) error {
	if _, ok := ctx.Value(txKey{}).(*sql.Tx); ok {
		return fn(ctx)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		if busyError(err) {
			return errs.New(errs.CodeConflict, "workspace is busy: another process is writing; retry")
		}
		return errs.Wrap(errs.CodeInternal, err, "cannot begin transaction")
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(context.WithValue(ctx, txKey{}, tx)); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return errs.Wrap(errs.CodeInternal, err, "cannot commit transaction")
	}
	return nil
}

// inAmbientTx reports whether the context already carries an outer
// transaction. Destructive filesystem operations must not finalize inside one:
// InTx returns nil for a joined transaction before the outer commit, so an
// outer rollback would restore metadata after its recovery bytes were deleted.
func inAmbientTx(ctx context.Context) bool {
	_, ok := ctx.Value(txKey{}).(*sql.Tx)
	return ok
}
