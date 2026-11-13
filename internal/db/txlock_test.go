package db_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"repairflow/internal/db"
	"repairflow/internal/testutil"
)

// TestWriteTransactionsAreImmediate pins the property that closes the
// cross-process lost-update window: a write transaction takes SQLite's lock at
// BEGIN, so a check-then-write inside one transaction cannot be interleaved by
// another process's commit. A deferred transaction would read the old row and
// then overwrite the other process's write.
func TestWriteTransactionsAreImmediate(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "lock.db")
	first, err := db.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer testutil.Close(t, first)
	if err := db.Migrate(ctx, first); err != nil {
		t.Fatal(err)
	}
	second, err := db.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer testutil.Close(t, second)
	// Keep the wait short so the test does not spend the production timeout.
	if _, err := second.ExecContext(ctx, "PRAGMA busy_timeout = 100"); err != nil {
		t.Fatal(err)
	}

	tx, err := first.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS lock_probe(x)"); err != nil {
		t.Fatalf("write inside first transaction: %v", err)
	}
	if _, err := second.BeginTx(ctx, nil); err == nil || !strings.Contains(err.Error(), "SQLITE_BUSY") {
		t.Fatalf("second writer must wait for the lock at BEGIN, got: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	// Once the first transaction is closed the second writer proceeds.
	tx2, err := second.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("second writer after commit: %v", err)
	}
	if err := tx2.Rollback(); err != nil {
		t.Fatal(err)
	}
}

// TestOpenWaitsForAnotherProcess: the CLI must be able to open a workspace while
// the web server holds a write transaction. journal_mode=WAL needs an exclusive
// lock, so it must wait (busy_timeout first) and be skipped when already set.
func TestOpenWaitsForAnotherProcess(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "open.db")
	first, err := db.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer testutil.Close(t, first)
	if err := db.Migrate(ctx, first); err != nil {
		t.Fatal(err)
	}
	tx, err := first.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS open_probe(x)"); err != nil {
		t.Fatal(err)
	}
	second, err := db.Open(ctx, path)
	if err != nil {
		t.Fatalf("second process must open a busy workspace: %v", err)
	}
	defer testutil.Close(t, second)
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
}
