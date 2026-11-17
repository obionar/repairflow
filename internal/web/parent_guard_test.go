package web_test

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"testing"

	"repairflow/internal/app"
	"repairflow/internal/testutil"
)

func TestNestedMutationsRefuseChildFromAnotherRepair(t *testing.T) {
	f := newWeb(t)
	ctx := context.Background()
	tech := f.login("tech", "password12345")
	csrf := f.csrfFor(tech)
	first, err := f.svc.CreateRepair(ctx, "setup", app.RepairInput{Device: "First", Issue: "First issue"})
	testutil.Must(t, err)
	second, err := f.svc.CreateRepair(ctx, "setup", app.RepairInput{Device: "Second", Issue: "Second issue"})
	testutil.Must(t, err)
	entry, err := f.svc.AddRepairEntry(ctx, "setup", second.ID, "note", "original")
	testutil.Must(t, err)
	part, err := f.svc.AddRepairPart(ctx, "setup", second.ID, app.PartInput{Description: "Fuse", QuantityMilli: 1000, UnitPriceCents: 250})
	testutil.Must(t, err)

	cases := []struct {
		name string
		path string
		form url.Values
	}{
		{"edit entry", fmt.Sprintf("/repairs/%d/entries/%d/edit", first.ID, entry.ID), url.Values{"kind": {"note"}, "body": {"wrong"}}},
		{"delete entry", fmt.Sprintf("/repairs/%d/entries/%d/delete", first.ID, entry.ID), nil},
		{"restore entry", fmt.Sprintf("/repairs/%d/entries/%d/restore", first.ID, entry.ID), nil},
		{"update part", fmt.Sprintf("/repairs/%d/parts/%d", first.ID, part.ID), url.Values{"description": {"Wrong"}, "quantity": {"1"}, "unit_price": {"9.00"}, "revision": {part.Revision}}},
		{"remove part", fmt.Sprintf("/repairs/%d/parts/%d/remove", first.ID, part.ID), nil},
		{"restore part", fmt.Sprintf("/repairs/%d/parts/%d/restore", first.ID, part.ID), nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			form := url.Values{"csrf_token": {csrf}}
			for key, values := range tc.form {
				form[key] = values
			}
			rec := f.post(tc.path, tech, form)
			if rec.Code != http.StatusNotFound {
				t.Fatalf("wrong-parent mutation returned %d, want 404: %s", rec.Code, rec.Body.String())
			}
		})
	}

	gotEntry, err := f.svc.GetEntry(ctx, entry.ID)
	testutil.Must(t, err)
	if gotEntry.Body != "original" || gotEntry.DeletedAt != "" {
		t.Fatalf("entry changed by wrong-parent requests: %+v", gotEntry)
	}
	gotPart, err := f.svc.GetRepairPart(ctx, part.ID)
	testutil.Must(t, err)
	if gotPart.Description != "Fuse" || gotPart.UnitPriceCents != 250 || gotPart.DeletedAt != "" {
		t.Fatalf("part changed by wrong-parent requests: %+v", gotPart)
	}
}
