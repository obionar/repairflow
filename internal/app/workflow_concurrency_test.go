package app_test

import (
	"context"
	"sync"
	"testing"

	"repairflow/internal/app"
	"repairflow/internal/errs"
)

// runConcurrently runs fn once on each service handle (two processes' view of
// one workspace) and reports how many calls succeeded.
func runConcurrently(t *testing.T, a, b *app.Service, fn func(*app.Service) error) int {
	t.Helper()
	var wg sync.WaitGroup
	results := make(chan error, 2)
	start := make(chan struct{})
	for _, svc := range []*app.Service{a, b} {
		wg.Add(1)
		go func(svc *app.Service) {
			defer wg.Done()
			<-start
			results <- fn(svc)
		}(svc)
	}
	close(start)
	wg.Wait()
	close(results)
	succeeded := 0
	for err := range results {
		if err == nil {
			succeeded++
		} else if errs.CodeOf(err) != errs.CodeConflict {
			t.Fatalf("unexpected failure: %v", err)
		}
	}
	return succeeded
}

// TestConcurrentArchiveRepair: two processes archiving the same repair must
// produce one state change, not two "already archived" passes with two audit
// entries. The check lives inside the write transaction.
func TestConcurrentArchiveRepair(t *testing.T) {
	ctx := context.Background()
	a, b := twoServices(t)
	r, err := a.CreateRepair(ctx, "tester", app.RepairInput{Device: "Radio", Issue: "Dead"})
	if err != nil {
		t.Fatal(err)
	}
	succeeded := runConcurrently(t, a, b, func(svc *app.Service) error {
		_, err := svc.ArchiveRepair(ctx, "tester", r.ID, true)
		return err
	})
	if succeeded != 1 {
		t.Fatalf("exactly one archive may succeed, got %d", succeeded)
	}
	got, err := a.GetRepairRow(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ArchivedAt == "" {
		t.Fatal("repair is not archived after the race")
	}
}

// TestConcurrentEntryDelete: the same invariant for soft-deleting a log entry.
func TestConcurrentEntryDelete(t *testing.T) {
	ctx := context.Background()
	a, b := twoServices(t)
	r, err := a.CreateRepair(ctx, "tester", app.RepairInput{Device: "Radio", Issue: "Dead"})
	if err != nil {
		t.Fatal(err)
	}
	e, err := a.AddRepairEntry(ctx, "tester", r.ID, "note", "first note")
	if err != nil {
		t.Fatal(err)
	}
	succeeded := runConcurrently(t, a, b, func(svc *app.Service) error {
		_, err := svc.DeleteRepairEntry(ctx, "tester", e.ID)
		return err
	})
	if succeeded != 1 {
		t.Fatalf("exactly one delete may succeed, got %d", succeeded)
	}
}

// TestConcurrentEstimateAccept: accepting the same estimate twice concurrently
// must not produce two accept transitions.
func TestConcurrentEstimateAccept(t *testing.T) {
	ctx := context.Background()
	a, b := twoServices(t)
	r, err := a.CreateRepair(ctx, "tester", app.RepairInput{Device: "Radio", Issue: "Dead"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.AddRepairPart(ctx, "tester", r.ID, app.PartInput{
		Description: "Labour", QuantityMilli: 1000, UnitPriceCents: 5000,
	}); err != nil {
		t.Fatal(err)
	}
	est, err := a.CreateEstimate(ctx, "tester", r.ID, app.EstimateInput{})
	if err != nil {
		t.Fatal(err)
	}
	succeeded := runConcurrently(t, a, b, func(svc *app.Service) error {
		_, err := svc.AcceptEstimate(ctx, "tester", est.ID)
		return err
	})
	if succeeded != 1 {
		t.Fatalf("exactly one accept may succeed, got %d", succeeded)
	}
	got, err := a.GetEstimate(ctx, est.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "accepted" {
		t.Fatalf("estimate status after the race: %s", got.Status)
	}
}
