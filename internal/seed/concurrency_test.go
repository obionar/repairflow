package seed_test

import (
	"context"
	"sync"
	"testing"

	"repairflow/internal/app"
	"repairflow/internal/db"
	"repairflow/internal/files"
	"repairflow/internal/seed"
	"repairflow/internal/testutil"
)

func TestConcurrentSeedHasOneWinnerAndNoOrphanFiles(t *testing.T) {
	home := t.TempDir()
	open := func() *app.Service {
		handle, err := db.Open(context.Background(), db.Path(home))
		testutil.Must(t, err)
		t.Cleanup(func() { testutil.Close(t, handle) })
		testutil.Must(t, db.Migrate(context.Background(), handle))
		return app.New(handle).WithStore(files.New(home))
	}
	services := []*app.Service{open(), open()}
	const attempts = 4
	var wg sync.WaitGroup
	results := make(chan seed.Result, attempts)
	errors := make(chan error, attempts)
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(svc *app.Service) {
			defer wg.Done()
			result, err := seed.Demo(context.Background(), svc)
			if err != nil {
				errors <- err
				return
			}
			results <- result
		}(services[i%2])
	}
	wg.Wait()
	close(results)
	close(errors)
	for err := range errors {
		t.Errorf("concurrent seed: %v", err)
	}
	seeded := 0
	replayed := 0
	for result := range results {
		if result.Seeded {
			seeded++
		}
		if result.AlreadySeeded {
			replayed++
		}
	}
	if seeded != 1 || replayed != attempts-1 {
		t.Fatalf("seeded=%d already=%d, want 1/%d", seeded, replayed, attempts-1)
	}
	missing, orphans, err := services[0].ReconcileAttachments(context.Background())
	testutil.Must(t, err)
	if len(missing) != 0 || len(orphans) != 0 {
		t.Fatalf("seed left inconsistent files: missing=%v orphans=%v", missing, orphans)
	}
}
