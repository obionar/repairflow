package app_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"repairflow/internal/app"
	"repairflow/internal/db"
	"repairflow/internal/files"
	"repairflow/internal/testutil"
)

func TestReconcileFindsOrphansForUnknownRepairDirectory(t *testing.T) {
	home := t.TempDir()
	svc, err := newAttachmentServiceAtHome(t, home)
	testutil.Must(t, err)
	dir := filepath.Join(home, "files", "tickets", "999")
	testutil.Must(t, os.MkdirAll(dir, 0o700))
	name := strings.Repeat("a", 32)
	testutil.Must(t, os.WriteFile(filepath.Join(dir, name), []byte("orphan"), 0o600))
	missing, orphans, err := svc.ReconcileAttachments(context.Background())
	testutil.Must(t, err)
	if len(missing) != 0 || len(orphans) != 1 || !strings.Contains(orphans[0], "999") {
		t.Fatalf("missing=%v orphans=%v, want repair 999 orphan", missing, orphans)
	}
}

func newAttachmentServiceAtHome(t *testing.T, home string) (*app.Service, error) {
	t.Helper()
	handle, err := db.Open(context.Background(), db.Path(home))
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() { testutil.Close(t, handle) })
	if err := db.Migrate(context.Background(), handle); err != nil {
		return nil, err
	}
	return app.New(handle).WithStore(files.New(home)), nil
}
