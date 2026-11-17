package web_test

import (
	"context"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"repairflow/internal/app"
	"repairflow/internal/testutil"
)

func TestCorrectionAndPickupDraftsSurviveValidation(t *testing.T) {
	f := newWeb(t)
	ctx := context.Background()
	tech := f.login("tech", "password12345")
	csrf := f.csrfFor(tech)
	repair, err := f.svc.CreateRepair(ctx, "setup", app.RepairInput{Device: "Bench Device", Issue: "Intermittent fault"})
	testutil.Must(t, err)
	entry, err := f.svc.AddRepairEntry(ctx, "setup", repair.ID, "work", "Saved entry")
	testutil.Must(t, err)
	path := fmt.Sprintf("/repairs/%d", repair.ID)
	draft := "Correction <not saved> & still editable"
	rec := f.post(fmt.Sprintf("%s/entries/%d/edit", path, entry.ID), tech, url.Values{
		"csrf_token": {csrf}, "kind": {"invalid"}, "body": {draft},
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid correction: %d %s", rec.Code, rec.Body.String())
	}
	for _, want := range []string{html.EscapeString(draft), `id="entry-edit-panel-1" open`, `name="kind" value="work"`} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("rejected correction missing %q", want)
		}
	}
	got, err := f.svc.GetRepair(ctx, repair.ID, false)
	testutil.Must(t, err)
	if got.Entries[0].Body != "Saved entry" {
		t.Fatal("rejected correction changed saved text")
	}
	revisions, err := f.svc.ListRepairEntryRevisions(ctx, repair.ID, nil)
	testutil.Must(t, err)
	if len(revisions) != 0 {
		t.Fatal("rejected correction created an audit revision")
	}

	rec = f.post(path+"/pickup", tech, url.Values{
		"csrf_token": {csrf}, "picked_up_by": {""}, "pickup_notes": {"Keep <handover> notes"},
	})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `value="Keep &lt;handover&gt; notes"`) {
		t.Fatalf("rejected pickup lost notes: %d %s", rec.Code, rec.Body.String())
	}
	got, err = f.svc.GetRepair(ctx, repair.ID, false)
	testutil.Must(t, err)
	if got.PickupAt != "" || got.PickupNotes != "" {
		t.Fatal("rejected pickup changed saved values")
	}

	rec = f.post(path+"/pickup", tech, url.Values{
		"csrf_token": {csrf}, "picked_up_by": {"Keep collector"}, "pickup_notes": {strings.Repeat("x", 5001)},
	})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `value="Keep collector"`) {
		t.Fatalf("rejected pickup lost collector: %d", rec.Code)
	}

	// The re-rendered draft remains a correction, not a new log entry.
	rec = f.post(fmt.Sprintf("%s/entries/%d/edit", path, entry.ID), tech, url.Values{
		"csrf_token": {csrf}, "kind": {"work"}, "body": {draft},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("correction retry: %d %s", rec.Code, rec.Body.String())
	}
	got, err = f.svc.GetRepair(ctx, repair.ID, false)
	testutil.Must(t, err)
	if len(got.Entries) != 1 || got.Entries[0].Body != draft {
		t.Fatalf("correction retry did not edit the original: %+v", got.Entries)
	}

	// A concurrent deletion still exposes the rejected text for copying, without
	// pretending the deleted entry can be edited or recreating it automatically.
	_, err = f.svc.DeleteRepairEntry(ctx, "setup", entry.ID)
	testutil.Must(t, err)
	rec = f.post(fmt.Sprintf("%s/entries/%d/edit", path, entry.ID), tech, url.Values{
		"csrf_token": {csrf}, "kind": {"work"}, "body": {"Copy this rejected correction"},
	})
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `readonly>Copy this rejected correction</textarea>`) {
		t.Fatalf("concurrent deletion lost correction: %d", rec.Code)
	}
}
