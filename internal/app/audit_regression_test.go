package app_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"repairflow/internal/app"
	"repairflow/internal/db"
	"repairflow/internal/errs"
	"repairflow/internal/files"
	"repairflow/internal/testutil"
)

func TestRepairRevisionIncludesCustomerAssignment(t *testing.T) {
	ctx := context.Background()
	svc := newService(t)
	a, err := svc.CreateCustomer(ctx, "tester", app.CustomerInput{Name: "Original"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := svc.CreateCustomer(ctx, "tester", app.CustomerInput{Name: "Replacement"})
	if err != nil {
		t.Fatal(err)
	}
	repair, err := svc.CreateRepair(ctx, "tester", app.RepairInput{CustomerID: a.ID, Device: "Phone", Issue: "Test"})
	if err != nil {
		t.Fatal(err)
	}
	oldRevision := repair.Revision
	updated, err := svc.UpdateRepairDetails(ctx, "tester", repair.ID, app.RepairPatch{CustomerID: &b.ID}, oldRevision)
	if err != nil {
		t.Fatalf("reassign: %v", err)
	}
	if updated.Revision == oldRevision {
		t.Fatal("customer reassignment must rotate the repair revision")
	}
	if _, err := svc.UpdateRepairDetails(ctx, "tester", repair.ID, app.RepairPatch{CustomerID: &a.ID}, oldRevision); errs.CodeOf(err) != errs.CodeConflict {
		t.Fatalf("stale full-form save must not undo the reassignment: %v", err)
	}
	after, err := svc.GetRepairRow(ctx, repair.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.CustomerID != b.ID {
		t.Fatalf("customer changed after stale save: got %d want %d", after.CustomerID, b.ID)
	}
}

func TestAttachmentReconcileDetectsContentCorruption(t *testing.T) {
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
	a, err := svc.AddAttachment(ctx, "tester", tk.ID, 0, "evidence.bin", bytes.NewReader([]byte("original bytes")), "", "test")
	if err != nil {
		t.Fatal(err)
	}
	name, err := svc.AttachmentStorageName(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "files", "tickets", strconv.FormatInt(tk.ID, 10), name)
	if err := os.WriteFile(path, []byte("corrupted bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	missing, orphans, err := svc.ReconcileAttachments(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 1 || len(orphans) != 0 {
		t.Fatalf("corrupt bytes must be reported: missing=%v orphans=%v", missing, orphans)
	}
}

func TestCustomerIdempotencyIncludesAddress(t *testing.T) {
	ctx := context.Background()
	svc := newService(t)
	key := "audit-address-0001"
	first, err := svc.CreateCustomerWithKey(ctx, "tester", app.CustomerInput{
		Name: "Same", Address: "Address A",
	}, key)
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.CreateCustomerWithKey(ctx, "tester", app.CustomerInput{
		Name: "Same", Address: "Address B",
	}, key)
	if errs.CodeOf(err) != errs.CodeConflict {
		t.Fatalf("changed address must conflict, got: %v", err)
	}
	after, err := svc.GetCustomer(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Address != "Address A" {
		t.Fatalf("address changed after conflicting retry: %q", after.Address)
	}
}

// A purge waiting for the write lock must re-check the retention deadline
// after a concurrent restore/re-detach. The old pre-transaction check could
// destroy bytes under a newly renewed grace period.
func TestPurgeRechecksRenewedRetentionInsideTransaction(t *testing.T) {
	ctx := context.Background()
	svc, a, payload, now := purgeFixture(t)
	home := mustWorkspaceHome(t)
	otherHandle, err := db.Open(ctx, filepath.Join(home, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { testutil.Close(t, otherHandle) })
	other := app.New(otherHandle).WithStore(files.New(home))
	now = now.Add(app.PurgeGrace + time.Hour)
	svc.SetClock(func() time.Time { return now })
	other.SetClock(func() time.Time { return now })

	ready := make(chan struct{})
	release := make(chan struct{})
	txDone := make(chan error, 1)
	go func() {
		txDone <- other.InTx(ctx, func(txCtx context.Context) error {
			if _, err := other.Restore(txCtx, "tester", a.ID); err != nil {
				return err
			}
			if _, err := other.Detach(txCtx, "tester", a.ID); err != nil {
				return err
			}
			close(ready)
			<-release
			return nil
		})
	}()
	<-ready

	purgeDone := make(chan error, 1)
	go func() {
		_, err := svc.Purge(ctx, "tester", a.ID, true)
		purgeDone <- err
	}()
	// Give purge time to pass any outer checks and block on the write lock.
	time.Sleep(100 * time.Millisecond)
	close(release)
	if err := <-txDone; err != nil {
		t.Fatalf("restore/re-detach: %v", err)
	}
	if err := <-purgeDone; errs.CodeOf(err) != errs.CodeConflict {
		t.Fatalf("purge must refuse the renewed grace period, got: %v", err)
	}
	assertPurgeRolledBack(t, svc, a.ID, payload)
}

// Keys created before Address joined the customer fingerprint must still
// replay after an upgrade. New keys bind Address, so the original defect is
// closed for new requests.
func TestLegacyCustomerIdempotencyStillReplays(t *testing.T) {
	ctx := context.Background()
	svc := newService(t)
	key := "audit-legacy-key-0001"
	in := app.CustomerInput{Name: "Legacy", Phone: "111", Email: "legacy@example.invalid", Address: "Address A"}
	first, err := svc.CreateCustomerWithKey(ctx, "tester", in, key)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.New()
	h.Write([]byte("customer.create"))
	for _, field := range []string{in.Name, in.Phone, in.Email, in.Notes, "tester"} {
		h.Write([]byte{0x1f})
		h.Write([]byte(field))
	}
	if _, err := svc.DB().Exec(`UPDATE request_idempotency SET fingerprint = ? WHERE key = ?`,
		hex.EncodeToString(h.Sum(nil)), key); err != nil {
		t.Fatal(err)
	}
	replay, err := svc.CreateCustomerWithKey(ctx, "tester", in, key)
	if err != nil {
		t.Fatalf("legacy replay: %v", err)
	}
	if replay.ID != first.ID {
		t.Fatalf("legacy replay returned customer %d, want %d", replay.ID, first.ID)
	}
	changed := in
	changed.Address = "A different address"
	if _, err := svc.CreateCustomerWithKey(ctx, "tester", changed, key); errs.CodeOf(err) != errs.CodeConflict {
		t.Fatalf("legacy key with changed address must conflict, got: %v", err)
	}
}
