package cli

import (
	"context"

	"repairflow/internal/app"
	"repairflow/internal/errs"
)

func runUser(ctx context.Context, opts options, args []string, getenv Env) (any, error) {
	if len(args) == 0 {
		return nil, errs.New(errs.CodeUsage, "usage: repairflow user <add|list|passwd|enable|disable> ...")
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
	case "add":
		p, err := parseArgs(rest, []string{"role", "actor"}, nil)
		if err != nil {
			return nil, err
		}
		if err := requirePositional(p, 1, "repairflow user add USERNAME --role admin|technician|viewer (password via REPAIRFLOW_NEW_PASSWORD)"); err != nil {
			return nil, err
		}
		password := getenv("REPAIRFLOW_NEW_PASSWORD")
		if password == "" {
			return nil, errs.New(errs.CodeUsage,
				"password must come from REPAIRFLOW_NEW_PASSWORD (never from command-line arguments)")
		}
		actor := resolveActor(p, getenv)
		return svc.CreateUser(ctx, actor, p.positional[0], password, p.get("role"))
	case "list":
		p, err := parseArgs(rest, nil, nil)
		if err != nil {
			return nil, err
		}
		if err := requirePositional(p, 0, "repairflow user list"); err != nil {
			return nil, err
		}
		return svc.ListUsers(ctx)
	case "passwd":
		p, err := parseArgs(rest, []string{"actor"}, nil)
		if err != nil {
			return nil, err
		}
		if err := requirePositional(p, 1, "repairflow user passwd USERNAME (password via REPAIRFLOW_NEW_PASSWORD)"); err != nil {
			return nil, err
		}
		password := getenv("REPAIRFLOW_NEW_PASSWORD")
		if password == "" {
			return nil, errs.New(errs.CodeUsage, "password must come from REPAIRFLOW_NEW_PASSWORD (never from command-line arguments)")
		}
		actor := resolveActor(p, getenv)
		if err := svc.ResetPassword(ctx, actor, app.RoleAdmin, p.positional[0], password); err != nil {
			return nil, err
		}
		return map[string]any{"username": p.positional[0], "password_changed": true, "sessions_revoked": true}, nil
	case "enable", "disable":
		p, err := parseArgs(rest, []string{"actor"}, nil)
		if err != nil {
			return nil, err
		}
		if err := requirePositional(p, 1, "repairflow user "+sub+" USERNAME"); err != nil {
			return nil, err
		}
		actor := resolveActor(p, getenv)
		return svc.SetUserActive(ctx, actor, app.RoleAdmin, p.positional[0], sub == "enable")
	default:
		return nil, errs.New(errs.CodeUsage, "unknown user subcommand %q (supported: add, list, passwd, enable, disable)", sub)
	}
}
