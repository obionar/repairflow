package app_test

import (
	"context"
	"testing"

	"repairflow/internal/app"
)

// TestDashboardIncludesUnassignedRepairs: an unassigned repair (nullable
// customer_id, schema v9) must appear in the workbench and collection queues.
// An inner join to customers hides it while the counters still count it.
func TestDashboardIncludesUnassignedRepairs(t *testing.T) {
	ctx := context.Background()
	svc := newService(t)
	r, err := svc.CreateRepair(ctx, "tester", app.RepairInput{Device: "Unassigned radio", Issue: "No sound"})
	if err != nil {
		t.Fatal(err)
	}
	d, err := svc.Dashboard(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range d.Workbench {
		if item.ID == r.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("unassigned repair %d missing from the workbench queue: %+v", r.ID, d.Workbench)
	}
	if d.OpenCount != 1 {
		t.Fatalf("open count must include the unassigned repair: %d", d.OpenCount)
	}
	// The same rule in the collection queue.
	if _, err := svc.SetRepairStage(ctx, "tester", r.ID, "in_progress", false); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetRepairStage(ctx, "tester", r.ID, "repaired", false); err != nil {
		t.Fatal(err)
	}
	d, err = svc.Dashboard(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found = false
	for _, item := range d.ReadyForCollection {
		if item.ID == r.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("unassigned repaired %d missing from the collection queue: %+v", r.ID, d.ReadyForCollection)
	}
}
