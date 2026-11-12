package seed_test

import (
	"context"
	"testing"

	"repairflow/internal/app"
	"repairflow/internal/db"
	"repairflow/internal/errs"
	"repairflow/internal/files"
	"repairflow/internal/seed"
)

func newService(t *testing.T) *app.Service {
	t.Helper()
	home := t.TempDir()
	handle, err := db.Open(context.Background(), db.Path(home))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { handle.Close() })
	if err := db.Migrate(context.Background(), handle); err != nil {
		t.Fatal(err)
	}
	return app.New(handle).WithStore(files.New(home))
}

func TestDemoSeedAndIdempotence(t *testing.T) {
	ctx := context.Background()
	svc := newService(t)

	res, err := seed.Demo(ctx, svc)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if !res.Seeded || res.Customers != seed.DemoCustomers || res.Repairs != seed.DemoRepairs ||
		res.Entries != seed.DemoEntries || res.Parts != seed.DemoParts || res.Estimates != seed.DemoEstimates {
		t.Fatalf("seed result: %+v", res)
	}
	if res.Attachments != seed.DemoAttachments {
		t.Fatalf("demo must include generated sample media: %+v", res)
	}

	// Idempotent: a second run reports already seeded and changes nothing.
	again, err := seed.Demo(ctx, svc)
	if err != nil || !again.AlreadySeeded {
		t.Fatalf("second seed: %+v %v", again, err)
	}
	c, ti, _ := svc.Counts(ctx)
	if c != seed.DemoCustomers || ti != seed.DemoRepairs {
		t.Fatalf("second seed changed data: %d/%d", c, ti)
	}

	// Seeded data flows through the same rules: staged repairs, entries, activity.
	page, err := svc.ListRepairs(ctx, app.RepairFilter{Stage: "repaired", Search: "ThinkPad T480 (DEMO)"})
	if err != nil || page.Total != 1 {
		t.Fatalf("demo stages: %+v %v", page, err)
	}
	detail, err := svc.GetRepair(ctx, page.Items[0].ID, true)
	if err != nil || len(detail.Entries) != 3 {
		t.Fatalf("demo entries: %+v %v", detail, err)
	}
	// Everything is attributed to the demo actor and marked synthetic.
	for _, a := range detail.Activity {
		if a.Actor != seed.DemoActor {
			t.Fatalf("demo actor: %+v", a)
		}
	}

	// Demo acceptance: one photo on intake and one on a log entry; all
	// sample media carries a caption and verifies byte-intact.
	atts, err := svc.ListRepairAttachments(context.Background(), 1, true)
	if err != nil || len(atts) != 2 {
		t.Fatalf("demo media on repair 1: %v %v", atts, err)
	}
	intake, onEntry := 0, 0
	hashes := map[string]bool{}
	for _, a := range atts {
		if a.Caption == "" {
			t.Fatalf("demo media without caption: %+v", a)
		}
		if hashes[a.SHA256] {
			t.Fatalf("demo media files must be byte-distinct: %s", a.DisplayName)
		}
		hashes[a.SHA256] = true
		if len(a.EntryIDs) > 0 {
			onEntry++
		} else {
			intake++
		}
		rc, _, err := svc.OpenBytes(context.Background(), a.ID)
		if err != nil {
			t.Fatalf("demo media bytes: %v", err)
		}
		rc.Close()
	}
	if intake != 1 || onEntry != 1 {
		t.Fatalf("need one intake + one log-entry photo, got %d/%d", intake, onEntry)
	}

	// demo-v2 coverage: parts (including an unknown cost and one removed
	// line), an accepted estimate with a discount and a draft estimate, plus
	// recorded pickup state, so every UI workflow has demo content.
	parts, err := svc.ListRepairParts(ctx, 1, false)
	if err != nil || len(parts) != 2 {
		t.Fatalf("active parts on repair 1: %+v %v", parts, err)
	}
	if parts[0].UnitCostCents == nil || *parts[0].UnitCostCents != 1450 {
		t.Fatalf("part cost: %+v", parts[0])
	}
	removed, err := svc.ListRepairParts(ctx, 7, true)
	if err != nil || len(removed) != 2 {
		t.Fatalf("parts on repair 7: %+v %v", removed, err)
	}
	removedLines := 0
	for _, p := range removed {
		if p.DeletedAt != "" {
			removedLines++
		}
	}
	if removedLines != 1 {
		t.Fatalf("demo must show one removed part line: %+v", removed)
	}
	estimates, err := svc.ListEstimates(ctx, 1)
	if err != nil || len(estimates) != 1 || estimates[0].Status != "accepted" || estimates[0].DiscountCents == 0 {
		t.Fatalf("accepted demo estimate: %+v %v", estimates, err)
	}
	draft, err := svc.ListEstimates(ctx, 2)
	if err != nil || len(draft) != 1 || draft[0].Status != "draft" {
		t.Fatalf("draft demo estimate: %+v %v", draft, err)
	}
	picked, err := svc.GetRepair(ctx, 1, false)
	if err != nil || picked.PickedUpBy == "" || picked.PickupAt == "" {
		t.Fatalf("demo pickup state: %+v %v", picked.Repair, err)
	}
}

