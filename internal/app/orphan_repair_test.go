package app_test

import (
	"context"
	"strings"
	"testing"

	"repairflow/internal/app"
	"repairflow/internal/errs"
)

func TestCreateOrphanRepairAssignAndClearCustomer(t *testing.T) {
	ctx := context.Background()
	svc := newService(t)

	orphan, err := svc.CreateRepair(ctx, "tester", app.RepairInput{
		Device: "Laptop", Issue: "No customer yet",
	})
	if err != nil {
		t.Fatalf("create orphan: %v", err)
	}
	if orphan.ID == 0 || orphan.CustomerID != 0 || !orphan.Unassigned || orphan.CustomerName != "" {
		t.Fatalf("orphan result: %+v", orphan)
	}

	detail, err := svc.GetRepair(ctx, orphan.ID, false)
	if err != nil {
		t.Fatalf("get orphan: %v", err)
	}
	if !detail.Unassigned || detail.CustomerID != 0 {
		t.Fatalf("get orphan: %+v", detail.Repair)
	}
	page, err := svc.ListRepairs(ctx, app.RepairFilter{})
	if err != nil || page.Total != 1 || len(page.Items) != 1 {
		t.Fatalf("list orphan: total=%d items=%d err=%v", page.Total, len(page.Items), err)
	}
	if !page.Items[0].Unassigned {
		t.Fatalf("orphan missing from list: %+v", page.Items[0])
	}

	customer, err := svc.CreateCustomer(ctx, "tester", app.CustomerInput{Name: "Ada Lovelace"})
	if err != nil {
		t.Fatalf("create customer: %v", err)
	}
	assigned, err := svc.UpdateRepairDetails(ctx, "tester", orphan.ID, app.RepairPatch{CustomerID: &customer.ID}, "")
	if err != nil {
		t.Fatalf("assign customer: %v", err)
	}
	if assigned.CustomerID != customer.ID || assigned.Unassigned || assigned.CustomerName != "Ada Lovelace" {
		t.Fatalf("assigned repair: %+v", assigned)
	}

	zero := int64(0)
	cleared, err := svc.UpdateRepairDetails(ctx, "tester", orphan.ID, app.RepairPatch{CustomerID: &zero}, "")
	if err != nil {
		t.Fatalf("clear customer: %v", err)
	}
	if cleared.CustomerID != 0 || !cleared.Unassigned || cleared.CustomerName != "" {
		t.Fatalf("cleared repair: %+v", cleared)
	}

	missing := int64(9999)
	if _, err := svc.UpdateRepairDetails(ctx, "tester", orphan.ID, app.RepairPatch{CustomerID: &missing}, ""); errs.CodeOf(err) != errs.CodeValidation {
		t.Fatalf("assign unknown customer: %v", err)
	}
	after, err := svc.GetRepair(ctx, orphan.ID, false)
	if err != nil || !after.Unassigned {
		t.Fatalf("failed assignment changed the repair: %+v err=%v", after.Repair, err)
	}

	activity, err := svc.GetRepair(ctx, orphan.ID, false)
	if err != nil {
		t.Fatalf("read activity: %v", err)
	}
	var auditedCustomerChanges int
	for _, a := range activity.Activity {
		if a.Action == "update" && strings.Contains(a.Detail, "customer_id") {
			auditedCustomerChanges++
		}
	}
	if auditedCustomerChanges != 2 {
		t.Fatalf("assignment/clear not audited: %+v", activity.Activity)
	}
}

func TestOrphanRepairRejectsUnknownCustomerAndNegativeID(t *testing.T) {
	ctx := context.Background()
	svc := newService(t)

	if _, err := svc.CreateRepair(ctx, "tester", app.RepairInput{
		CustomerID: 9999, Device: "Laptop", Issue: "Broken",
	}); errs.CodeOf(err) != errs.CodeValidation {
		t.Fatalf("unknown customer: %v", err)
	}
	if _, err := svc.CreateRepair(ctx, "tester", app.RepairInput{
		CustomerID: -1, Device: "Laptop", Issue: "Broken",
	}); errs.CodeOf(err) != errs.CodeValidation {
		t.Fatalf("negative customer: %v", err)
	}
}
