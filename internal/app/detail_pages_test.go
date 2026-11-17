package app_test

import (
	"bytes"
	"context"
	"fmt"
	"testing"

	"repairflow/internal/app"
	"repairflow/internal/testutil"
)

func TestRepairDetailPagesBoundRowsAndChildQueries(t *testing.T) {
	ctx := context.Background()
	svc, _ := newAttachmentService(t)
	repair, err := svc.CreateRepair(ctx, "tester", app.RepairInput{Device: "Long repair", Issue: "History"})
	testutil.Must(t, err)
	entries := make([]app.Entry, 3)
	for i := range entries {
		entries[i], err = svc.AddRepairEntry(ctx, "tester", repair.ID, "note", fmt.Sprintf("entry-%d", i+1))
		testutil.Must(t, err)
	}
	for i := 0; i < 2; i++ {
		body := fmt.Sprintf("corrected-%d", i+1)
		_, err = svc.EditRepairEntry(ctx, "tester", entries[i].ID, nil, &body)
		testutil.Must(t, err)
	}
	for i := 0; i < 3; i++ {
		_, err = svc.AddRepairPart(ctx, "tester", repair.ID, app.PartInput{Description: fmt.Sprintf("part-%d", i+1), QuantityMilli: 1000, UnitPriceCents: 100})
		testutil.Must(t, err)
	}
	attachments := make([]app.Attachment, 3)
	for i := range attachments {
		attachments[i], err = svc.AddAttachment(ctx, "tester", repair.ID, entries[0].ID, fmt.Sprintf("file-%d", i+1), bytes.NewReader([]byte{byte(i + 1)}), "", "test")
		testutil.Must(t, err)
	}

	entryPage, err := svc.ListRepairEntriesPage(ctx, repair.ID, true, 1, 2)
	testutil.Must(t, err)
	if entryPage.Total != 3 || len(entryPage.Items) != 2 || entryPage.Items[0].ID != entries[0].ID {
		t.Fatalf("entry page: %+v", entryPage)
	}
	entryPage, err = svc.ListRepairEntriesPage(ctx, repair.ID, true, 2, 2)
	testutil.Must(t, err)
	if len(entryPage.Items) != 1 || entryPage.Items[0].ID != entries[2].ID {
		t.Fatalf("second entry page: %+v", entryPage)
	}
	revisions, err := svc.ListRepairEntryRevisions(ctx, repair.ID, []int64{entries[0].ID})
	testutil.Must(t, err)
	if len(revisions) != 1 || len(revisions[entries[0].ID]) != 1 {
		t.Fatalf("revisions were not limited to visible entries: %+v", revisions)
	}

	activity, err := svc.ListRepairActivityPage(ctx, repair.ID, 1, 4)
	testutil.Must(t, err)
	if activity.Total != 12 || len(activity.Items) != 4 {
		t.Fatalf("activity page: total=%d items=%d", activity.Total, len(activity.Items))
	}
	parts, err := svc.ListRepairPartsPage(ctx, repair.ID, true, 2, 2)
	testutil.Must(t, err)
	if parts.Total != 3 || len(parts.Items) != 1 || parts.Items[0].Description != "part-3" {
		t.Fatalf("part page: %+v", parts)
	}
	files, err := svc.ListRepairAttachmentsPage(ctx, repair.ID, true, 2, 2)
	testutil.Must(t, err)
	if files.Total != 3 || len(files.Items) != 1 || files.Items[0].ID != attachments[2].ID {
		t.Fatalf("attachment page: %+v", files)
	}
	if len(files.Items[0].EntryIDs) != 1 || files.Items[0].EntryIDs[0] != entries[0].ID {
		t.Fatalf("attachment links missing from page: %+v", files.Items[0])
	}
}
