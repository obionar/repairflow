package seed

import (
	"context"

	"repairflow/internal/app"
	"repairflow/internal/errs"
)

// ResetResult reports a completed demo reset.
type ResetResult struct {
	Reset  bool   `json:"reset"`
	Seeded Result `json:"seeded"`
}

// Reset restores a *proven* demo workspace to pristine demo state
// Refusal-first:
//
//  1. the demo seed marker must exist and match the current dataset version
//     (a real workspace never carries it);
//  2. the record counts must match the untouched demo dataset exactly — any
//     operator edit means this is no longer a pure demo workspace and
//     nothing is touched;
//  3. only then are business records and attachment bytes cleared (users and
//     sessions survive) and the demo re-seeded.
//
// The CLI additionally requires an explicit --confirm (destructive-action
// review). There is no automatic or scheduled reset anywhere.
func Reset(ctx context.Context, svc *app.Service) (*ResetResult, error) {
	found, checksum, err := svc.SeedMarker(ctx, DemoKey)
	if err != nil {
		return nil, err
	}
	if !found || checksum != manifestChecksum() {
		return nil, errs.New(errs.CodeConflict,
			"refusing demo reset: workspace is not a proven demo dataset (seed marker missing or dataset version differs)")
	}
	// Content-level provenance: the live workspace content must still match
	// the fingerprint recorded at seed time, so any operator edit — even one
	// that keeps the record counts intact — invalidates the reset.
	stateFound, wantFingerprint, err := svc.SeedMarker(ctx, DemoStateKey)
	if err != nil {
		return nil, err
	}
	liveFingerprint, err := svc.BusinessFingerprint(ctx)
	if err != nil {
		return nil, err
	}
	if !stateFound || liveFingerprint != wantFingerprint {
		return nil, errs.New(errs.CodeConflict,
			"refusing demo reset: workspace content was modified after seeding")
	}
	c, t, e, p, est, a, err := svc.BusinessCounts(ctx)
	if err != nil {
		return nil, err
	}
	if c != DemoCustomers || t != DemoRepairs || e != DemoEntries || p != DemoParts ||
		est != DemoEstimates || a != DemoAttachments {
		return nil, errs.New(errs.CodeConflict,
			"refusing demo reset: workspace was modified after seeding (%d customers, %d repairs, %d entries, %d parts, %d estimates, %d attachments; demo has %d/%d/%d/%d/%d/%d)",
			c, t, e, p, est, a, DemoCustomers, DemoRepairs, DemoEntries, DemoParts, DemoEstimates, DemoAttachments)
	}
	if err := svc.ClearBusinessRecords(ctx); err != nil {
		return nil, err
	}
	seeded, err := Demo(ctx, svc)
	if err != nil {
		return nil, err
	}
	return &ResetResult{Reset: true, Seeded: seeded}, nil
}
