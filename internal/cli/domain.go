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
)

// openService opens (creating if needed) the workspace database and returns
// the application service. The schema is forward-only embedded migrations: a
// fresh database is created at the latest version; a database from a newer
// version is refused. Nothing here touches data outside the workspace.
func openService(ctx context.Context, home string, logger *slog.Logger) (*app.Service, func(), error) {
	started := time.Now()
	logger.Debug("database open", "operation", "open_service", "home", home)
	handle, err := db.Open(ctx, db.Path(home))
	if err != nil {
		logger.Debug("database open failed", "operation", "open_service", "error_code", errs.CodeOf(err))
		return nil, nil, err
	}
	if err := db.Migrate(ctx, handle); err != nil {
		_ = handle.Close()
		logger.Debug("database migration failed", "operation", "migrate", "error_code", errs.CodeOf(err))
		return nil, nil, err
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
