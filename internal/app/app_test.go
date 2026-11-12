package app_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"repairflow/internal/app"
	"repairflow/internal/db"
	"repairflow/internal/errs"
	"repairflow/internal/files"
	"repairflow/internal/workspace"
)

func newService(t *testing.T) *app.Service {
	t.Helper()
	handle, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { handle.Close() })
	if err := db.Migrate(context.Background(), handle); err != nil {
		t.Fatal(err)
	}
	return app.New(handle)
}

func TestCustomerLifecycle(t *testing.T) {
	ctx := context.Background()
	svc := newService(t)

	c, err := svc.CreateCustomer(ctx, "tester", app.CustomerInput{Name: "  Ada Lovelace  ", Phone: "123", Email: "ada@example.invalid", Notes: "n"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if c.Name != "Ada Lovelace" || c.ID == 0 || c.Revision == "" {
		t.Fatalf("created: %+v", c)
	}

	got, err := svc.GetCustomer(ctx, c.ID)
	if err != nil || got.Revision != c.Revision {
		t.Fatalf("get: %+v %v", got, err)
	}

	// Update with correct revision: applies and rotates the revision.
	newName := "Ada L."
	updated, err := svc.UpdateCustomer(ctx, "tester", c.ID, app.CustomerPatch{Name: &newName}, got.Revision)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Name != "Ada L." || updated.Revision == got.Revision {
		t.Fatalf("update result: %+v", updated)
	}

	// Stale revision: conflict, and the record must be untouched.
	_, err = svc.UpdateCustomer(ctx, "tester", c.ID, app.CustomerPatch{Name: ptr("Mallory")}, got.Revision)
	if err == nil || errs.CodeOf(err) != errs.CodeConflict {
		t.Fatalf("expected conflict, got %v", err)
	}
	after, _ := svc.GetCustomer(ctx, c.ID)
	if after.Name != "Ada L." {
		t.Fatalf("stale write modified data: %+v", after)
	}

	// No revision: explicit last-write-wins.
	if _, err := svc.UpdateCustomer(ctx, "tester", c.ID, app.CustomerPatch{Notes: ptr("changed")}, ""); err != nil {
		t.Fatalf("unconditional update: %v", err)
	}

	history, err := svc.CustomerHistory(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	actions := []string{}
	for _, h := range history {
		actions = append(actions, h.Action)
	}
	joined := strings.Join(actions, ",")
	if !strings.Contains(joined, "create") || !strings.Contains(joined, "update") || strings.Contains(joined, "update_noop") {
		t.Fatalf("history actions: %v", actions)
	}
}

func TestCustomerValidationBounds(t *testing.T) {
	ctx := context.Background()
	svc := newService(t)

	cases := []app.CustomerInput{
		{Name: ""},
		{Name: "   "},
		{Name: strings.Repeat("x", 201)},
		{Name: "ok", Phone: strings.Repeat("p", 101)},
		{Name: "ok", Email: strings.Repeat("e", 201)},
		{Name: "ok", Notes: strings.Repeat("n", 5001)},
	}
	for i, in := range cases {
		if _, err := svc.CreateCustomer(ctx, "tester", in); errs.CodeOf(err) != errs.CodeValidation {
			t.Errorf("case %d: expected validation_error, got %v", i, err)
		}
	}

	// Unicode and control-free text round-trips exactly.
	c, err := svc.CreateCustomer(ctx, "tester", app.CustomerInput{Name: "Zoë Öztürk 日本語 🛠"})
	if err != nil {
		t.Fatalf("unicode create: %v", err)
	}
	if got, _ := svc.GetCustomer(ctx, c.ID); got.Name != "Zoë Öztürk 日本語 🛠" {
		t.Fatalf("unicode roundtrip: %q", got.Name)
	}
}

func TestCustomerSearchAndPagination(t *testing.T) {
	ctx := context.Background()
	svc := newService(t)
	for _, name := range []string{"Anna Alpha", "Bruno Beta", "Greta Gamma", "Anna Delta"} {
		if _, err := svc.CreateCustomer(ctx, "tester", app.CustomerInput{Name: name}); err != nil {
			t.Fatal(err)
		}
	}

	page1, err := svc.ListCustomers(ctx, app.CustomerFilter{Page: 1, PerPage: 2})
	if err != nil {
		t.Fatal(err)
	}
	if page1.Total != 4 || len(page1.Items) != 2 || page1.Page != 1 {
		t.Fatalf("page1: %+v", page1)
	}
	// Stable deterministic ordering: id descending.
	if page1.Items[0].ID < page1.Items[1].ID {
		t.Fatalf("ordering not id-desc: %+v", page1.Items)
	}
	page2, _ := svc.ListCustomers(ctx, app.CustomerFilter{Page: 2, PerPage: 2})
	if len(page2.Items) != 2 || page2.Items[0].ID >= page1.Items[0].ID {
		t.Fatalf("page2: %+v", page2)
	}

	found, err := svc.ListCustomers(ctx, app.CustomerFilter{Search: "Anna"})
	if err != nil || found.Total != 2 {
		t.Fatalf("search: %+v %v", found, err)
	}
	// LIKE metacharacters are literal, not wildcards.
	if r, _ := svc.ListCustomers(ctx, app.CustomerFilter{Search: "%"}); r.Total != 0 {
		t.Fatalf("wildcard search must be literal: %+v", r)
	}
}

func TestContactAndDeviceParityFields(t *testing.T) {
	ctx := context.Background()
	svc := newService(t)
	c, err := svc.CreateCustomer(ctx, "tester", app.CustomerInput{
		Name: "Customer", Address: "1 Test Street",
	})
	if err != nil {
		t.Fatal(err)
	}
	if c.Address != "1 Test Street" {
		t.Fatalf("customer address: %+v", c)
	}
	if found, err := svc.ListCustomers(ctx, app.CustomerFilter{Search: "Test Street"}); err != nil || found.Total != 1 {
		t.Fatalf("address search: %+v %v", found, err)
	}

	tk, err := svc.CreateRepair(ctx, "tester", app.RepairInput{
		CustomerID: c.ID, DeviceType: "Laptop", Brand: "Lenovo", Model: "ThinkPad T480",
		Serial: "SN-123", Issue: "No power", Notes: "Bench",
	})
	if err != nil {
		t.Fatal(err)
	}
	if tk.Device != "Lenovo ThinkPad T480" || tk.DeviceType != "Laptop" || tk.ReportedIssue != "No power" {
		t.Fatalf("device projection: %+v", tk)
	}
	for _, search := range []string{"SN-123", "ThinkPad"} {
		if found, err := svc.ListRepairs(ctx, app.RepairFilter{Search: search}); err != nil || found.Total != 1 {
			t.Fatalf("repair search %q: %+v %v", search, found, err)
		}
	}

	newModel, newAddress := "ThinkPad X1 Carbon", "2 Updated Avenue"
	updated, err := svc.UpdateRepairDetails(ctx, "tester", tk.ID, app.RepairPatch{Model: &newModel}, tk.Revision)
	if err != nil || updated.Device != "Lenovo ThinkPad X1 Carbon" {
		t.Fatalf("device update: %+v %v", updated, err)
	}
	customer, err := svc.UpdateCustomer(ctx, "tester", c.ID, app.CustomerPatch{Address: &newAddress}, c.Revision)
	if err != nil || customer.Address != newAddress {
		t.Fatalf("address update: %+v %v", customer, err)
	}

	// The original flattened API remains a compatible alias.
	legacy, err := svc.CreateRepair(ctx, "tester", app.RepairInput{CustomerID: c.ID, Device: "Mixer", Issue: "Silent"})
	if err != nil || legacy.DeviceType != "Mixer" || legacy.Device != "Mixer" {
		t.Fatalf("legacy device alias: %+v %v", legacy, err)
	}
}

func TestRepairLifecycleAndStages(t *testing.T) {
	ctx := context.Background()
	svc := newService(t)
	c, _ := svc.CreateCustomer(ctx, "tester", app.CustomerInput{Name: "Cust"})

	tk, err := svc.CreateRepair(ctx, "tester", app.RepairInput{CustomerID: c.ID, Device: "Device", Issue: "Broken"})
	if err != nil {
		t.Fatalf("create repair: %v", err)
	}
	if tk.Stage != "new" {
		t.Fatalf("initial stage: %s", tk.Stage)
	}

	// Unknown customer is a validation error (FK enforced).
	if _, err := svc.CreateRepair(ctx, "tester", app.RepairInput{CustomerID: 9999, Device: "D", Issue: "I"}); errs.CodeOf(err) != errs.CodeValidation {
		t.Fatalf("expected validation_error for missing customer, got %v", err)
	}

	// Invalid stage and invalid transition are refused.
	if _, err := svc.SetRepairStage(ctx, "tester", tk.ID, "bogus", false); errs.CodeOf(err) != errs.CodeValidation {
		t.Fatalf("bogus stage: %v", err)
	}
	if _, err := svc.SetRepairStage(ctx, "tester", tk.ID, "repaired", false); errs.CodeOf(err) != errs.CodeValidation {
		t.Fatalf("illegal transition new->repaired must be refused, got %v", err)
	}

	// Legal chain to terminal.
	for _, stage := range []string{"in_progress", "waiting_parts", "in_progress", "repaired"} {
		if _, err := svc.SetRepairStage(ctx, "tester", tk.ID, stage, false); err != nil {
			t.Fatalf("stage %s: %v", stage, err)
		}
	}

	// Terminal: same-stage is a conflict; leaving requires explicit reopen.
	if _, err := svc.SetRepairStage(ctx, "tester", tk.ID, "repaired", false); errs.CodeOf(err) != errs.CodeConflict {
		t.Fatalf("same stage: %v", err)
	}
	if _, err := svc.SetRepairStage(ctx, "tester", tk.ID, "in_progress", false); errs.CodeOf(err) != errs.CodeValidation {
		t.Fatalf("terminal without reopen must be refused, got %v", err)
	}
	if _, err := svc.SetRepairStage(ctx, "tester", tk.ID, "in_progress", true); err != nil {
		t.Fatalf("explicit reopen: %v", err)
	}

	// Work log + activity are recorded with attribution.
	if _, err := svc.AddRepairEntry(ctx, "tester", tk.ID, "work", "Replaced part"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AddRepairEntry(ctx, "tester", tk.ID, "telepathy", "x"); errs.CodeOf(err) != errs.CodeValidation {
		t.Fatalf("bad kind: %v", err)
	}
	detail, err := svc.GetRepair(ctx, tk.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Entries) != 1 {
		t.Fatalf("entries: %+v", detail.Entries)
	}
	var actions []string
	for _, a := range detail.Activity {
		actions = append(actions, a.Action)
		if a.Actor != "tester" {
			t.Fatalf("actor attribution: %+v", a)
		}
	}
	joined := strings.Join(actions, ",")
	for _, want := range []string{"create", "stage_change", "stage_reopen", "entry_add"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("activity missing %s: %v", want, actions)
		}
	}
}

func TestRepairUpdateStaleRevision(t *testing.T) {
	ctx := context.Background()
	svc := newService(t)
	c, _ := svc.CreateCustomer(ctx, "tester", app.CustomerInput{Name: "Cust"})
	tk, _ := svc.CreateRepair(ctx, "tester", app.RepairInput{CustomerID: c.ID, Device: "D", Issue: "I"})

	// Stale revision on stage change of details is refused.
	if _, err := svc.UpdateRepairDetails(ctx, "tester", tk.ID, app.RepairPatch{Device: ptr("D2")}, "bogus-revision"); errs.CodeOf(err) != errs.CodeConflict {
		t.Fatalf("expected conflict, got %v", err)
	}
	got, _ := svc.GetRepairRow(ctx, tk.ID)
	if got.Device != "D" {
		t.Fatalf("stale write modified repair: %+v", got)
	}
	if _, err := svc.UpdateRepairDetails(ctx, "tester", tk.ID, app.RepairPatch{Device: ptr("D2")}, got.Revision); err != nil {
		t.Fatalf("update with fresh revision: %v", err)
	}
}

func TestDashboardQueues(t *testing.T) {
	ctx := context.Background()
	svc := newService(t)
	c, err := svc.CreateCustomer(ctx, "tester", app.CustomerInput{Name: "Cust"})
	if err != nil {
		t.Fatal(err)
	}
	newRepair, err := svc.CreateRepair(ctx, "tester", app.RepairInput{CustomerID: c.ID, Device: "New", Issue: "I"})
	if err != nil {
		t.Fatal(err)
	}
	working, err := svc.CreateRepair(ctx, "tester", app.RepairInput{CustomerID: c.ID, Device: "Working", Issue: "I"})
	if err != nil {
		t.Fatal(err)
	}
	ready, err := svc.CreateRepair(ctx, "tester", app.RepairInput{CustomerID: c.ID, Device: "Ready", Issue: "I"})
	if err != nil {
		t.Fatal(err)
	}
	collected, err := svc.CreateRepair(ctx, "tester", app.RepairInput{CustomerID: c.ID, Device: "Collected", Issue: "I"})
	if err != nil {
		t.Fatal(err)
	}
	archived, err := svc.CreateRepair(ctx, "tester", app.RepairInput{CustomerID: c.ID, Device: "Archived", Issue: "I"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetRepairStage(ctx, "tester", working.ID, "in_progress", false); err != nil {
		t.Fatal(err)
	}
	for _, repair := range []app.Repair{ready, collected} {
		if _, err := svc.SetRepairStage(ctx, "tester", repair.ID, "in_progress", false); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := svc.SetRepairStage(ctx, "tester", ready.ID, "repaired", false); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetRepairStage(ctx, "tester", collected.ID, "no_repair", false); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetRepairPickup(ctx, "tester", collected.ID, "2026-11-11", "Customer", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ArchiveRepair(ctx, "tester", archived.ID, true); err != nil {
		t.Fatal(err)
	}

	queues, err := svc.Dashboard(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if queues.OpenCount != 2 || queues.AwaitingPickup != 1 || len(queues.Workbench) != 2 || len(queues.ReadyForCollection) != 1 {
		t.Fatalf("queue counts: %+v", queues)
	}
	if queues.Workbench[0].ID != working.ID || queues.Workbench[1].ID != newRepair.ID {
		t.Fatalf("workbench order: %+v", queues.Workbench)
	}
	if queues.ReadyForCollection[0].ID != ready.ID {
		t.Fatalf("ready queue: %+v", queues.ReadyForCollection)
	}
	for _, repair := range append(queues.Workbench, queues.ReadyForCollection...) {
		if repair.ID == collected.ID || repair.ID == archived.ID {
			t.Fatalf("collected/archived repair leaked into queues: %+v", repair)
		}
	}
}

func TestRepairListingFilters(t *testing.T) {
	ctx := context.Background()
	svc := newService(t)
	c1, _ := svc.CreateCustomer(ctx, "tester", app.CustomerInput{Name: "One"})
	c2, _ := svc.CreateCustomer(ctx, "tester", app.CustomerInput{Name: "Two"})
	t1, _ := svc.CreateRepair(ctx, "tester", app.RepairInput{CustomerID: c1.ID, Device: "Laptop", Issue: "dead"})
	t2, _ := svc.CreateRepair(ctx, "tester", app.RepairInput{CustomerID: c2.ID, Device: "Phone", Issue: "cracked"})
	svc.SetRepairStage(ctx, "tester", t2.ID, "in_progress", false)

	if p, _ := svc.ListRepairs(ctx, app.RepairFilter{Stage: "in_progress"}); p.Total != 1 || p.Items[0].ID != t2.ID {
		t.Fatalf("stage filter: %+v", p)
	}
	if p, _ := svc.ListRepairs(ctx, app.RepairFilter{CustomerID: c1.ID}); p.Total != 1 || p.Items[0].ID != t1.ID {
		t.Fatalf("customer filter: %+v", p)
	}
	if p, _ := svc.ListRepairs(ctx, app.RepairFilter{Search: "cracked"}); p.Total != 1 {
		t.Fatalf("search filter: %+v", p)
	}
	if _, err := svc.ListRepairs(ctx, app.RepairFilter{Stage: "bogus"}); errs.CodeOf(err) != errs.CodeValidation {
		t.Fatalf("bad stage filter: %v", err)
	}
	if _, err := svc.SetRepairPickup(ctx, "tester", t1.ID, "2026-11-11", "Owner", ""); err != nil {
		t.Fatal(err)
	}
	collected, inShop := true, false
	if p, _ := svc.ListRepairs(ctx, app.RepairFilter{PickedUp: &collected}); p.Total != 1 || p.Items[0].ID != t1.ID {
		t.Fatalf("picked-up filter: %+v", p)
	}
	if p, _ := svc.ListRepairs(ctx, app.RepairFilter{PickedUp: &inShop}); p.Total != 1 || p.Items[0].ID != t2.ID {
		t.Fatalf("in-shop filter: %+v", p)
	}
}

func TestNotFoundCodes(t *testing.T) {
	ctx := context.Background()
	svc := newService(t)
	if _, err := svc.GetCustomer(ctx, 42); errs.CodeOf(err) != errs.CodeNotFound || errs.ExitCode(err) != 4 {
		t.Fatalf("customer not found: %v", err)
	}
	if _, err := svc.GetRepair(ctx, 42, false); errs.CodeOf(err) != errs.CodeNotFound {
		t.Fatalf("repair not found: %v", err)
	}
	if _, err := svc.AddRepairEntry(ctx, "tester", 42, "work", "x"); errs.CodeOf(err) != errs.CodeNotFound {
		t.Fatalf("entry on missing repair: %v", err)
	}
}

func TestInTxAtomicity(t *testing.T) {
	ctx := context.Background()
	svc := newService(t)

	// A failing composed operation must roll everything back.
	err := svc.InTx(ctx, func(ctx context.Context) error {
		if _, err := svc.CreateCustomer(ctx, "tester", app.CustomerInput{Name: "Doomed"}); err != nil {
			return err
		}
		return errs.New(errs.CodeInternal, "boom")
	})
	if err == nil {
		t.Fatal("expected InTx to fail")
	}
	if p, _ := svc.ListCustomers(ctx, app.CustomerFilter{}); p.Total != 0 {
		t.Fatalf("rollback lost: %+v", p)
	}

	// A successful composed operation commits all parts.
	if err := svc.InTx(ctx, func(ctx context.Context) error {
		c, err := svc.CreateCustomer(ctx, "tester", app.CustomerInput{Name: "Kept"})
		if err != nil {
			return err
		}
		_, err = svc.CreateRepair(ctx, "tester", app.RepairInput{CustomerID: c.ID, Device: "D", Issue: "I"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if p, _ := svc.ListRepairs(ctx, app.RepairFilter{}); p.Total != 1 {
		t.Fatalf("commit lost: %+v", p)
	}
}

func TestConcurrentWriters(t *testing.T) {
	ctx := context.Background()
	handle, err := db.Open(ctx, filepath.Join(t.TempDir(), "concurrent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()
	if err := db.Migrate(ctx, handle); err != nil {
		t.Fatal(err)
	}
	svc := app.New(handle)

	// Concurrent service calls must serialize safely (single writer design
	// plus busy_timeout), not corrupt or deadlock.
	var wg sync.WaitGroup
	errsSeen := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := svc.CreateCustomer(ctx, "tester", app.CustomerInput{Name: "Concurrent"})
			if err != nil {
				errsSeen <- err
			}
		}(i)
	}
	wg.Wait()
	close(errsSeen)
	for err := range errsSeen {
		t.Fatalf("concurrent write failed: %v", err)
	}
	if p, _ := svc.ListCustomers(ctx, app.CustomerFilter{}); p.Total != 8 {
		t.Fatalf("lost writes: %+v", p)
	}
}

func TestSchemaRefusesNewerVersion(t *testing.T) {
	ctx := context.Background()
	handle, err := db.Open(ctx, filepath.Join(t.TempDir(), "future.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()
	if err := db.Migrate(ctx, handle); err != nil {
		t.Fatal(err)
	}
	if _, err := handle.Exec("INSERT INTO schema_migrations(version, name, applied_at) VALUES(999,'future','x')"); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, handle); err == nil || errs.CodeOf(err) != errs.CodeConfigVersion {
		t.Fatalf("newer schema must be refused, got %v", err)
	}
	if err := db.IntegrityCheck(ctx, handle); err != nil {
		t.Fatalf("integrity: %v", err)
	}
}

func TestDbOpenIsIdempotent(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "idem.db")
	h1, err := db.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer h1.Close()
	if err := db.Migrate(ctx, h1); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, h1); err != nil {
		t.Fatalf("second migrate must be a no-op: %v", err)
	}
	v, _ := db.CurrentVersion(ctx, h1)
	if v != db.LatestVersion() {
		t.Fatalf("version %d != latest %d", v, db.LatestVersion())
	}
}

func ptr[T any](v T) *T { return &v }

func TestPickupTracking(t *testing.T) {
	ctx := context.Background()
	svc := newService(t)
	c, _ := svc.CreateCustomer(ctx, "tester", app.CustomerInput{Name: "Cust"})
	tk, _ := svc.CreateRepair(ctx, "tester", app.RepairInput{CustomerID: c.ID, Device: "D", Issue: "I"})

	// Pickup requires both time and person; bad timestamps refused.
	if _, err := svc.SetRepairPickup(ctx, "tester", tk.ID, "2026-11-10", "", ""); errs.CodeOf(err) != errs.CodeValidation {
		t.Fatalf("missing person: %v", err)
	}
	if _, err := svc.SetRepairPickup(ctx, "tester", tk.ID, "someday", "Someone", ""); errs.CodeOf(err) != errs.CodeValidation {
		t.Fatalf("bad timestamp: %v", err)
	}

	got, err := svc.SetRepairPickup(ctx, "tester", tk.ID, "2026-11-10", "Customer Person", "collected with receipt")
	if err != nil {
		t.Fatalf("pickup: %v", err)
	}
	if got.PickupAt != "2026-11-10T00:00:00Z" || got.PickedUpBy != "Customer Person" {
		t.Fatalf("pickup fields: %+v", got)
	}

	detail, _ := svc.GetRepair(ctx, tk.ID, false)
	if detail.Activity[len(detail.Activity)-1].Action != "pickup" {
		t.Fatalf("pickup must be audited: %+v", detail.Activity)
	}
}

func TestEntryEditSoftDeleteRecover(t *testing.T) {
	ctx := context.Background()
	svc := newService(t)
	c, _ := svc.CreateCustomer(ctx, "tester", app.CustomerInput{Name: "Cust"})
	tk, _ := svc.CreateRepair(ctx, "tester", app.RepairInput{CustomerID: c.ID, Device: "D", Issue: "I"})
	e, _ := svc.AddRepairEntry(ctx, "tester", tk.ID, "work", "Wrong text")

	// Edit in place; previous content preserved as immutable revision.
	newBody := "Corrected text"
	edited, err := svc.EditRepairEntry(ctx, "tester", e.ID, nil, &newBody)
	if err != nil {
		t.Fatalf("edit: %v", err)
	}
	if edited.Body != "Corrected text" {
		t.Fatalf("edited: %+v", edited)
	}
	revs, err := svc.ListEntryRevisions(ctx, e.ID)
	if err != nil || len(revs) != 1 {
		t.Fatalf("revisions: %+v %v", revs, err)
	}
	if revs[0].PrevBody != "Wrong text" || revs[0].Actor != "tester" {
		t.Fatalf("revision snapshot: %+v", revs[0])
	}

	// No-op edit is a conflict, not a silent duplicate revision.
	if _, err := svc.EditRepairEntry(ctx, "tester", e.ID, nil, &newBody); errs.CodeOf(err) != errs.CodeConflict {
		t.Fatalf("noop edit: %v", err)
	}

	// Soft delete: hidden from normal views, visible with include-deleted.
	if _, err := svc.DeleteRepairEntry(ctx, "tester", e.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	normal, _ := svc.GetRepair(ctx, tk.ID, false)
	if len(normal.Entries) != 0 {
		t.Fatalf("deleted entry visible: %+v", normal.Entries)
	}
	all, _ := svc.GetRepair(ctx, tk.ID, true)
	if len(all.Entries) != 1 || all.Entries[0].DeletedAt == "" {
		t.Fatalf("deleted entry must be recoverable: %+v", all.Entries)
	}
	if _, err := svc.EditRepairEntry(ctx, "tester", e.ID, nil, &newBody); errs.CodeOf(err) != errs.CodeConflict {
		t.Fatalf("edit of deleted entry: %v", err)
	}

	// Restore brings it back; double delete/restore conflict.
	restored, err := svc.RestoreRepairEntry(ctx, "tester", e.ID)
	if err != nil || restored.DeletedAt != "" {
		t.Fatalf("restore: %+v %v", restored, err)
	}
	if _, err := svc.RestoreRepairEntry(ctx, "tester", e.ID); errs.CodeOf(err) != errs.CodeConflict {
		t.Fatalf("double restore: %v", err)
	}

	// History never physically deleted: revision rows survive everything.
	revs, _ = svc.ListEntryRevisions(ctx, e.ID)
	if len(revs) != 1 {
		t.Fatalf("revisions lost: %+v", revs)
	}
}

func TestArchiveSemantics(t *testing.T) {
	ctx := context.Background()
	svc := newService(t)
	c, _ := svc.CreateCustomer(ctx, "tester", app.CustomerInput{Name: "Cust"})
	tk, _ := svc.CreateRepair(ctx, "tester", app.RepairInput{CustomerID: c.ID, Device: "D", Issue: "I"})

	if _, err := svc.ArchiveRepair(ctx, "tester", tk.ID, true); err != nil {
		t.Fatalf("archive repair: %v", err)
	}
	if p, _ := svc.ListRepairs(ctx, app.RepairFilter{}); p.Total != 0 {
		t.Fatalf("archived repair still listed: %+v", p)
	}
	if p, _ := svc.ListRepairs(ctx, app.RepairFilter{IncludeArchived: true}); p.Total != 1 {
		t.Fatalf("include-archived must show it: %+v", p)
	}
	if _, err := svc.ArchiveRepair(ctx, "tester", tk.ID, true); errs.CodeOf(err) != errs.CodeConflict {
		t.Fatalf("double archive: %v", err)
	}
	if _, err := svc.ArchiveRepair(ctx, "tester", tk.ID, false); err != nil {
		t.Fatalf("unarchive: %v", err)
	}

	if _, err := svc.ArchiveCustomer(ctx, "tester", c.ID, true); err != nil {
		t.Fatalf("archive customer: %v", err)
	}
	if p, _ := svc.ListCustomers(ctx, app.CustomerFilter{}); p.Total != 0 {
		t.Fatalf("archived customer still listed: %+v", p)
	}
	history, _ := svc.CustomerHistory(ctx, c.ID)
	if history[len(history)-1].Action != "archive" {
		t.Fatalf("archive must be audited: %+v", history)
	}
}

func TestPartsLifecycle(t *testing.T) {
	ctx := context.Background()
	svc := newService(t)
	c, _ := svc.CreateCustomer(ctx, "tester", app.CustomerInput{Name: "Cust"})
	tk, _ := svc.CreateRepair(ctx, "tester", app.RepairInput{CustomerID: c.ID, Device: "D", Issue: "I"})

	cost := int64(1234)
	p, err := svc.AddRepairPart(ctx, "tester", tk.ID, app.PartInput{
		Description: "DC-in jack", QuantityMilli: 1500, Unit: "m", UnitCostCents: &cost, UnitPriceCents: 2500,
	})
	if err != nil {
		t.Fatalf("add part: %v", err)
	}
	if app.LineTotalCents(p.QuantityMilli, p.UnitPriceCents) != 3750 {
		t.Fatalf("line total wrong: %+v", p)
	}

	// Validation: zero quantity, negative price refused.
	if _, err := svc.AddRepairPart(ctx, "tester", tk.ID, app.PartInput{Description: "x", QuantityMilli: 0, UnitPriceCents: 1}); errs.CodeOf(err) != errs.CodeValidation {
		t.Fatalf("zero qty: %v", err)
	}
	if _, err := svc.AddRepairPart(ctx, "tester", tk.ID, app.PartInput{Description: "x", QuantityMilli: 1000, UnitPriceCents: -1}); errs.CodeOf(err) != errs.CodeValidation {
		t.Fatalf("negative price: %v", err)
	}

	// Stale revision refused; fresh revision applies; cost clearable.
	_, err = svc.UpdateRepairPart(ctx, "tester", p.ID, app.PartPatch{UnitPriceCents: ptr(int64(3000))}, "stale")
	if errs.CodeOf(err) != errs.CodeConflict {
		t.Fatalf("stale part update: %v", err)
	}
	upd, err := svc.UpdateRepairPart(ctx, "tester", p.ID, app.PartPatch{UnitPriceCents: ptr(int64(3000)), ClearCost: true}, p.Revision)
	if err != nil || upd.UnitCostCents != nil || upd.UnitPriceCents != 3000 {
		t.Fatalf("part update: %+v %v", upd, err)
	}

	// Soft delete + restore.
	if _, err := svc.DeleteRepairPart(ctx, "tester", p.ID); err != nil {
		t.Fatalf("part remove: %v", err)
	}
	if list, _ := svc.ListRepairParts(ctx, tk.ID, false); len(list) != 0 {
		t.Fatalf("removed part visible: %+v", list)
	}
	if list, _ := svc.ListRepairParts(ctx, tk.ID, true); len(list) != 1 {
		t.Fatalf("removed part not recoverable: %+v", list)
	}
	if _, err := svc.RestoreRepairPart(ctx, "tester", p.ID); err != nil {
		t.Fatalf("part restore: %v", err)
	}
}

func TestPartRejectsLineTotalOverflow(t *testing.T) {
	ctx := context.Background()
	svc := newService(t)
	c, err := svc.CreateCustomer(ctx, "tester", app.CustomerInput{Name: "Cust"})
	if err != nil {
		t.Fatal(err)
	}
	tk, err := svc.CreateRepair(ctx, "tester", app.RepairInput{CustomerID: c.ID, Device: "D", Issue: "I"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.AddRepairPart(ctx, "tester", tk.ID, app.PartInput{
		Description: "Extreme", QuantityMilli: 1_000_000_000, UnitPriceCents: 10_000_000_000,
	})
	if errs.CodeOf(err) != errs.CodeValidation {
		t.Fatalf("overflowing line total must be refused before storage: %v", err)
	}
}

func TestEstimatesSnapshotAndAccept(t *testing.T) {
	ctx := context.Background()
	svc := newService(t)
	c, _ := svc.CreateCustomer(ctx, "tester", app.CustomerInput{Name: "Cust"})
	tk, _ := svc.CreateRepair(ctx, "tester", app.RepairInput{CustomerID: c.ID, Device: "D", Issue: "I"})

	// Estimate without parts is refused.
	if _, err := svc.CreateEstimate(ctx, "tester", tk.ID, app.EstimateInput{}); errs.CodeOf(err) != errs.CodeValidation {
		t.Fatalf("empty estimate: %v", err)
	}

	cost := int64(500)
	p1, _ := svc.AddRepairPart(ctx, "tester", tk.ID, app.PartInput{Description: "Screen", QuantityMilli: 1000, UnitCostCents: &cost, UnitPriceCents: 8000})
	svc.AddRepairPart(ctx, "tester", tk.ID, app.PartInput{Description: "Adhesive", QuantityMilli: 2000, UnitPriceCents: 250})

	e1, err := svc.CreateEstimate(ctx, "tester", tk.ID, app.EstimateInput{DiscountKind: "percent", DiscountValue: 10})
	if err != nil {
		t.Fatalf("create estimate: %v", err)
	}
	// 8000 + 500 = 8500 subtotal; 10% = 850; total 7650.
	if e1.SubtotalCents != 8500 || e1.DiscountCents != 850 || e1.TotalCents != 7650 || e1.Version != 1 || len(e1.Lines) != 2 {
		t.Fatalf("estimate totals: %+v", e1)
	}

	// Snapshot immutability: later part price changes do NOT touch it.
	svc.UpdateRepairPart(ctx, "tester", p1.ID, app.PartPatch{UnitPriceCents: ptr(int64(99999))}, "")
	again, _ := svc.GetEstimate(ctx, e1.ID)
	if again.TotalCents != 7650 || again.Lines[0].UnitPriceCents != 8000 {
		t.Fatalf("estimate mutated after part change: %+v", again)
	}

	// v2 snapshots the CURRENT parts (price now 999.99). Over-subtotal amount
	// discounts are refused outright — never silently capped or negative.
	if _, err := svc.CreateEstimate(ctx, "tester", tk.ID, app.EstimateInput{DiscountKind: "amount", DiscountValue: 200000}); errs.CodeOf(err) != errs.CodeValidation {
		t.Fatalf("over-subtotal discount must be refused, got %v", err)
	}
	e2, err := svc.CreateEstimate(ctx, "tester", tk.ID, app.EstimateInput{DiscountKind: "amount", DiscountValue: 100000})
	if err != nil {
		t.Fatalf("v2: %v", err)
	}
	if e2.SubtotalCents != 100499 || e2.TotalCents != 499 {
		t.Fatalf("v2 totals: %+v", e2)
	}
	acc, err := svc.AcceptEstimate(ctx, "tester", e2.ID)
	if err != nil || acc.Status != "accepted" {
		t.Fatalf("accept: %+v %v", acc, err)
	}
	// Accepting v2 supersedes only an earlier *accepted* version;
	// draft snapshots keep their status and never change.
	e1After, _ := svc.GetEstimate(ctx, e1.ID)
	if e1After.Status != "draft" || e1After.TotalCents != 7650 {
		t.Fatalf("v1 draft must remain immutable: %+v", e1After)
	}
	if _, err := svc.AcceptEstimate(ctx, "tester", e2.ID); errs.CodeOf(err) != errs.CodeConflict {
		t.Fatalf("double accept: %v", err)
	}

	// Activity records the commercial events.
	detail, _ := svc.GetRepair(ctx, tk.ID, false)
	joined := ""
	for _, a := range detail.Activity {
		joined += a.Action + ","
	}
	for _, want := range []string{"part_add", "part_update", "estimate_create", "estimate_accept"} {
		if strings.Count(joined, want) == 0 {
			t.Fatalf("activity missing %s: %s", want, joined)
		}
	}
	if strings.Count(joined, "estimate_create") != 2 {
		t.Fatalf("expected two estimate snapshots: %s", joined)
	}
}

func newAttachmentService(t *testing.T) (*app.Service, string) {
	t.Helper()
	home := t.TempDir()
	handle, err := db.Open(context.Background(), filepath.Join(home, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { handle.Close() })
	if err := db.Migrate(context.Background(), handle); err != nil {
		t.Fatal(err)
	}
	workspaceHomeForTest = home
	return app.New(handle).WithStore(files.New(home)), home
}

func TestAttachmentLifecycle(t *testing.T) {
	ctx := context.Background()
	svc, _ := newAttachmentService(t)
	c, _ := svc.CreateCustomer(ctx, "tester", app.CustomerInput{Name: "Cust"})
	tk, _ := svc.CreateRepair(ctx, "tester", app.RepairInput{CustomerID: c.ID, Device: "D", Issue: "I"})
	e, _ := svc.AddRepairEntry(ctx, "tester", tk.ID, "work", "Diagnostics")

	payload := []byte("PNG-ish bytes \x89PNG fake")
	a, err := svc.AddAttachment(ctx, "tester", tk.ID, e.ID, "../../etc/passwd", bytes.NewReader(payload), "intake photo", "cli")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if a.DisplayName != "passwd" || a.SizeBytes != int64(len(payload)) || a.State != "attached" {
		t.Fatalf("attachment: %+v", a)
	}
	if len(a.EntryIDs) != 1 || a.EntryIDs[0] != e.ID {
		t.Fatalf("entry link: %+v", a)
	}
	if a.PreviewSafe {
		t.Fatalf("unknown media type must not be preview-safe: %s", a.MediaType)
	}

	// Byte-exact retrieval with hash verification (agent contract).
	rc, meta, err := svc.OpenBytes(ctx, a.ID)
	if err != nil {
		t.Fatalf("open bytes: %v", err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if !bytes.Equal(got, payload) || meta.SHA256 != a.SHA256 {
		t.Fatal("bytes/hash mismatch on retrieval")
	}

	// Caption edit is audited.
	upd, err := svc.SetCaption(ctx, "tester", a.ID, "cracked screen close-up")
	if err != nil || upd.Caption != "cracked screen close-up" {
		t.Fatalf("caption: %+v %v", upd, err)
	}

	// Cross-repair entry links are refused.
	tk2, _ := svc.CreateRepair(ctx, "tester", app.RepairInput{CustomerID: c.ID, Device: "D2", Issue: "I2"})
	e2, _ := svc.AddRepairEntry(ctx, "tester", tk2.ID, "work", "other repair")
	if _, err := svc.LinkEntry(ctx, "tester", a.ID, e2.ID); errs.CodeOf(err) != errs.CodeValidation {
		t.Fatalf("cross-repair link must be refused: %v", err)
	}
	// Same-repair second link works (one attachment, many entries).
	if _, err := svc.LinkEntry(ctx, "tester", a.ID, e.ID); err == nil {
		// linking the same entry twice is idempotent (INSERT OR IGNORE)
	} else {
		t.Fatalf("re-link: %v", err)
	}
	if _, err := svc.UnlinkEntry(ctx, "tester", a.ID, e.ID); err != nil {
		t.Fatalf("unlink: %v", err)
	}
	if _, err := svc.UnlinkEntry(ctx, "tester", a.ID, e.ID); errs.CodeOf(err) != errs.CodeConflict {
		t.Fatalf("double unlink: %v", err)
	}
}

func TestAttachmentRetentionGraceAndPurge(t *testing.T) {
	ctx := context.Background()
	svc, home := newAttachmentService(t)
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	svc.SetClock(func() time.Time { return now })

	c, _ := svc.CreateCustomer(ctx, "tester", app.CustomerInput{Name: "Cust"})
	tk, _ := svc.CreateRepair(ctx, "tester", app.RepairInput{CustomerID: c.ID, Device: "D", Issue: "I"})
	a, err := svc.AddAttachment(ctx, "tester", tk.ID, 0, "firmware.bin", bytes.NewReader([]byte("\x7fELF firmware")), "", "external-agent")
	if err != nil {
		t.Fatalf("add: %v", err)
	}

	// Purge of an attached file is refused outright.
	if _, err := svc.Purge(ctx, "tester", a.ID, true); errs.CodeOf(err) != errs.CodeConflict {
		t.Fatalf("purge attached: %v", err)
	}

	det, err := svc.Detach(ctx, "tester", a.ID)
	if err != nil {
		t.Fatalf("detach: %v", err)
	}
	if det.State != "detached" || det.PurgeAfter == "" {
		t.Fatalf("detached state: %+v", det)
	}
	// Bytes retained in the archive (recoverable) but hidden from normal
	// views; visible with includeAll.
	if list, _ := svc.ListRepairAttachments(ctx, tk.ID, false); len(list) != 0 {
		t.Fatalf("detached attachment must leave the normal view: %+v", list)
	}
	if list, _ := svc.ListRepairAttachments(ctx, tk.ID, true); len(list) != 1 || list[0].State != "detached" {
		t.Fatalf("detached attachment must remain recoverable: %+v", list)
	}

	// Purge without confirmation is refused.
	if _, err := svc.Purge(ctx, "tester", a.ID, false); errs.CodeOf(err) != errs.CodeValidation {
		t.Fatalf("unconfirmed purge: %v", err)
	}
	// Within the 30-day grace, purge is refused.
	if _, err := svc.Purge(ctx, "tester", a.ID, true); errs.CodeOf(err) != errs.CodeConflict {
		t.Fatalf("early purge must be refused: %v", err)
	}

	// Restore makes it visible again; detach again for the purge path.
	if _, err := svc.Restore(ctx, "tester", a.ID); err != nil {
		t.Fatalf("restore: %v", err)
	}
	svc.Detach(ctx, "tester", a.ID)

	// After the grace period, explicit purge destroys bytes but keeps the
	// audited metadata row.
	now = now.Add(31 * 24 * time.Hour)
	purged, err := svc.Purge(ctx, "tester", a.ID, true)
	if err != nil {
		t.Fatalf("purge after grace: %v", err)
	}
	if purged.State != "purged" || purged.SHA256 == "" {
		t.Fatalf("purged row: %+v", purged)
	}
	if _, _, err := svc.OpenBytes(ctx, a.ID); errs.CodeOf(err) != errs.CodeNotFound {
		t.Fatalf("purged bytes must be gone: %v", err)
	}
	// No automatic purge exists: state is only changed by explicit calls.
	detail, _ := svc.GetRepair(ctx, tk.ID, false)
	joined := ""
	for _, act := range detail.Activity {
		joined += act.Action + ","
	}
	for _, want := range []string{"attachment_add", "attachment_detach", "attachment_restore", "attachment_detach", "attachment_purge"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("audit missing %s: %s", want, joined)
		}
	}
	_ = home
}

func TestAttachmentPurgeRefusesMalformedRetentionMetadata(t *testing.T) {
	ctx := context.Background()
	svc, _ := newAttachmentService(t)
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	svc.SetClock(func() time.Time { return now })
	c, err := svc.CreateCustomer(ctx, "tester", app.CustomerInput{Name: "Cust"})
	if err != nil {
		t.Fatal(err)
	}
	tk, err := svc.CreateRepair(ctx, "tester", app.RepairInput{CustomerID: c.ID, Device: "D", Issue: "I"})
	if err != nil {
		t.Fatal(err)
	}
	a, err := svc.AddAttachment(ctx, "tester", tk.ID, 0, "evidence.bin", bytes.NewReader([]byte("original bytes")), "", "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Detach(ctx, "tester", a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.DB().Exec(`UPDATE attachments SET purge_after = 'not-a-time' WHERE id = ?`, a.ID); err != nil {
		t.Fatal(err)
	}
	now = now.Add(31 * 24 * time.Hour)
	if _, err := svc.Purge(ctx, "tester", a.ID, true); errs.CodeOf(err) != errs.CodeInternal {
		t.Fatalf("malformed retention metadata must refuse purge: %v", err)
	}
	rc, meta, err := svc.OpenBytes(ctx, a.ID)
	if err != nil {
		t.Fatalf("refused purge must preserve bytes: %v", err)
	}
	got, readErr := io.ReadAll(rc)
	closeErr := rc.Close()
	if readErr != nil || closeErr != nil || string(got) != "original bytes" || meta.State != "detached" {
		t.Fatalf("attachment changed after refused purge: %q %+v read=%v close=%v", got, meta, readErr, closeErr)
	}
}

func TestAttachmentReconcile(t *testing.T) {
	ctx := context.Background()
	svc, _ := newAttachmentService(t)
	c, _ := svc.CreateCustomer(ctx, "tester", app.CustomerInput{Name: "Cust"})
	tk, _ := svc.CreateRepair(ctx, "tester", app.RepairInput{CustomerID: c.ID, Device: "D", Issue: "I"})

	a, err := svc.AddAttachment(ctx, "tester", tk.ID, 0, "manual.pdf", bytes.NewReader([]byte("%PDF-1.4 fake")), "", "cli")
	if err != nil {
		t.Fatal(err)
	}
	missing, orphans, err := svc.ReconcileAttachments(ctx)
	if err != nil || len(missing) != 0 || len(orphans) != 0 {
		t.Fatalf("clean workspace must reconcile: %v %v %v", missing, orphans, err)
	}

	// Delete bytes behind the service's back -> detected as missing.
	name, _ := svc.AttachmentStorageName(ctx, a.ID)
	if err := os.Remove(filepath.Join(mustWorkspaceHome(t), "files", "tickets", fmt.Sprintf("%d", tk.ID), name)); err != nil {
		// workspace home is the temp dir used in newAttachmentService; recover via store path
		t.Fatalf("remove bytes: %v", err)
	}
	missing, _, err = svc.ReconcileAttachments(ctx)
	if err != nil || len(missing) != 1 {
		t.Fatalf("missing bytes must be detected: %v %v", missing, err)
	}
}

// mustWorkspaceHome returns the temp workspace used by the last attachment
// service helper call in this test file's flow (set explicitly in tests).
var workspaceHomeForTest string

func mustWorkspaceHome(t *testing.T) string {
	if workspaceHomeForTest == "" {
		t.Fatal("workspaceHomeForTest not set")
	}
	return workspaceHomeForTest
}

// ---- fault injection ------------------------------------------------------

// Fault: process termination before an SQLite commit. The abandoned write
// transaction must leave no trace when the database is reopened.
func TestCrashBeforeCommitRollsBack(t *testing.T) {
	ctx := context.Background()
	home := filepath.Join(t.TempDir(), "ws")
	svc := reopenService(t, home)

	// Simulated crash: perform writes inside a transaction and never commit.
	handle, err := db.Open(ctx, db.Path(home))
	if err != nil {
		t.Fatal(err)
	}
	tx, err := handle.Begin()
	if err != nil {
		t.Fatal(err)
	}
	tx.Exec(`INSERT INTO customers(name, created_at, updated_at) VALUES('Ghost', 'x', 'y')`)
	// crash: neither commit nor rollback; just drop the connection.
	handle.Close()

	svc2 := reopenService(t, home)
	if p, _ := svc2.ListCustomers(ctx, app.CustomerFilter{}); p.Total != 0 {
		t.Fatalf("uncommitted write survived a crash: %+v", p)
	}
	_ = svc
}

// Fault: crash after file rename but before the database row.
// The orphan bytes must be detectable evidence, not silent corruption.
func TestCrashAfterRenameLeavesDetectableOrphan(t *testing.T) {
	ctx := context.Background()
	svc, home := newAttachmentService(t)
	c, _ := svc.CreateCustomer(ctx, "tester", app.CustomerInput{Name: "Cust"})
	tk, _ := svc.CreateRepair(ctx, "tester", app.RepairInput{CustomerID: c.ID, Device: "D", Issue: "I"})

	// Simulate the crash window: bytes published by the file layer, but the
	// metadata row was never committed.
	store := files.New(home)
	stored, err := store.Add(tk.ID, bytes.NewReader([]byte("orphaned bytes")), 0)
	if err != nil {
		t.Fatal(err)
	}
	missing, orphans, err := svc.ReconcileAttachments(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 0 || len(orphans) != 1 {
		t.Fatalf("orphan must be reported: missing=%v orphans=%v", missing, orphans)
	}
	if !strings.Contains(orphans[0], stored.StorageName) {
		t.Fatalf("orphan report must name the file: %v", orphans)
	}
}

// Cleanup removes stale staging and cache but never user files.
func TestWorkspaceCleanupNeverTouchesUserData(t *testing.T) {
	home := filepath.Join(t.TempDir(), "ws")
	if _, err := workspace.Init(home); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(home, "run", "upload-stale.part")
	fresh := filepath.Join(home, "run", "upload-fresh.part")
	os.WriteFile(stale, []byte("stale"), 0o600)
	os.WriteFile(fresh, []byte("fresh"), 0o600)
	old := time.Now().Add(-2 * time.Hour)
	os.Chtimes(stale, old, old)
	os.WriteFile(filepath.Join(home, "cache", "thumb"), []byte("c"), 0o600)
	userFile := filepath.Join(home, "files", "tickets", "1", strings.Repeat("a", 32))
	os.MkdirAll(filepath.Dir(userFile), 0o700)
	os.WriteFile(userFile, []byte("user bytes"), 0o600)

	res, err := workspace.Cleanup(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.StagingRemoved) != 1 || res.StagingRemoved[0] != filepath.Join("run", "upload-stale.part") {
		t.Fatalf("staging cleanup: %+v", res)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatal("fresh staging must be kept (in-flight operation)")
	}
	if _, err := os.Stat(userFile); err != nil {
		t.Fatal("cleanup must never touch user files")
	}
	if _, err := os.Stat(filepath.Join(home, "cache", "thumb")); err == nil {
		t.Fatal("cache should be removed")
	}
}

// TestAttachmentPreviewBounds checks that preview_safe stays a media-type
// judgement, preview_inline additionally requires the bounded size, and the
// stored bytes are never altered by either flag.
func TestAttachmentPreviewBounds(t *testing.T) {
	ctx := context.Background()
	svc, _ := newAttachmentService(t)
	c, _ := svc.CreateCustomer(ctx, "tester", app.CustomerInput{Name: "Cust"})
	tk, _ := svc.CreateRepair(ctx, "tester", app.RepairInput{CustomerID: c.ID, Device: "D", Issue: "I"})

	small := append([]byte("\xff\xd8\xff\xe0"), bytes.Repeat([]byte{0x11}, 32)...)
	a, err := svc.AddAttachment(ctx, "tester", tk.ID, 0, "small.jpg", bytes.NewReader(small), "", "test")
	if err != nil {
		t.Fatal(err)
	}
	if a.MediaType != "image/jpeg" || !a.PreviewSafe || !a.PreviewInline {
		t.Fatalf("small jpeg preview flags: %+v", a)
	}

	// A genuinely preview-safe image above the preview bound: original bytes
	// kept, downloadable, but not rendered inline.
	big := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0x00}, int(app.PreviewMaxBytes))...)
	b, err := svc.AddAttachment(ctx, "tester", tk.ID, 0, "big.png", bytes.NewReader(big), "", "test")
	if err != nil {
		t.Fatal(err)
	}
	if b.MediaType != "image/png" || !b.PreviewSafe {
		t.Fatalf("large png media type: %+v", b)
	}
	if b.PreviewInline {
		t.Fatalf("large png must not preview inline: %+v", b)
	}
	if b.SizeBytes != int64(len(big)) || b.SHA256 == "" {
		t.Fatalf("large png metadata: %+v", b)
	}
	rc, meta, err := svc.OpenBytes(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(rc)
	rc.Close()
	if err != nil || !bytes.Equal(got, big) {
		t.Fatalf("original bytes must be unchanged: len=%d err=%v", len(got), err)
	}
	if meta.SHA256 != b.SHA256 {
		t.Fatal("hash mismatch on bounded preview file")
	}
	// Listing keeps the distinction and never rewrites the bytes.
	list, err := svc.ListRepairAttachments(ctx, tk.ID, false)
	if err != nil || len(list) != 2 {
		t.Fatalf("list: %+v %v", list, err)
	}
	for _, got := range list {
		if got.ID == b.ID && got.PreviewInline {
			t.Fatal("list must not report the large image as inline")
		}
		if got.ID == a.ID && !got.PreviewInline {
			t.Fatal("list must report the small image as inline")
		}
	}
}
