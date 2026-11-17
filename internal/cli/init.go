package cli

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"

	"repairflow/internal/app"
	"repairflow/internal/backup"
	"repairflow/internal/config"
	"repairflow/internal/db"
	"repairflow/internal/errs"
	"repairflow/internal/i18n"
	"repairflow/internal/seed"
	"repairflow/internal/workspace"
)

// initDemoResult reports workspace initialization plus an optional demo seed.
type initDemoResult struct {
	Init workspace.InitResult `json:"init"`
	Seed *seed.Result         `json:"seed,omitempty"`
}

// initReport adds the additive ignored list without changing the established
// workspace init JSON fields.
type initReport struct {
	*workspace.InitResult
	Ignored          []string `json:"ignored,omitempty"`
	existingLanguage string
}

// openForInit initializes a fresh database, or migrates an outdated one only
// after verifying the operator-supplied pre-migration backup. Migration holds
// the exclusive workspace lock; ordinary opens use a shared lock and refuse an
// outdated schema.
func openForInit(ctx context.Context, opts options, home, backupPath string) (*app.Service, func(), bool, error) {
	dbPath := db.Path(home)
	_, statErr := os.Stat(dbPath)
	fresh := os.IsNotExist(statErr)

	current, err := peekSchema(ctx, home)
	if err != nil {
		return nil, nil, false, err
	}
	latest := db.LatestVersion()
	if current > latest {
		return nil, nil, false, errs.New(errs.CodeConfigVersion,
			"database schema version %d is newer than the supported version %d; upgrade RepairFlow first", current, latest)
	}
	if current == latest {
		svc, closeDB, err := openService(ctx, home, opts.logger)
		return svc, closeDB, backupPath != "", err
	}
	ignoredBackup := fresh && backupPath != ""
	if current != 0 {
		if backupPath == "" {
			return nil, nil, false, schemaGateError(home, current, latest)
		}
		if !filepath.IsAbs(backupPath) {
			return nil, nil, false, errs.New(errs.CodeValidation,
				"--backup must be an absolute path to a verified pre-migration backup")
		}
	}

	unlock, err := workspace.Lock(ctx, home, true)
	if err != nil {
		return nil, nil, false, err
	}
	// Recheck under the exclusive lock: another process may have migrated or
	// changed the schema while this command waited.
	current, err = peekSchema(ctx, home)
	if err != nil {
		unlock()
		return nil, nil, false, err
	}
	if current == latest {
		unlock()
		svc, closeDB, err := openService(ctx, home, opts.logger)
		return svc, closeDB, ignoredBackup, err
	}
	if current != 0 {
		manifest, err := backup.Verify(ctx, backupPath)
		if err != nil {
			unlock()
			return nil, nil, false, errs.Wrap(errs.CodeConflict, err,
				"pre-migration backup did not verify; migration was not started")
		}
		if manifest.SchemaVersion != current {
			unlock()
			return nil, nil, false, errs.New(errs.CodeConflict,
				"pre-migration backup schema version %d does not match workspace version %d", manifest.SchemaVersion, current)
		}
	}
	svc, closeDB, err := openServiceDatabase(ctx, home, opts.logger, true)
	if err != nil {
		unlock()
		return nil, nil, false, err
	}
	return svc, func() { closeDB(); unlock() }, ignoredBackup, nil
}

func peekSchema(ctx context.Context, home string) (int, error) {
	handle, err := db.Open(ctx, db.Path(home))
	if err != nil {
		return 0, err
	}
	defer func() { _ = handle.Close() }()
	return db.CurrentVersion(ctx, handle)
}

// workspaceInitWith initializes the workspace and applies the chosen web port
// and interface language to a config this call created. An existing config is
// never rewritten.
func workspaceInitWith(home string, port int, language string) (*workspace.InitResult, error) {
	res, err := workspace.Init(home)
	if err != nil {
		return nil, err
	}
	defaults := config.Default()
	if res.AlreadyInitialized || (port == defaults.Web.Port && (language == "" || language == defaults.UI.Language)) {
		return res, nil
	}
	cfg, err := config.Load(res.ConfigPath)
	if err != nil {
		return nil, err
	}
	if port != defaults.Web.Port {
		cfg.Web.Port = port
	}
	if language != "" {
		cfg.UI.Language = language
	}
	if err := os.WriteFile(res.ConfigPath+".tmp", config.Render(cfg), 0o600); err != nil {
		return nil, errs.Wrap(errs.CodeInternal, err, "cannot write config")
	}
	if err := os.Rename(res.ConfigPath+".tmp", res.ConfigPath); err != nil {
		return nil, errs.Wrap(errs.CodeInternal, err, "cannot publish config")
	}
	return res, nil
}

func runInit(ctx context.Context, stdin io.Reader, stdout io.Writer, opts options, args []string, getenv Env) (any, error) {
	p, err := parseArgs(args, []string{"demo-user", "language", "backup"}, []string{"demo", "interactive"})
	if err != nil {
		return nil, err
	}
	if len(p.positional) > 0 {
		return nil, errs.New(errs.CodeUsage, "init takes no positional arguments, got %q", strings.Join(p.positional, " "))
	}
	if p.flag("interactive") {
		data, err := runInitWizard(ctx, stdin, stdout, opts, getenv)
		if err != nil {
			return nil, err
		}
		// The wizard only opens the database when it creates an account or
		// seeds demo data; the plain path must initialize it too.
		if result, ok := data.(map[string]any); ok {
			if res, ok := result["init"].(*workspace.InitResult); ok {
				_, closeDB, _, err := openForInit(ctx, opts, res.Home, "")
				if err != nil {
					return nil, err
				}
				closeDB()
			}
		}
		return data, nil
	}
	if !p.flag("demo") && p.get("demo-user") != "" {
		return nil, errs.New(errs.CodeUsage, "--demo-user requires --demo")
	}
	language := ""
	if raw := p.get("language"); raw != "" {
		tr, err := i18n.New()
		if err != nil {
			return nil, errs.Wrap(errs.CodeInternal, err, "cannot load interface translations")
		}
		language = i18n.Normalize(raw)
		if !tr.Has(language) {
			codes := make([]string, 0, 4)
			for _, l := range tr.Languages() {
				codes = append(codes, l.Code)
			}
			return nil, errs.New(errs.CodeValidation,
				"unknown interface language %q (available: %s)", raw, strings.Join(codes, ", "))
		}
	}
	seedOpts, err := demoOptions(p, getenv)
	if err != nil {
		return nil, err
	}
	home, err := resolveHome(opts, getenv)
	if err != nil {
		return nil, err
	}
	res, err := workspaceInitWith(home, config.Default().Web.Port, language)
	if err != nil {
		return nil, err
	}
	report := &initReport{InitResult: res}
	if res.AlreadyInitialized && language != "" {
		cfg, err := config.Load(res.ConfigPath)
		if err != nil {
			return nil, err
		}
		// An existing config is never rewritten, so a supplied --language is
		// not applied even when it matches the current value.
		report.Ignored = []string{"language"}
		report.existingLanguage = cfg.UI.Language
	}
	// init is the explicit database-initializing and migration command.
	svc, closeDB, ignoredBackup, err := openForInit(ctx, opts, home, p.get("backup"))
	if err != nil {
		return nil, err
	}
	defer closeDB()
	if ignoredBackup {
		report.Ignored = append(report.Ignored, "backup")
	}
	if !p.flag("demo") {
		return report, nil
	}
	seeded, err := seed.DemoWithOptions(ctx, svc, seedOpts)
	if err != nil {
		return nil, err
	}
	return initDemoResult{Init: *res, Seed: &seeded}, nil
}
