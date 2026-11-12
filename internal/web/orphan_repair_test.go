package web_test

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"repairflow/internal/app"
)

func TestOrphanRepairWebCreateAndAssignLater(t *testing.T) {
	f := newWeb(t)
	cookie := f.login("tech", "password12345")
	csrf := f.csrfFor(cookie)

	page := f.get("/repairs/new", cookie)
	if !strings.Contains(page, `id="customer_name"`) || !strings.Contains(page, `id="customer_id"`) {
		t.Fatalf("new-repair page is missing the customer-name autocomplete fields:\n%s", page)
	}
	if strings.Contains(page, "No customers yet") {
		t.Fatal("new-repair page must render without requiring an existing customer")
	}

	rec := f.post("/repairs/new", cookie, url.Values{
		"csrf_token": {csrf},
		"device":     {"Laptop"},
		"issue":      {"No customer yet"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("blank-customer create: status %d body %s", rec.Code, rec.Body.String())
	}
	list, err := f.svc.ListRepairs(context.Background(), app.RepairFilter{})
	if err != nil || list.Total != 1 || !list.Items[0].Unassigned {
		t.Fatalf("blank-customer repair was not stored as unassigned: total=%d items=%+v err=%v", list.Total, list.Items, err)
	}
	repairID := list.Items[0].ID
	detail := f.get("/repairs/"+strconv.FormatInt(repairID, 10), cookie)
	if !strings.Contains(detail, "Unassigned") {
		t.Fatalf("repair page does not show Unassigned:\n%s", detail)
	}

	customer, err := f.svc.CreateCustomer(context.Background(), "setup", app.CustomerInput{Name: "Ada Lovelace"})
	if err != nil {
		t.Fatal(err)
	}
	page = f.get("/repairs/new", cookie)
	if !strings.Contains(page, `value="Ada Lovelace"`) {
		t.Fatalf("customer autocomplete is missing the customer option:\n%s", page)
	}

	// No-JavaScript fallback: an exact customer name resolves without the
	// hidden ID field, while a partial/unknown name is refused.
	rec = f.post("/repairs/new", cookie, url.Values{
		"csrf_token":    {csrf},
		"customer_name": {"Ada Lovelace"},
		"device":        {"No-JS Laptop"},
		"issue":         {"Exact name"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("exact-name create: status %d body %s", rec.Code, rec.Body.String())
	}
	rec = f.post("/repairs/new", cookie, url.Values{
		"csrf_token":    {csrf},
		"customer_name": {"Ada"},
		"device":        {"Typo Laptop"},
		"issue":         {"Unknown customer"},
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown-name create: status %d, want 400", rec.Code)
	}

	rec = f.post("/repairs/"+strconv.FormatInt(repairID, 10), cookie, url.Values{
		"csrf_token":    {csrf},
		"customer_id":   {strconv.FormatInt(customer.ID, 10)},
		"customer_name": {"Ada Lovelace"},
		"device":        {"Laptop"},
		"issue":         {"No customer yet"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("assign customer: status %d body %s", rec.Code, rec.Body.String())
	}
	assigned, err := f.svc.GetRepair(context.Background(), repairID, false)
	if err != nil || assigned.CustomerID != customer.ID || assigned.CustomerName != "Ada Lovelace" {
		t.Fatalf("assign customer: %+v err=%v", assigned.Repair, err)
	}
	detail = f.get("/repairs/"+strconv.FormatInt(repairID, 10), cookie)
	if !strings.Contains(detail, "Ada Lovelace") || strings.Contains(detail, "Unassigned") {
		t.Fatalf("repair page did not switch to the assigned customer:\n%s", detail)
	}

	rec = f.post("/repairs/"+strconv.FormatInt(repairID, 10), cookie, url.Values{
		"csrf_token":    {csrf},
		"customer_id":   {""},
		"customer_name": {""},
		"device":        {"Laptop"},
		"issue":         {"No customer yet"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("clear customer: status %d body %s", rec.Code, rec.Body.String())
	}
	cleared, err := f.svc.GetRepair(context.Background(), repairID, false)
	if err != nil || !cleared.Unassigned || cleared.CustomerID != 0 {
		t.Fatalf("clear customer: %+v err=%v", cleared.Repair, err)
	}
}
