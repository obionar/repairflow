// Package db is the SQLite adapter: opening, pragmas, forward-only embedded
// migrations and readiness checks. It contains no business rules.
//
// Driver: modernc.org/sqlite, a pure-Go CGO-free driver chosen so the single
// static binary and cross-builds work without a C toolchain. ARM64 runtime
// behavior is verified on the Raspberry Pi 3B+ / Alpine deployment target.
package db

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"repairflow/internal/errs"

	_ "modernc.org/sqlite" // pure-Go SQLite driver, registers "sqlite"
)

// DriverName is the registered database/sql driver.
const DriverName = "sqlite"

// DBFileName is the database file inside <workspace>/data/.
const DBFileName = "repairflow.db"

// Path returns the database location inside a workspace.
func Path(workspaceHome string) string {
	return filepath.Join(workspaceHome, "data", DBFileName)
}

// Open opens (creating if needed) the database at path with the required
// pragmas. The parent directory is created owner-only if absent (it is the
// database's own directory); the file itself is created owner-only.
func Open(ctx context.Context, path string) (*sql.DB, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errs.New(errs.CodeValidation, "database path must not be empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, errs.Wrap(errs.CodeInternal, err, "cannot create database directory")
	}
	handle, err := sql.Open(DriverName, path)
	if err != nil {
		return nil, errs.Wrap(errs.CodeInternal, err, "cannot open database %q", path)
	}
	// One connection at a time: deterministic write serialization inside the
	// process; cross-process concurrency is handled by busy_timeout.
	handle.SetMaxOpenConns(1)
	for _, pragma := range []string{
		"PRAGMA foreign_keys = ON",
		"PRAGMA journal_mode = WAL",
		"PRAGMA synchronous = FULL",
		"PRAGMA busy_timeout = 5000",
	} {
		if _, err := handle.ExecContext(ctx, pragma); err != nil {
			_ = handle.Close()
			return nil, errs.Wrap(errs.CodeInternal, err, "cannot apply %s to %q", pragma, path)
		}
	}
	if err := handle.PingContext(ctx); err != nil {
		_ = handle.Close()
		return nil, errs.Wrap(errs.CodeInternal, err, "cannot ping database %q", path)
	}
	info, err := os.Stat(path)
	if err != nil {
		_ = handle.Close()
		return nil, errs.Wrap(errs.CodeInternal, err, "cannot inspect database %q", path)
	}
	if info.Mode().Perm()&0o077 != 0 {
		// Best effort tightening; refuse only if we cannot fix it.
		if err := os.Chmod(path, 0o600); err != nil {
			_ = handle.Close()
			return nil, errs.Wrap(errs.CodePermission, err, "database %q has unsafe permissions %04o", path, info.Mode().Perm())
		}
	}
	return handle, nil
}

// CurrentVersion returns the highest applied migration version (0 = fresh).
func CurrentVersion(ctx context.Context, handle *sql.DB) (int, error) {
	var version sql.NullInt64
	err := handle.QueryRowContext(ctx, "SELECT max(version) FROM schema_migrations").Scan(&version)
	if err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return 0, nil
		}
		return 0, errs.Wrap(errs.CodeInternal, err, "cannot read schema version")
	}
	if !version.Valid {
		return 0, nil
	}
	return int(version.Int64), nil
}

// Migrate applies pending migrations in order, each recorded in
// schema_migrations. A database from a newer version is refused
// ("refuse, never guess"); migrations never run implicitly at startup —
// callers invoke Migrate explicitly (init/seed/doctor).
func Migrate(ctx context.Context, handle *sql.DB) error {
	if err := refusePreRenameWorkspace(ctx, handle); err != nil {
		return err
	}
	if _, err := handle.ExecContext(ctx,
		`CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			name TEXT NOT NULL,
			applied_at TEXT NOT NULL
		) STRICT`); err != nil {
		return errs.Wrap(errs.CodeInternal, err, "cannot create schema_migrations")
	}

	current, err := CurrentVersion(ctx, handle)
	if err != nil {
		return err
	}
	latest := LatestVersion()
	if current > latest {
		return errs.New(errs.CodeConfigVersion, "database schema version %d is newer than the supported version %d; upgrade RepairFlow first", current, latest)
	}
	if current == latest {
		return nil
	}
	for _, m := range migrations {
		if m.version <= current {
			continue
		}
		if err := apply(ctx, handle, m); err != nil {
			return err
		}
	}
	return nil
}

