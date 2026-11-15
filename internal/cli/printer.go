package cli

import (
	"context"
	"strings"

	"repairflow/internal/app"
	"repairflow/internal/errs"
)

// runPrinter shows or selects the workspace label printer and prints a test
// label. The CLI is outside the web authorization boundary, so it acts as an
// administrator, like the account commands.
func runPrinter(ctx context.Context, opts options, args []string, getenv Env) (any, error) {
	if len(args) == 0 {
		return nil, errs.New(errs.CodeUsage, "usage: repairflow printer <show|set|test>")
	}
	sub, rest := args[0], args[1:]
	home, err := resolveHome(opts, getenv)
	if err != nil {
		return nil, err
	}
	svc, closeDB, err := openService(ctx, home, opts.logger)
	if err != nil {
		return nil, err
	}
	defer closeDB()

	switch sub {
	case "show":
		p, err := parseArgs(rest, nil, nil)
		if err != nil {
			return nil, err
		}
		if err := requirePositional(p, 0, "repairflow printer show"); err != nil {
			return nil, err
		}
		return svc.LabelPrinter(ctx)
	case "set":
		p, err := parseArgs(rest, nil, nil)
		if err != nil {
			return nil, err
		}
		if err := requirePositional(p, 1, "repairflow printer set p15|none"); err != nil {
			return nil, err
		}
		driver := strings.ToLower(strings.TrimSpace(p.positional[0]))
		if driver == "none" || driver == "off" {
			driver = ""
		}
		return svc.SetLabelPrinter(ctx, app.RoleAdmin, driver)
	case "test":
		p, err := parseArgs(rest, nil, nil)
		if err != nil {
			return nil, err
		}
		if err := requirePositional(p, 0, "repairflow printer test"); err != nil {
			return nil, err
		}
		return svc.PrintTestLabel(ctx, app.RoleAdmin)
	default:
		return nil, errs.New(errs.CodeUsage, "unknown printer subcommand %q (supported: show, set, test)", sub)
	}
}
