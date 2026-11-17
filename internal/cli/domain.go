package cli

import (
	"context"
	"log/slog"
	"strconv"
	"time"

	"repairflow/internal/app"
	"repairflow/internal/db"
	"repairflow/internal/errs"
	"repairflow/internal/files"
	"repairflow/internal/workspace"
)

// openService opens the workspace under a shared writer lock. Ordinary domain
// commands and serve never migrate: an outdated schema is refused with the
// explicit backup/migration remedy below.
func openService(ctx context.Context, home string, logger *slog.Logger) (*app.Service, func(), error) {
	unlock, err := workspace.Lock(ctx, home, false)
	if err != nil {
		return nil, nil, err
	}
	svc, closeDB, err := openServiceDatabase(ctx, home, logger, false)
	if err != nil {
		unlock()
		return nil, nil, err
	}
	return svc, func() { closeDB(); unlock() }, nil
}

// openServiceDatabase opens the database without taking the workspace lock.
// migrate is reserved for the explicit init path, which holds the exclusive lock.
func openServiceDatabase(ctx context.Context, home string, logger *slog.Logger, migrate bool) (*app.Service, func(), error) {
	started := time.Now()
	logger.Debug("database open", "operation", "open_service", "home", home)
	handle, err := db.Open(ctx, db.Path(home))
	if err != nil {
		logger.Debug("database open failed", "operation", "open_service", "error_code", errs.CodeOf(err))
		return nil, nil, err
	}
	if migrate {
		if err := db.Migrate(ctx, handle); err != nil {
			_ = handle.Close()
			logger.Debug("database migration failed", "operation", "migrate", "error_code", errs.CodeOf(err))
			return nil, nil, err
		}
	} else {
		current, err := db.CurrentVersion(ctx, handle)
		if err != nil {
			_ = handle.Close()
			return nil, nil, err
		}
		if current > db.LatestVersion() {
			_ = handle.Close()
			return nil, nil, errs.New(errs.CodeConfigVersion,
				"database schema version %d is newer than the supported version %d; upgrade RepairFlow first", current, db.LatestVersion())
		}
		if current < db.LatestVersion() {
			_ = handle.Close()
			return nil, nil, schemaGateError(home, current, db.LatestVersion())
		}
	}
	svc := app.New(handle).WithStore(files.New(home))
	if err := svc.RecoverPendingDeletes(ctx); err != nil {
		_ = handle.Close()
		logger.Debug("interrupted attachment delete recovery failed", "operation", "recover_pending_deletes", "error_code", errs.CodeOf(err))
		return nil, nil, err
	}
	logger.Debug("database ready", "operation", "open_service", "schema_version", db.LatestVersion(),
		"duration_ms", time.Since(started).Milliseconds())
	return svc, func() {
		if err := handle.Close(); err != nil {
			logger.Error("database close failed", "operation", "close_database", "error", err)
		}
	}, nil
}

func schemaGateError(home string, current, latest int) error {
	return errs.New(errs.CodeConflict,
		"workspace schema version %d is older than the supported version %d; stop the service, run `repairflow backup create --output ABSOLUTE_PATH --home %s`, then `repairflow init --backup ABSOLUTE_PATH --home %s`",
		current, latest, home, home)
}

// resolveActor picks the audit identity for mutations: explicit --actor,
// then $USER, then "operator".
func resolveActor(p parsedArgs, getenv Env) string {
	if actor := p.get("actor"); actor != "" {
		return actor
	}
	if user := getenv("USER"); user != "" {
		return user
	}
	return "operator"
}

func parseID(raw, entity string) (int64, error) {
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, errs.New(errs.CodeValidation, "%s id must be a positive integer (got %q)", entity, raw)
	}
	return id, nil
}

func parsePositiveInt(raw, field string) (int, error) {
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 0, errs.New(errs.CodeValidation, "%s must be a positive integer (got %q)", field, raw)
	}
	return n, nil
}

func paginationArgs(p parsedArgs) (page, perPage int, err error) {
	page, perPage = 1, 20
	if p.get("page") != "" {
		page, err = parsePositiveInt(p.get("page"), "page")
		if err != nil {
			return 0, 0, err
		}
	}
	if p.get("per-page") != "" {
		perPage, err = parsePositiveInt(p.get("per-page"), "per-page")
		if err != nil {
			return 0, 0, err
		}
	}
	return page, perPage, nil
}

// requirePositional ensures the exact number of positional arguments.
func requirePositional(p parsedArgs, want int, usage string) error {
	if len(p.positional) != want {
		return errs.New(errs.CodeUsage, "usage: %s", usage)
	}
	return nil
}
