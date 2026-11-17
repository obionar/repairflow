package web_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"repairflow/internal/app"
	"repairflow/internal/testutil"
)

func TestRepairEntryPagesKeepOlderHistoryReachable(t *testing.T) {
	f := newWeb(t)
	tech := f.login("tech", "password12345")
	repair, err := f.svc.CreateRepair(context.Background(), "setup", app.RepairInput{Device: "Long history", Issue: "Pagination"})
	testutil.Must(t, err)
	for i := 1; i <= 21; i++ {
		_, err := f.svc.AddRepairEntry(context.Background(), "setup", repair.ID, "note", fmt.Sprintf("entry-marker-%02d", i))
		testutil.Must(t, err)
	}
	path := fmt.Sprintf("/repairs/%d", repair.ID)
	first := f.get(path, tech)
	if !strings.Contains(first, "entry-marker-01") || strings.Contains(first, "entry-marker-21") {
		t.Fatal("first entry page is not bounded to the first 20 entries")
	}
	if !strings.Contains(first, `id="entries-next"`) {
		t.Fatal("first entry page has no next link")
	}
	second := f.get(path+"?entries_page=2", tech)
	if !strings.Contains(second, "entry-marker-21") || strings.Contains(second, "entry-marker-01") {
		t.Fatal("second entry page does not contain only the remaining entry")
	}
	if !strings.Contains(second, `id="entries-prev"`) || !strings.Contains(second, "?entries_page=1#saved-log") {
		t.Fatal("second entry page has no working previous link")
	}
}