// refusePreRenameWorkspace refuses a workspace database created before the
// Ticket→Repair rename. Those databases still carry the legacy
// "tickets" tables; the rename is schema-only and no data migration is
// written (only demo/synthetic workspaces exist), so the operator must
// recreate the workspace. Runs before any write.
func refusePreRenameWorkspace(ctx context.Context, handle *sql.DB) error {
	var found int
	if err := handle.QueryRowContext(ctx,
		`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name IN
			('tickets','ticket_entries','ticket_entry_revisions','ticket_parts','ticket_activity')`).Scan(&found); err != nil {
		return errs.Wrap(errs.CodeInternal, err, "cannot inspect schema for pre-rename tables")
	}
	if found == 0 {
		return nil
	}
	return errs.New(errs.CodeConflict,
		"workspace database predates the Ticket→Repair rename (legacy \"tickets\" tables are still present): there is no data migration, so recreate this demo workspace (move it aside and run `repairflow init --demo`)")
}

func apply(ctx context.Context, handle *sql.DB, m migration) error {
	if m.version == 9 {
		return applyRepairCustomerOptional(ctx, handle)
	}
	tx, err := handle.BeginTx(ctx, nil)
	if err != nil {
		return errs.Wrap(errs.CodeInternal, err, "cannot begin migration %d", m.version)
	}
	defer tx.Rollback() //nolint:errcheck // commit below supersedes this
	if _, err := tx.ExecContext(ctx, m.sql); err != nil {
		return errs.Wrap(errs.CodeInternal, err, "migration %d (%s) failed", m.version, m.name)
	}
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO schema_migrations(version, name, applied_at) VALUES(?,?,strftime('%Y-%m-%dT%H:%M:%SZ','now'))",
		m.version, m.name); err != nil {
		return errs.Wrap(errs.CodeInternal, err, "cannot record migration %d", m.version)
	}
	if err := tx.Commit(); err != nil {
		return errs.Wrap(errs.CodeInternal, err, "cannot commit migration %d", m.version)
	}
	return nil
}

// applyRepairCustomerOptional rebuilds repairs because SQLite cannot drop the
// NOT NULL constraint in place. Foreign keys are disabled on this single
// connection for the swap, child rows are left untouched, and the resulting
// schema is checked before the migration row is committed.
func applyRepairCustomerOptional(ctx context.Context, handle *sql.DB) (err error) {
	if _, err = handle.ExecContext(ctx, "PRAGMA foreign_keys = OFF"); err != nil {
		return errs.Wrap(errs.CodeInternal, err, "cannot disable foreign keys for migration 9")
	}
	defer func() {
		if _, restoreErr := handle.ExecContext(ctx, "PRAGMA foreign_keys = ON"); restoreErr != nil && err == nil {
			err = errs.Wrap(errs.CodeInternal, restoreErr, "cannot restore foreign keys after migration 9")
		}
	}()

	tx, err := handle.BeginTx(ctx, nil)
	if err != nil {
		return errs.Wrap(errs.CodeInternal, err, "cannot begin migration 9")
	}
	defer tx.Rollback() //nolint:errcheck // commit below supersedes this
	if _, err := tx.ExecContext(ctx, schemaV9); err != nil {
		return errs.Wrap(errs.CodeInternal, err, "migration 9 (repair_customer_optional) failed")
	}
	var violations int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM pragma_foreign_key_check`).Scan(&violations); err != nil {
		return errs.Wrap(errs.CodeInternal, err, "cannot check foreign keys after migration 9")
	}
	if violations != 0 {
		return errs.New(errs.CodeInternal, "migration 9 left %d foreign-key violation(s)", violations)
	}
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO schema_migrations(version, name, applied_at) VALUES(9,'repair_customer_optional',strftime('%Y-%m-%dT%H:%M:%SZ','now'))"); err != nil {
		return errs.Wrap(errs.CodeInternal, err, "cannot record migration 9")
	}
	if err := tx.Commit(); err != nil {
		return errs.Wrap(errs.CodeInternal, err, "cannot commit migration 9")
	}
	return nil
}

// IntegrityCheck runs SQLite's own integrity and foreign-key checks.
func IntegrityCheck(ctx context.Context, handle *sql.DB) error {
	var result string
	if err := handle.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&result); err != nil {
		return errs.Wrap(errs.CodeInternal, err, "integrity_check failed to run")
	}
	if result != "ok" {
		return errs.New(errs.CodeInternal, "integrity_check reported: %s", result)
	}
	rows, err := handle.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return errs.Wrap(errs.CodeInternal, err, "foreign_key_check failed to run")
	}
	defer func() { _ = rows.Close() }()
	var violations []string
	for rows.Next() {
		var table, parent string
		var rowid, fkid any
		if err := rows.Scan(&table, &rowid, &parent, &fkid); err != nil {
			return errs.Wrap(errs.CodeInternal, err, "cannot read foreign_key_check result")
		}
		violations = append(violations, fmt.Sprintf("%s->%s", table, parent))
	}
	if err := rows.Err(); err != nil {
		return errs.Wrap(errs.CodeInternal, err, "foreign_key_check iteration failed")
	}
	if len(violations) > 0 {
		return errs.New(errs.CodeInternal, "foreign key violations: %s", strings.Join(violations, ", "))
	}
	return nil
}
