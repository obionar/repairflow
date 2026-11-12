package cli

import (
	"context"
	"fmt"

	"repairflow/internal/app"
	"repairflow/internal/buildinfo"
	"repairflow/internal/db"
	"repairflow/internal/errs"
	"repairflow/internal/files"
	"repairflow/internal/seed"
	"repairflow/internal/workspace"
)

type doctorCheck struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

type doctorReport struct {
	OK     bool          `json:"ok"`
	Checks []doctorCheck `json:"checks"`
}

type statusReport struct {
	Version       string `json:"version"`
	Home          string `json:"home"`
	Initialized   bool   `json:"initialized"`
	ConfigVersion int    `json:"config_version"`
	SchemaVersion int    `json:"schema_version"`
	Customers     int    `json:"customers"`
	Repairs       int    `json:"repairs"`
	DemoSeeded    bool   `json:"demo_seeded"`
}

// runDoctor performs read-only workspace/database diagnostics. Every check
// reports its own result; the command exits 3 when anything is unhealthy.
func runDoctor(ctx context.Context, opts options, args []string, getenv Env) (doctorReport, error) {
	if len(args) > 0 {
		return doctorReport{}, errs.New(errs.CodeUsage, "doctor takes no arguments")
	}
	home, err := resolveHome(opts, getenv)
	if err != nil {
		return doctorReport{}, err
	}
	report := doctorReport{OK: true}
	add := func(name string, err error, okDetail string) {
		if err != nil {
			report.OK = false
			report.Checks = append(report.Checks, doctorCheck{Name: name, OK: false, Detail: err.Error()})
			return
		}
		report.Checks = append(report.Checks, doctorCheck{Name: name, OK: true, Detail: okDetail})
	}

	status, err := workspace.Check(home)
	workspaceDetail := home
	if status != nil {
		workspaceDetail = fmt.Sprintf("%s (config_version %d)", home, status.Config.ConfigVersion)
	}
	add("workspace", err, workspaceDetail)
	if err != nil {
		return report, nil
	}

	handle, err := db.Open(ctx, db.Path(home))
	if err != nil {
		add("database", err, "")
		return report, nil
	}
	defer func() { _ = handle.Close() }()

	version, err := db.CurrentVersion(ctx, handle)
	if err == nil && version != db.LatestVersion() {
		err = errs.New(errs.CodeConflict, "schema version %d, expected %d", version, db.LatestVersion())
	}
	add("schema", err, fmt.Sprintf("version %d", db.LatestVersion()))
	add("integrity", db.IntegrityCheck(ctx, handle), "integrity_check ok, no foreign key violations")

	svc := app.New(handle).WithStore(files.New(home))
	if err := svc.RecoverPendingDeletes(ctx); err != nil {
		add("attachments", err, "cannot recover an interrupted attachment delete")
		return report, nil
	}
	customers, repairs, err := svc.Counts(ctx)
	add("records", err, fmt.Sprintf("%d customers, %d repairs", customers, repairs))
	seeded, _, err := svc.SeedMarker(ctx, seed.DemoKey)
	add("demo_marker", err, map[bool]string{true: "demo dataset present", false: "no demo dataset"}[seeded])

	missing, orphans, err := svc.ReconcileAttachments(ctx)
	detail := "attachments and bytes consistent"
	if len(missing) > 0 || len(orphans) > 0 {
		err = errs.New(errs.CodeInternal, "missing bytes: %v; orphan files: %v", missing, orphans)
		detail = ""
	}
	add("attachments", err, detail)

	return report, nil
}

// runCleanup removes disposable workspace content: stale upload
// staging files and regenerable cache. User data is never touched.
func runCleanup(opts options, args []string, getenv Env) (any, error) {
	if len(args) > 0 {
		return nil, errs.New(errs.CodeUsage, "cleanup takes no arguments")
	}
	home, err := resolveHome(opts, getenv)
	if err != nil {
		return nil, err
	}
	return workspace.Cleanup(home)
}

// runStatus reports a compact workspace summary (read-only).
func runStatus(ctx context.Context, opts options, args []string, getenv Env) (statusReport, error) {
	if len(args) > 0 {
		return statusReport{}, errs.New(errs.CodeUsage, "status takes no arguments")
	}
	home, err := resolveHome(opts, getenv)
	if err != nil {
		return statusReport{}, err
	}
	report := statusReport{Version: buildinfo.Get().Version, Home: home}
	status, err := workspace.Check(home)
	if err != nil {
		if errs.CodeOf(err) == errs.CodeNotInitialized {
			return report, nil // uninitialized workspace is a valid status
		}
		return statusReport{}, err
	}
	report.Initialized = true
	report.ConfigVersion = status.Config.ConfigVersion

	handle, err := db.Open(ctx, db.Path(home))
	if err != nil {
		opts.logger.Debug("status database unavailable", "operation", "status", "error_code", errs.CodeOf(err))
		return report, nil // keep status readable; doctor provides strict diagnostics
	}
	defer func() { _ = handle.Close() }()
	if version, err := db.CurrentVersion(ctx, handle); err == nil {
		report.SchemaVersion = version
	} else {
		opts.logger.Debug("status schema unavailable", "operation", "status", "error_code", errs.CodeOf(err))
	}
	svc := app.New(handle)
	if c, t, err := svc.Counts(ctx); err == nil {
		report.Customers, report.Repairs = c, t
	} else {
		opts.logger.Debug("status counts unavailable", "operation", "status", "error_code", errs.CodeOf(err))
	}
	if seeded, _, err := svc.SeedMarker(ctx, seed.DemoKey); err == nil {
		report.DemoSeeded = seeded
	} else {
		opts.logger.Debug("status demo marker unavailable", "operation", "status", "error_code", errs.CodeOf(err))
	}
	return report, nil
}
