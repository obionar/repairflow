package app_test

import (
	"context"
	"testing"

	"repairflow/internal/app"
)

func TestOrphanRepairIdempotencyReplays(t *testing.T) {
	ctx := context.Background()
	svc := newService(t)
	in := app.RepairInput{Device: "Laptop", Issue: "No customer yet"}
	key := "orphan-request-0001"

	first, err := svc.CreateRepairWithKey(ctx, "tester", in, key)
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	if !first.Unassigned || first.CustomerID != 0 {
		t.Fatalf("first repair: %+v", first)
	}
	second, err := svc.CreateRepairWithKey(ctx, "tester", in, key)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if second.ID != first.ID || !second.Unassigned {
		t.Fatalf("replay returned a different repair: first=%+v second=%+v", first, second)
	}
	page, err := svc.ListRepairs(ctx, app.RepairFilter{})
	if err != nil || page.Total != 1 {
		t.Fatalf("idempotent orphan create wrote %d rows (err=%v)", page.Total, err)
	}
}
