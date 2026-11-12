package app_test

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"repairflow/internal/app"
	"repairflow/internal/errs"
	"repairflow/internal/files"
)

func purgeFixture(t *testing.T) (*app.Service, app.Attachment, []byte, time.Time) {
	t.Helper()
	ctx := context.Background()
	svc, _ := newAttachmentService(t)
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	svc.SetClock(func() time.Time { return now })
	c, err := svc.CreateCustomer(ctx, "tester", app.CustomerInput{Name: "Cust"})
	if err != nil {
		t.Fatal(err)
	}
	tk, err := svc.CreateRepair(ctx, "tester", app.RepairInput{CustomerID: c.ID, Device: "D", Issue: "I"})
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("original attachment bytes that must survive rollback")
	a, err := svc.AddAttachment(ctx, "tester", tk.ID, 0, "evidence.bin", bytes.NewReader(payload), "", "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Detach(ctx, "tester", a.ID); err != nil {
		t.Fatal(err)
	}
	return svc, a, payload, now
}

func assertPurgeRolledBack(t *testing.T, svc *app.Service, id int64, want []byte) {
	t.Helper()
	ctx := context.Background()
	gotMeta, err := svc.GetAttachment(ctx, id)
	if err != nil {
		t.Fatalf("read attachment after failed purge: %v", err)
	}
	if gotMeta.State != "detached" {
		t.Fatalf("state after failed purge = %q, want detached", gotMeta.State)
	}
	rc, meta, err := svc.OpenBytes(ctx, id)
	if err != nil {
		t.Fatalf("original bytes lost after failed purge: %v", err)
	}
	got, readErr := io.ReadAll(rc)
	closeErr := rc.Close()
	if readErr != nil || closeErr != nil {
		t.Fatalf("read original bytes: read=%v close=%v", readErr, closeErr)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("original bytes changed after failed purge: got %q want %q", got, want)
	}
	if meta.SHA256 != gotMeta.SHA256 {
		t.Fatalf("hash changed after failed purge: got %s want %s", gotMeta.SHA256, meta.SHA256)
	}
}

func TestAttachmentPurgeMetadataFailurePreservesBytes(t *testing.T) {
	svc, a, payload, now := purgeFixture(t)
	if _, err := svc.DB().Exec(`
		CREATE TRIGGER fail_purge_metadata
		BEFORE UPDATE OF state ON attachments
		WHEN NEW.state = 'purged'
		BEGIN
			SELECT RAISE(ABORT, 'injected metadata failure');
		END;`); err != nil {
		t.Fatal(err)
	}
	now = now.Add(31 * 24 * time.Hour)
	svc.SetClock(func() time.Time { return now })

	if _, err := svc.Purge(context.Background(), "tester", a.ID, true); err == nil {
		t.Fatal("purge must fail when the metadata update fails")
	}
	assertPurgeRolledBack(t, svc, a.ID, payload)
}

func TestAttachmentPurgeAuditFailurePreservesBytes(t *testing.T) {
	svc, a, payload, now := purgeFixture(t)
	if _, err := svc.DB().Exec(`
		CREATE TRIGGER fail_purge_audit
		BEFORE INSERT ON repair_activity
		WHEN NEW.action = 'attachment_purge'
		BEGIN
			SELECT RAISE(ABORT, 'injected audit failure');
		END;`); err != nil {
		t.Fatal(err)
	}
	now = now.Add(31 * 24 * time.Hour)
	svc.SetClock(func() time.Time { return now })

	if _, err := svc.Purge(context.Background(), "tester", a.ID, true); err == nil {
		t.Fatal("purge must fail when the audit insert fails")
	}
	assertPurgeRolledBack(t, svc, a.ID, payload)
}

