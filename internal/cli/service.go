package cli

import (
	"context"
	"os"
	"path/filepath"

	"repairflow/internal/errs"
	"repairflow/internal/service"
)

// Service commands: unit install/status/remove for systemd user units
// and OpenRC service scripts. --init forces the target; otherwise it is detected.
func runService(ctx context.Context, opts options, args []string, getenv Env) (any, error) {
	if len(args) == 0 {
		return nil, errs.New(errs.CodeUsage, "usage: repairflow service <install|status|remove> ...")
	}
	sub, rest := args[0], args[1:]
	p, err := parseArgs(rest, []string{"init", "name"}, []string{"no-enable"})
	if err != nil {
		return nil, err
	}
	initSystem, err := service.ParseInit(p.get("init"))
	if err != nil {
		return nil, err
	}
	serviceName, err := service.ParseName(p.get("name"))
	if err != nil {
		return nil, err
	}
	so := service.Options{
		Init:    initSystem,
		Name:    serviceName,
		UnitDir: getenv("REPAIRFLOW_UNIT_DIR"), // test/deploy override; empty = default
		InitDir: getenv("REPAIRFLOW_INIT_DIR"), // test/deploy override; empty = /etc/init.d
	}
	if err := requirePositional(p, 0, "repairflow service "+sub+" [--init systemd|openrc|none] [--name NAME] [--no-enable]"); err != nil {
		return nil, err
	}

	switch sub {
	case "install":
		home, err := resolveHome(opts, getenv)
		if err != nil {
			return nil, err
		}
		// A unit for a workspace that does not exist installs cleanly and then
		// fails at start (seen on Alpine under doas: the default resolved to
		// /root/.repairflow). Refuse before touching the init system.
		if _, statErr := os.Stat(filepath.Join(home, "config.toml")); statErr != nil {
			return nil, errs.New(errs.CodeValidation,
				"no RepairFlow workspace at %s; run `repairflow init --home %s` first or pass the right --home", home, home)
		}
		binary, err := os.Executable()
		if err != nil {
			return nil, errs.Wrap(errs.CodeInternal, err, "cannot determine executable path")
		}
		so.BinaryPath = binary
		so.Home = home
		so.Enable = !p.flag("no-enable")
		return service.Install(so)
	case "status":
		return service.Status(so)
	case "remove":
		return service.Remove(so)
	default:
		return nil, errs.New(errs.CodeUsage, "unknown service subcommand %q (supported: install, status, remove)", sub)
	}
}
