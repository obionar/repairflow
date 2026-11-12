package db

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"repairflow/internal/errs"
)

func TestContactDeviceParityMigrationPreservesRecords(t *testing.T) {
	ctx := context.Background()
	handle, err := Open(ctx, filepath.Join(t.TempDir(), "old.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = handle.Close() })
	if _, err := handle.ExecContext(ctx, `CREATE TABLE schema_migrations (
		version INTEGER PRIMARY KEY, name TEXT NOT NULL, applied_at TEXT NOT NULL) STRICT`); err != nil {
		t.Fatal(err)
	}
	for _, m := range migrations {
		if m.version > 6 {
			break
		}
		if err := apply(ctx, handle, m); err != nil {
			t.Fatalf("apply v%d: %v", m.version, err)
		}
	}
	if _, err := handle.ExecContext(ctx,
		`INSERT INTO customers(name, phone, email, notes, created_at, updated_at) VALUES('Existing', '', '', '', 'x', 'x')`); err != nil {
		t.Fatal(err)
	}
	if _, err := handle.ExecContext(ctx,
		`INSERT INTO repairs(customer_id, device, issue, stage, notes, created_at, updated_at)
		 VALUES(1, 'Existing flattened device', 'Existing issue', 'new', '', 'x', 'x')`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, handle); err != nil {
		t.Fatalf("migrate to v9: %v", err)
	}
	var address, deviceType, issue string
	if err := handle.QueryRowContext(ctx,
		`SELECT address, device_type, issue FROM repairs JOIN customers ON customers.id = repairs.customer_id`).
		Scan(&address, &deviceType, &issue); err != nil {
		t.Fatal(err)
	}
	if address != "" || deviceType != "Existing flattened device" || issue != "Existing issue" {
		t.Fatalf("migration changed existing data: address=%q device_type=%q issue=%q", address, deviceType, issue)
	}
	version, err := CurrentVersion(ctx, handle)
	if err != nil || version != 9 {
		t.Fatalf("version=%d err=%v", version, err)
	}
	var legacyColumns int
	if err := handle.QueryRowContext(ctx,
		`SELECT count(*) FROM pragma_table_info('repairs') WHERE name = 'legacy_id'`).Scan(&legacyColumns); err != nil {
		t.Fatal(err)
	}
	if legacyColumns != 0 {
		t.Fatal("clean-start schema must not retain legacy_id")
	}
	if err := IntegrityCheck(ctx, handle); err != nil {
		t.Fatal(err)
	}
}

func TestRepairCustomerOptionalMigrationPreservesRecords(t *testing.T) {
	ctx := context.Background()
	handle, err := Open(ctx, filepath.Join(t.TempDir(), "orphan-migration.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = handle.Close() })
	if _, err := handle.ExecContext(ctx, `CREATE TABLE schema_migrations (
		version INTEGER PRIMARY KEY, name TEXT NOT NULL, applied_at TEXT NOT NULL) STRICT`); err != nil {
		t.Fatal(err)
	}
	for _, m := range migrations {
		if m.version > 8 {
			break
		}
		if err := apply(ctx, handle, m); err != nil {
			t.Fatalf("apply v%d: %v", m.version, err)
		}
	}
	if _, err := handle.ExecContext(ctx,
		`INSERT INTO customers(name, phone, email, notes, created_at, updated_at)
		 VALUES('Existing', '', '', '', 'x', 'x')`); err != nil {
		t.Fatal(err)
	}
	if _, err := handle.ExecContext(ctx,
		`INSERT INTO repairs(customer_id, device, device_type, issue, stage, notes, created_at, updated_at)
		 VALUES(1, 'Existing device', 'Existing device', 'Existing issue', 'new', '', 'x', 'x')`); err != nil {
		t.Fatal(err)
	}
	if _, err := handle.ExecContext(ctx,
		`INSERT INTO repair_entries(repair_id, kind, body, created_at) VALUES(1, 'work', 'kept entry', 'x')`); err != nil {
		t.Fatal(err)
	}
	if _, err := handle.ExecContext(ctx,
		`INSERT INTO repair_activity(repair_id, actor, action, detail, created_at) VALUES(1, 'tester', 'create', '{}', 'x')`); err != nil {
		t.Fatal(err)
	}

	if err := Migrate(ctx, handle); err != nil {
		t.Fatalf("migrate to v9: %v", err)
	}
	version, err := CurrentVersion(ctx, handle)
	if err != nil || version != 9 {
		t.Fatalf("version=%d err=%v, want 9", version, err)
	}
	var customerID int64
	var device, issue string
	if err := handle.QueryRowContext(ctx,
		`SELECT customer_id, device, issue FROM repairs WHERE id = 1`).Scan(&customerID, &device, &issue); err != nil {
		t.Fatal(err)
	}
	if customerID != 1 || device != "Existing device" || issue != "Existing issue" {
		t.Fatalf("migration changed existing repair: customer=%d device=%q issue=%q", customerID, device, issue)
	}
	var entries, activities int
	if err := handle.QueryRowContext(ctx, `SELECT count(*) FROM repair_entries WHERE repair_id = 1`).Scan(&entries); err != nil {
		t.Fatal(err)
	}
	if err := handle.QueryRowContext(ctx, `SELECT count(*) FROM repair_activity WHERE repair_id = 1`).Scan(&activities); err != nil {
		t.Fatal(err)
	}
	if entries != 1 || activities != 1 {
		t.Fatalf("migration lost child rows: entries=%d activities=%d", entries, activities)
	}
	if _, err := handle.ExecContext(ctx,
		`INSERT INTO repairs(customer_id, device, device_type, issue, stage, notes, created_at, updated_at)
		 VALUES(NULL, 'Orphan device', 'Orphan device', 'No customer yet', 'new', '', 'x', 'x')`); err != nil {
		t.Fatalf("nullable customer_id not migrated: %v", err)
	}
	var foreignKeys int
	if err := handle.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil || foreignKeys != 1 {
		t.Fatalf("foreign_keys after migration = %d, err=%v; want 1", foreignKeys, err)
	}
	if err := IntegrityCheck(ctx, handle); err != nil {
		t.Fatal(err)
	}
}

// TestFreshSchemaUsesRepairNames pins the rename: a fresh workspace contains
// only repairs* tables, the old ticket* names are gone entirely.
func TestFreshSchemaUsesRepairNames(t *testing.T) {
	ctx := context.Background()
	handle, err := Open(ctx, filepath.Join(t.TempDir(), "fresh.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = handle.Close() })
	if err := Migrate(ctx, handle); err != nil {
		t.Fatal(err)
	}
	rows, err := handle.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'table'`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	seen := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		seen[name] = true
		if strings.HasPrefix(name, "ticket") {
			t.Fatalf("fresh schema must not contain legacy table %q", name)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"repairs", "repair_entries", "repair_entry_revisions", "repair_parts", "repair_activity"} {
		if !seen[want] {
			t.Fatalf("fresh schema is missing table %q (have %v)", want, seen)
		}
	}
	var legacyIndexes int
	if err := handle.QueryRowContext(ctx,
		`SELECT count(*) FROM sqlite_master WHERE type = 'index' AND name LIKE 'idx_ticket%'`).Scan(&legacyIndexes); err != nil {
		t.Fatal(err)
	}
	if legacyIndexes != 0 {
		t.Fatalf("fresh schema must not contain legacy ticket indexes, found %d", legacyIndexes)
	}
}

// TestPreRenameWorkspaceIsRefused covers a database at the latest
// version that still has the pre-rename "tickets" tables is refused with a
// "recreate the workspace" error, and nothing is written to it.
func TestPreRenameWorkspaceIsRefused(t *testing.T) {
	ctx := context.Background()
	handle, err := Open(ctx, filepath.Join(t.TempDir(), "prerename.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = handle.Close() })
	if _, err := handle.ExecContext(ctx, `CREATE TABLE schema_migrations (
		version INTEGER PRIMARY KEY, name TEXT NOT NULL, applied_at TEXT NOT NULL) STRICT`); err != nil {
		t.Fatal(err)
	}
	if _, err := handle.ExecContext(ctx,
		`INSERT INTO schema_migrations(version, name, applied_at) VALUES(?, 'synthetic_pre_rename', 'x')`, LatestVersion()); err != nil {
		t.Fatal(err)
	}
	if _, err := handle.ExecContext(ctx, `CREATE TABLE tickets (
		id INTEGER PRIMARY KEY, customer_id INTEGER NOT NULL, device TEXT NOT NULL) STRICT`); err != nil {
		t.Fatal(err)
	}
	if _, err := handle.ExecContext(ctx,
		`INSERT INTO tickets(id, customer_id, device) VALUES(1, 1, 'Old record')`); err != nil {
		t.Fatal(err)
	}

	err = Migrate(ctx, handle)
	if err == nil {
		t.Fatal("pre-rename workspace must be refused")
	}
	if code := errs.CodeOf(err); code != errs.CodeConflict {
		t.Fatalf("error code = %q, want %q (%v)", code, errs.CodeConflict, err)
	}
	if !strings.Contains(err.Error(), "recreate") {
		t.Fatalf("error must tell the operator to recreate the workspace: %v", err)
	}

	var repairs, tickets, migrations int
	if err := handle.QueryRowContext(ctx,
		`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'repairs'`).Scan(&repairs); err != nil {
		t.Fatal(err)
	}
	if err := handle.QueryRowContext(ctx, `SELECT count(*) FROM tickets`).Scan(&tickets); err != nil {
		t.Fatal(err)
	}
	if err := handle.QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&migrations); err != nil {
		t.Fatal(err)
	}
	if repairs != 0 || tickets != 1 || migrations != 1 {
		t.Fatalf("guard must not write: repairs=%d tickets=%d migrations=%d", repairs, tickets, migrations)
	}
}
