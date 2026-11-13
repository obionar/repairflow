package app_test

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"repairflow/internal/app"
	"repairflow/internal/db"
	"repairflow/internal/errs"
	"repairflow/internal/files"
	"repairflow/internal/testutil"
)

// twoServices opens one workspace twice. Two connections are what a second
// process sees (the CLI running next to the web server), so a test over them
// exercises the cross-process contract that a single handle hides.
func twoServices(t *testing.T) (*app.Service, *app.Service) {
	t.Helper()
	home := filepath.Join(t.TempDir(), "ws")
	open := func() *app.Service {
		handle, err := db.Open(context.Background(), db.Path(home))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { testutil.Close(t, handle) })
		if err := db.Migrate(context.Background(), handle); err != nil {
			t.Fatal(err)
		}
		return app.New(handle).WithStore(files.New(home))
	}
	return open(), open()
}

// TestStaleRepairRevisionAcrossProcesses: the revision is checked inside the
// write transaction, so a write committed by another process between read and
// update is refused instead of being silently overwritten.
func TestStaleRepairRevisionAcrossProcesses(t *testing.T) {
	ctx := context.Background()
	a, b := twoServices(t)
	c, err := a.CreateCustomer(ctx, "web", app.CustomerInput{Name: "Cust"})
	if err != nil {
		t.Fatal(err)
	}
	r, err := a.CreateRepair(ctx, "web", app.RepairInput{CustomerID: c.ID, Device: "Laptop", Issue: "No power"})
	if err != nil {
		t.Fatal(err)
	}

	// Process A reads the record and its revision.
	read, err := a.GetRepairRow(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Process B edits the same repair.
	if _, err := b.UpdateRepairDetails(ctx, "cli", r.ID, app.RepairPatch{Device: ptr("Laptop Pro")}, read.Revision); err != nil {
		t.Fatalf("process B update: %v", err)
	}
	// A's stale write must be refused, not applied over B's.
	if _, err := a.UpdateRepairDetails(ctx, "web", r.ID, app.RepairPatch{Notes: ptr("stale note")}, read.Revision); errs.CodeOf(err) != errs.CodeConflict {
		t.Fatalf("stale cross-process update must conflict, got: %v", err)
	}
	got, err := a.GetRepairRow(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.DeviceType != "Laptop Pro" || got.Notes != "" {
		t.Fatalf("process B's write was lost: %+v", got)
	}
}

// TestStalePartRevisionAcrossProcesses covers the same rule for part lines,
// where a lost update rewrites money.
func TestStalePartRevisionAcrossProcesses(t *testing.T) {
	ctx := context.Background()
	a, b := twoServices(t)
	r, err := a.CreateRepair(ctx, "web", app.RepairInput{Device: "Radio", Issue: "Dead"})
	if err != nil {
		t.Fatal(err)
	}
	price := int64(2500)
	p, err := a.AddRepairPart(ctx, "web", r.ID, app.PartInput{Description: "Fuse", QuantityMilli: 2000, Unit: "pcs", UnitPriceCents: price})
	if err != nil {
		t.Fatal(err)
	}
	read, err := a.GetRepairPart(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	cheaper := int64(1000)
	if _, err := b.UpdateRepairPart(ctx, "cli", p.ID, app.PartPatch{UnitPriceCents: &cheaper}, read.Revision); err != nil {
		t.Fatalf("process B part update: %v", err)
	}
	cheapest := int64(1)
	if _, err := a.UpdateRepairPart(ctx, "web", p.ID, app.PartPatch{UnitPriceCents: &cheapest}, read.Revision); errs.CodeOf(err) != errs.CodeConflict {
		t.Fatalf("stale part update must conflict, got: %v", err)
	}
	got, err := a.GetRepairPart(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.UnitPriceCents != cheaper {
		t.Fatalf("process B's price was lost: %+v", got)
	}
}

// TestIdempotentCreateAcrossProcesses: two processes retrying the same key must
// replay, never duplicate and never fail with a raw lock error.
func TestIdempotentCreateAcrossProcesses(t *testing.T) {
	ctx := context.Background()
	a, b := twoServices(t)
	in := app.RepairInput{Device: "Printer", Issue: "Jam"}
	key := "agent-req-cross-01"

	var wg sync.WaitGroup
	ids := make(chan int64, 8)
	errsSeen := make(chan error, 8)
	for i := 0; i < 8; i++ {
		svc := a
		if i%2 == 1 {
			svc = b
		}
		wg.Add(1)
		go func(svc *app.Service) {
			defer wg.Done()
			created, err := svc.CreateRepairWithKey(ctx, "agent", in, key)
			if err != nil {
				errsSeen <- err
				return
			}
			ids <- created.ID
		}(svc)
	}
	wg.Wait()
	close(ids)
	close(errsSeen)
	for err := range errsSeen {
		t.Fatalf("concurrent idempotent create failed: %v", err)
	}
	seen := map[int64]bool{}
	for id := range ids {
		seen[id] = true
	}
	if len(seen) != 1 {
		t.Fatalf("expected one repair across both processes, got %v", seen)
	}
	if p, err := a.ListRepairs(ctx, app.RepairFilter{}); err != nil || p.Total != 1 {
		t.Fatalf("duplicates created: %+v %v", p, err)
	}
}