func TestRecoverPendingDeleteRestoresInterruptedPurge(t *testing.T) {
	ctx := context.Background()
	svc, home := newAttachmentService(t)
	c, err := svc.CreateCustomer(ctx, "tester", app.CustomerInput{Name: "Cust"})
	if err != nil {
		t.Fatal(err)
	}
	tk, err := svc.CreateRepair(ctx, "tester", app.RepairInput{CustomerID: c.ID, Device: "D", Issue: "I"})
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("bytes protected across an interrupted purge")
	a, err := svc.AddAttachment(ctx, "tester", tk.ID, 0, "evidence.bin", bytes.NewReader(payload), "", "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Detach(ctx, "tester", a.ID); err != nil {
		t.Fatal(err)
	}
	name, err := svc.AttachmentStorageName(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	store := files.New(home)
	if _, err := store.StageDelete(a.RepairID, name); err != nil {
		t.Fatal(err)
	}
	// Simulate a process crash after staging the filesystem delete but before
	// the metadata transaction commits: the live name is gone, metadata is
	// still detached.
	if _, _, err := svc.OpenBytes(ctx, a.ID); err == nil {
		t.Fatal("staged delete must make the live path unavailable before recovery")
	}
	if err := svc.RecoverPendingDeletes(ctx); err != nil {
		t.Fatalf("recovery: %v", err)
	}
	assertPurgeRolledBack(t, svc, a.ID, payload)
	pending, err := store.ListPendingDeletes()
	if err != nil || len(pending) != 0 {
		t.Fatalf("recovery links after restore: %+v %v", pending, err)
	}
}

func TestRecoverPendingDeleteFinalizesCommittedDelete(t *testing.T) {
	ctx := context.Background()
	svc, home := newAttachmentService(t)
	c, err := svc.CreateCustomer(ctx, "tester", app.CustomerInput{Name: "Cust"})
	if err != nil {
		t.Fatal(err)
	}
	tk, err := svc.CreateRepair(ctx, "tester", app.RepairInput{CustomerID: c.ID, Device: "D", Issue: "I"})
	if err != nil {
		t.Fatal(err)
	}
	a, err := svc.AddAttachment(ctx, "tester", tk.ID, 0, "intake.jpg", bytes.NewReader([]byte("committed delete")), "", "test")
	if err != nil {
		t.Fatal(err)
	}
	name, err := svc.AttachmentStorageName(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	store := files.New(home)
	if _, err := store.StageDelete(a.RepairID, name); err != nil {
		t.Fatal(err)
	}
	// Simulate a crash after demo-reset metadata deletion committed but before
	// recovery links were finalized. There is no row left to restore.
	if _, err := svc.DB().Exec(`DELETE FROM attachments WHERE id = ?`, a.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecoverPendingDeletes(ctx); err != nil {
		t.Fatalf("recovery: %v", err)
	}
	pending, err := store.ListPendingDeletes()
	if err != nil || len(pending) != 0 {
		t.Fatalf("recovery links after committed delete: %+v %v", pending, err)
	}
	if exists, err := store.Exists(a.RepairID, name); err != nil || exists {
		t.Fatalf("committed delete bytes must not be restored: exists=%v err=%v", exists, err)
	}
}

func TestPurgeWaitsForPurgeLock(t *testing.T) {
	svc, a, _, now := purgeFixture(t)
	now = now.Add(31 * 24 * time.Hour)
	svc.SetClock(func() time.Time { return now })
	home := mustWorkspaceHome(t)
	release, err := files.New(home).LockPurge(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := svc.Purge(context.Background(), "tester", a.ID, true)
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("purge completed while the cross-process purge lock was held: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("purge after lock release: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("purge did not resume after the purge lock was released")
	}
}

func TestRestoreDeleteReportsMissingRecoveryLink(t *testing.T) {
	ctx := context.Background()
	svc, home := newAttachmentService(t)
	c, err := svc.CreateCustomer(ctx, "tester", app.CustomerInput{Name: "Cust"})
	if err != nil {
		t.Fatal(err)
	}
	tk, err := svc.CreateRepair(ctx, "tester", app.RepairInput{CustomerID: c.ID, Device: "D", Issue: "I"})
	if err != nil {
		t.Fatal(err)
	}
	a, err := svc.AddAttachment(ctx, "tester", tk.ID, 0, "evidence.bin", bytes.NewReader([]byte("bytes")), "", "test")
	if err != nil {
		t.Fatal(err)
	}
	name, err := svc.AttachmentStorageName(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	store := files.New(home)
	pd, err := store.StageDelete(a.RepairID, name)
	if err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(home, "files", ".purge", strconv.FormatInt(a.RepairID, 10), pd.BackupName)
	if err := os.Remove(backup); err != nil {
		t.Fatal(err)
	}
	if err := store.RestoreDelete(pd); err == nil {
		t.Fatal("missing recovery link must be reported, not silently treated as restored")
	}
}

func TestRecoveryWaitsForPurgeLock(t *testing.T) {
	ctx := context.Background()
	svc, home := newAttachmentService(t)
	c, err := svc.CreateCustomer(ctx, "tester", app.CustomerInput{Name: "Cust"})
	if err != nil {
		t.Fatal(err)
	}
	tk, err := svc.CreateRepair(ctx, "tester", app.RepairInput{CustomerID: c.ID, Device: "D", Issue: "I"})
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("must not be restored by a concurrent opener")
	a, err := svc.AddAttachment(ctx, "tester", tk.ID, 0, "evidence.bin", bytes.NewReader(payload), "", "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Detach(ctx, "tester", a.ID); err != nil {
		t.Fatal(err)
	}
	name, err := svc.AttachmentStorageName(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	store := files.New(home)
	if _, err := store.StageDelete(a.RepairID, name); err != nil {
		t.Fatal(err)
	}
	release, err := store.LockPurge(ctx)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- svc.RecoverPendingDeletes(ctx) }()
	select {
	case err := <-done:
		t.Fatalf("recovery acted while another process held the purge lock: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("recovery after lock release: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("recovery did not resume after the purge lock was released")
	}
	assertPurgeRolledBack(t, svc, a.ID, payload)
}

func TestDestructiveAttachmentOpsRefuseAmbientTransaction(t *testing.T) {
	ctx := context.Background()
	svc, a, payload, now := purgeFixture(t)
	now = now.Add(31 * 24 * time.Hour)
	svc.SetClock(func() time.Time { return now })

	err := svc.InTx(ctx, func(txCtx context.Context) error {
		if _, err := svc.Purge(txCtx, "tester", a.ID, true); errs.CodeOf(err) != errs.CodeValidation {
			t.Fatalf("purge inside ambient transaction = %v, want validation_error", err)
		}
		if err := svc.ClearBusinessRecords(txCtx); errs.CodeOf(err) != errs.CodeValidation {
			t.Fatalf("clear inside ambient transaction = %v, want validation_error", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("ambient transaction: %v", err)
	}
	assertPurgeRolledBack(t, svc, a.ID, payload)
}

func TestClearBusinessRecordsFailurePreservesAttachmentBytes(t *testing.T) {
	ctx := context.Background()
	svc, _ := newAttachmentService(t)
	c, err := svc.CreateCustomer(ctx, "tester", app.CustomerInput{Name: "Cust"})
	if err != nil {
		t.Fatal(err)
	}
	tk, err := svc.CreateRepair(ctx, "tester", app.RepairInput{CustomerID: c.ID, Device: "D", Issue: "I"})
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("attachment bytes that must survive a failed demo reset")
	a, err := svc.AddAttachment(ctx, "tester", tk.ID, 0, "intake.jpg", bytes.NewReader(payload), "", "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.DB().Exec(`
		CREATE TRIGGER fail_clear_repairs
		BEFORE DELETE ON repairs
		BEGIN
			SELECT RAISE(ABORT, 'injected clear failure');
		END;`); err != nil {
		t.Fatal(err)
	}

	if err := svc.ClearBusinessRecords(ctx); err == nil {
		t.Fatal("clear must fail when a business-table delete fails")
	}
	if _, err := svc.GetAttachment(ctx, a.ID); err != nil {
		t.Fatalf("failed clear must roll back metadata: %v", err)
	}
	rc, meta, err := svc.OpenBytes(ctx, a.ID)
	if err != nil {
		t.Fatalf("failed clear lost attachment bytes: %v", err)
	}
	got, readErr := io.ReadAll(rc)
	closeErr := rc.Close()
	if readErr != nil || closeErr != nil {
		t.Fatalf("read attachment after failed clear: read=%v close=%v", readErr, closeErr)
	}
	if !bytes.Equal(got, payload) || meta.SHA256 != a.SHA256 {
		t.Fatalf("attachment changed after failed clear: got %q sha=%s want %q sha=%s", got, meta.SHA256, payload, a.SHA256)
	}
}