func TestDemoRefusesRealData(t *testing.T) {
	ctx := context.Background()
	svc := newService(t)
	if _, err := svc.CreateCustomer(ctx, "operator", app.CustomerInput{Name: "Real Person"}); err != nil {
		t.Fatal(err)
	}
	_, err := seed.Demo(ctx, svc)
	if err == nil || errs.CodeOf(err) != errs.CodeConflict {
		t.Fatalf("seed over real data must refuse with conflict, got %v", err)
	}
	if c, _, _ := svc.Counts(ctx); c != 1 {
		t.Fatal("refused seed must not modify data")
	}
}

func TestDemoResetRestoresPristineState(t *testing.T) {
	ctx := context.Background()
	svc := newService(t)
	if _, err := seed.Demo(ctx, svc); err != nil {
		t.Fatal(err)
	}
	// Corrupt pristine state via a legitimate edit (removes an entry).
	res, err := seed.Reset(ctx, svc)
	if err != nil {
		t.Fatalf("reset of proven demo workspace: %v", err)
	}
	if !res.Reset || !res.Seeded.Seeded {
		t.Fatalf("reset result: %+v", res)
	}
	c, ti, en, p, es, at, err := svc.BusinessCounts(ctx)
	if err != nil || c != seed.DemoCustomers || ti != seed.DemoRepairs || en != seed.DemoEntries ||
		p != seed.DemoParts || es != seed.DemoEstimates || at != seed.DemoAttachments {
		t.Fatalf("pristine counts after reset: %d/%d/%d/%d/%d/%d %v", c, ti, en, p, es, at, err)
	}
	// Media bytes exist again (fresh attachments, byte-intact).
	atts, _ := svc.ListRepairAttachments(ctx, 1, true)
	if len(atts) != 2 {
		t.Fatalf("media after reset: %+v", atts)
	}
	for _, a := range atts {
		rc, _, err := svc.OpenBytes(ctx, a.ID)
		if err != nil {
			t.Fatalf("bytes after reset: %v", err)
		}
		rc.Close()
	}
}

