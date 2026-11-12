package app_test

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"repairflow/internal/app"
	"repairflow/internal/db"
	"repairflow/internal/errs"
	"repairflow/internal/files"
)

func reopenService(t *testing.T, home string) *app.Service {
	t.Helper()
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

func TestIdempotentRepairCreate(t *testing.T) {
	ctx := context.Background()
	home := filepath.Join(t.TempDir(), "ws")
	svc := reopenService(t, home)
	c, _ := svc.CreateCustomer(ctx, "tester", app.CustomerInput{Name: "Cust"})
	in := app.RepairInput{CustomerID: c.ID, Device: "Device", Issue: "Issue"}
	key := "agent-req-0001"

	// 1. First successful request.
	first, err := svc.CreateRepairWithKey(ctx, "tester", in, key)
	if err != nil {
		t.Fatalf("first create: %v", err)
	}

	// 2. Identical retry replays the original result (same id, no new rows).
	retry, err := svc.CreateRepairWithKey(ctx, "tester", in, key)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if retry.ID != first.ID || retry.Revision != first.Revision {
		t.Fatalf("replay must return the original result: %+v vs %+v", retry, first)
	}
	if p, _ := svc.ListRepairs(ctx, app.RepairFilter{}); p.Total != 1 {
		t.Fatalf("replay created a duplicate: %d", p.Total)
	}

	// 3. Conflicting retry (same key, different payload) is a structured conflict.
	conflict := in
	conflict.Device = "Different device"
	if _, err := svc.CreateRepairWithKey(ctx, "tester", conflict, key); errs.CodeOf(err) != errs.CodeConflict {
		t.Fatalf("conflicting retry must be conflict, got: %v", err)
	}
	if p, _ := svc.ListRepairs(ctx, app.RepairFilter{}); p.Total != 1 {
		t.Fatal("conflict must not create records")
	}

	// 7. Different keys with identical payloads create separate records.
	second, err := svc.CreateRepairWithKey(ctx, "tester", in, "agent-req-0002")
	if err != nil || second.ID == first.ID {
		t.Fatalf("distinct keys must create distinct repairs: %+v %v", second, err)
	}
}

func TestIdempotencyKeyNotConsumedByValidationFailure(t *testing.T) {
	ctx := context.Background()
	svc := reopenService(t, filepath.Join(t.TempDir(), "ws"))
	c, _ := svc.CreateCustomer(ctx, "tester", app.CustomerInput{Name: "Cust"})
	key := "agent-req-0003"

	// 6. Invalid request (empty device) with the key: validation error...
	bad := app.RepairInput{CustomerID: c.ID, Device: "", Issue: "Issue"}
	if _, err := svc.CreateRepairWithKey(ctx, "tester", bad, key); errs.CodeOf(err) != errs.CodeValidation {
		t.Fatalf("invalid request: %v", err)
	}
	// ...then a corrected retry with the SAME key succeeds (key not consumed).
	good := app.RepairInput{CustomerID: c.ID, Device: "Fixed device", Issue: "Issue"}
	created, err := svc.CreateRepairWithKey(ctx, "tester", good, key)
	if err != nil {
		t.Fatalf("corrected retry must succeed: %v", err)
	}
	// And the corrected request now replays consistently.
	retry, err := svc.CreateRepairWithKey(ctx, "tester", good, key)
	if err != nil || retry.ID != created.ID {
		t.Fatalf("replay after correction: %+v %v", retry, err)
	}
}

func TestIdempotencyConcurrentDuplicates(t *testing.T) {
	ctx := context.Background()
	svc := reopenService(t, filepath.Join(t.TempDir(), "ws"))
	c, _ := svc.CreateCustomer(ctx, "tester", app.CustomerInput{Name: "Cust"})
	in := app.RepairInput{CustomerID: c.ID, Device: "Device", Issue: "Issue"}
	key := "agent-req-0004"

	// 4. Concurrent same-key attempts create exactly one repair.
	var wg sync.WaitGroup
	ids := make(chan int64, 8)
	errsSeen := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tk, err := svc.CreateRepairWithKey(ctx, "tester", in, key)
			if err != nil {
				errsSeen <- err
				return
			}
			ids <- tk.ID
		}()
	}
	wg.Wait()
	close(ids)
	close(errsSeen)
	for err := range errsSeen {
		t.Fatalf("concurrent idempotent create failed: %v", err)
	}
	seen := map[int64]bool{}
	for id := range ids {
		seen[id] = true
	}
	if len(seen) != 1 {
		t.Fatalf("expected one distinct repair id, got %v", seen)
	}
	if p, _ := svc.ListRepairs(ctx, app.RepairFilter{}); p.Total != 1 {
		t.Fatalf("duplicates created: %d", p.Total)
	}
}

func TestIdempotencyPersistsAcrossRestart(t *testing.T) {
	ctx := context.Background()
	home := filepath.Join(t.TempDir(), "ws")
	svc := reopenService(t, home)
	c, _ := svc.CreateCustomer(ctx, "tester", app.CustomerInput{Name: "Cust"})
	in := app.RepairInput{CustomerID: c.ID, Device: "Device", Issue: "Issue"}
	key := "agent-req-0005"

	first, err := svc.CreateRepairWithKey(ctx, "tester", in, key)
	if err != nil {
		t.Fatal(err)
	}

	// 5. Restart: a fresh Service over the same workspace replays the key.
	svc2 := reopenService(t, home)
	retry, err := svc2.CreateRepairWithKey(ctx, "tester", in, key)
	if err != nil || retry.ID != first.ID {
		t.Fatalf("replay across restart: %+v %v", retry, err)
	}
	// The replayed result carries the full record fields.
	if retry.Device != "Device" || retry.CustomerID != c.ID {
		t.Fatalf("replayed record incomplete: %+v", retry)
	}
}

func TestIdempotencyKeysAreOpaqueAndValidated(t *testing.T) {
	ctx := context.Background()
	svc := reopenService(t, filepath.Join(t.TempDir(), "ws"))
	c, _ := svc.CreateCustomer(ctx, "tester", app.CustomerInput{Name: "Cust"})
	in := app.RepairInput{CustomerID: c.ID, Device: "D", Issue: "I"}

	for _, bad := range []string{"short", "has spaces here", "unicode-ключ", strings129()} {
		if _, err := svc.CreateRepairWithKey(ctx, "tester", in, bad); errs.CodeOf(err) != errs.CodeValidation {
			t.Errorf("key %q must be refused", bad)
		}
	}
	// Customer intake carries the same contract.
	cust, err := svc.CreateCustomerWithKey(ctx, "tester", app.CustomerInput{Name: "Idem"}, "agent-req-0006")
	if err != nil {
		t.Fatal(err)
	}
	again, err := svc.CreateCustomerWithKey(ctx, "tester", app.CustomerInput{Name: "Idem"}, "agent-req-0006")
	if err != nil || again.ID != cust.ID {
		t.Fatalf("customer replay: %+v %v", again, err)
	}
	if _, err := svc.CreateCustomerWithKey(ctx, "tester", app.CustomerInput{Name: "Other"}, "agent-req-0006"); errs.CodeOf(err) != errs.CodeConflict {
		t.Fatalf("customer key conflict: %v", err)
	}
}

func strings129() string {
	s := ""
	for i := 0; i < 130; i++ {
		s += "a"
	}
	return s
}
