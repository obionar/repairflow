package cli

import (
	"context"
	"net"
	"strings"

	"repairflow/internal/db"
	"repairflow/internal/errs"
	"repairflow/internal/i18n"
	"repairflow/internal/web"
	"repairflow/internal/workspace"
)

// serveReport describes the listener for machine output.
type serveReport struct {
	Home string `json:"home"`
	Host string `json:"host"`
	Port int    `json:"port"`
}

// runServe starts the embedded web interface with graceful shutdown on
// SIGINT/SIGTERM.
func runServe(ctx context.Context, opts options, args []string, getenv Env) (any, error) {
	if len(args) > 0 {
		return nil, errs.New(errs.CodeUsage, "serve takes no arguments")
	}
	home, err := resolveHome(opts, getenv)
	if err != nil {
		return nil, err
	}
	status, err := workspace.Check(home)
	if err != nil {
		return nil, err
	}
	svc, closeDB, err := openService(ctx, home, opts.logger)
	if err != nil {
		return nil, err
	}
	defer closeDB()
	srv, err := web.New(svc)
	if err != nil {
		return nil, errs.Wrap(errs.CodeInternal, err, "cannot build web interface")
	}
	srv.WithLogger(opts.logger).WithAuthentication(status.Config.Web.AuthRequired)
	srv.WithLoopbackOnly(!status.Config.Web.AllowRemote)
	tr, err := i18n.New()
	if err != nil {
		return nil, errs.Wrap(errs.CodeInternal, err, "cannot load interface translations")
	}
	if err := tr.LoadDir(home); err != nil {
		opts.logger.Warn("workspace locale override ignored", "operation", "serve", "error", err)
	}
	if !tr.Has(status.Config.UI.Language) {
		opts.logger.Warn("configured ui.language has no catalog; falling back to English",
			"operation", "serve", "language", status.Config.UI.Language)
	}
	srv.WithTranslations(tr, status.Config.UI.Language)
	if !status.Config.Web.AuthRequired {
		if !isLoopbackBind(status.Config.Web.Host) {
			opts.logger.Warn("SERVING WITHOUT AUTHENTICATION ON A NON-LOOPBACK ADDRESS: anyone who can reach this port has full write access (web.allow_insecure_lan, testing only)",
				"operation", "serve", "host", status.Config.Web.Host, "port", status.Config.Web.Port)
		} else {
			opts.logger.Warn("web authentication disabled for loopback test/demo mode", "operation", "serve")
		}
	}
	opts.logger.Debug("web server configured", "operation", "serve", "home", home,
		"host", status.Config.Web.Host, "port", status.Config.Web.Port,
		"environment", status.Config.App.Environment, "config_version", status.Config.ConfigVersion,
		"schema_version", db.LatestVersion())
	if err := web.ListenAndServe(ctx, status.Config.Web.Host, status.Config.Web.Port, srv.Handler()); err != nil {
		return nil, err
	}
	return serveReport{Home: home, Host: status.Config.Web.Host, Port: status.Config.Web.Port}, nil
}

// isLoopbackBind reports whether a configured listen address is loopback-only.
func isLoopbackBind(host string) bool {
	h := strings.TrimSpace(host)
	if strings.EqualFold(h, "localhost") {
		return true
	}
	if ip := net.ParseIP(strings.Trim(h, "[]")); ip != nil {
		return ip.IsLoopback()
	}
	return false
}