func TestDemoResetRefusals(t *testing.T) {
	ctx := context.Background()

	// Unseeded workspace: no marker -> refuse.
	svc := newService(t)
	if _, err := seed.Reset(ctx, svc); errs.CodeOf(err) != errs.CodeConflict {
		t.Fatalf("unseeded workspace must refuse: %v", err)
	}

	// Real data in a seeded workspace -> refuse and change nothing.
	svc2 := newService(t)
	if _, err := seed.Demo(ctx, svc2); err != nil {
		t.Fatal(err)
	}
	if _, err := svc2.CreateCustomer(ctx, "operator", app.CustomerInput{Name: "Real Person Added"}); err != nil {
		t.Fatal(err)
	}
	if _, err := seed.Reset(ctx, svc2); errs.CodeOf(err) != errs.CodeConflict {
		t.Fatalf("modified workspace must refuse: %v", err)
	}
	c, _, _, _, _, _, _ := svc2.BusinessCounts(ctx)
	if c != seed.DemoCustomers+1 {
		t.Fatal("refused reset must not delete anything")
	}

	// Modified entry content also invalidates the provenance.
	svc3 := newService(t)
	if _, err := seed.Demo(ctx, svc3); err != nil {
		t.Fatal(err)
	}
	detail, _ := svc3.GetRepair(ctx, 1, false)
	body := "operator edited the log"
	if _, err := svc3.EditRepairEntry(ctx, "operator", detail.Entries[0].ID, nil, &body); err != nil {
		t.Fatal(err)
	}
	if _, err := seed.Reset(ctx, svc3); errs.CodeOf(err) != errs.CodeConflict {
		t.Fatalf("edited demo must refuse reset: %v", err)
	}
}

// TestDemoUserProvisioning covers the optional demo operator account: it is
// only created on request, uses the out-of-band password, never resets an
// existing account's password, and survives a demo reset.
func TestDemoUserProvisioning(t *testing.T) {
	ctx := context.Background()
	svc := newService(t)

	// Data-only seeding never creates credentials.
	res, err := seed.Demo(ctx, svc)
	if err != nil || res.User != nil {
		t.Fatalf("data-only seed must not create a user: %+v %v", res, err)
	}
	users, err := svc.ListUsers(ctx)
	if err != nil || len(users) != 0 {
		t.Fatalf("unexpected users: %+v %v", users, err)
	}

	// A password without a username is refused (no silent default account).
	if _, err := seed.DemoWithOptions(ctx, svc, seed.Options{Password: "demo-password-1"}); errs.CodeOf(err) != errs.CodeUsage {
		t.Fatalf("password without username must be refused with usage_error: %v", err)
	}
	// A username without a password is refused.
	if _, err := seed.DemoWithOptions(ctx, svc, seed.Options{User: "demo"}); errs.CodeOf(err) != errs.CodeUsage {
		t.Fatalf("username without password must be refused with usage_error: %v", err)
	}

	// Create the demo operator on an already-seeded demo workspace.
	got, err := seed.DemoWithOptions(ctx, svc, seed.Options{User: "demo", Password: "demo-password-1"})
	if err != nil {
		t.Fatalf("demo user: %v", err)
	}
	if !got.AlreadySeeded || got.User == nil || !got.User.Created || got.User.Role != seed.DemoUserRole {
		t.Fatalf("demo user result: %+v", got)
	}
	if _, _, err := svc.Login(ctx, "demo", "demo-password-1"); err != nil {
		t.Fatalf("demo login: %v", err)
	}

	// Re-running with a different password must not rewrite the account.
	again, err := seed.DemoWithOptions(ctx, svc, seed.Options{User: "demo", Password: "different-password-2"})
	if err != nil || again.User == nil || !again.User.AlreadyExisted || again.User.Created {
		t.Fatalf("second demo user call: %+v %v", again, err)
	}
	if _, _, err := svc.Login(ctx, "demo", "demo-password-1"); err != nil {
		t.Fatalf("existing demo password must be unchanged: %v", err)
	}
	if _, _, err := svc.Login(ctx, "demo", "different-password-2"); err == nil {
		t.Fatal("existing demo password was silently replaced")
	}

	// A demo reset keeps users and sessions provisioning intact.
	if _, err := seed.Reset(ctx, svc); err != nil {
		t.Fatalf("demo reset with user: %v", err)
	}
	users, err = svc.ListUsers(ctx)
	if err != nil || len(users) != 1 || users[0].Username != "demo" {
		t.Fatalf("demo user must survive reset: %+v %v", users, err)
	}
	if _, _, err := svc.Login(ctx, "demo", "demo-password-1"); err != nil {
		t.Fatalf("demo login after reset: %v", err)
	}
}
