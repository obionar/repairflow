package cli

import (
	"context"
	"fmt"

	"repairflow/internal/errs"
	"repairflow/internal/seed"
)

// runDemo handles the demo command group.
func runDemo(ctx context.Context, opts options, args []string, getenv Env) (any, error) {
	if len(args) == 0 {
		return nil, errs.New(errs.CodeUsage, "usage: repairflow demo reset --confirm")
	}
	sub, rest := args[0], args[1:]
	if sub != "reset" {
		return nil, errs.New(errs.CodeUsage, "unknown demo subcommand %q (supported: reset)", sub)
	}
	p, err := parseArgs(rest, nil, []string{"confirm"})
	if err != nil {
		return nil, err
	}
	if err := requirePositional(p, 0, "repairflow demo reset --confirm"); err != nil {
		return nil, err
	}
	if !p.flag("confirm") {
		return nil, errs.New(errs.CodeValidation,
			"demo reset permanently clears the demo records and files; re-run with --confirm after review")
	}
	home, err := resolveHome(opts, getenv)
	if err != nil {
		return nil, err
	}
	svc, closeDB, err := openService(ctx, home, opts.logger)
	if err != nil {
		return nil, err
	}
	defer closeDB()
	return seed.Reset(ctx, svc)
}

// renderSeedUser renders the optional demo account line. It never prints the
// password.
func renderSeedUser(u *seed.UserResult) string {
	if u == nil {
		return ""
	}
	state := "created"
	if u.AlreadyExisted {
		state = "already existed (password unchanged)"
	}
	return fmt.Sprintf("demo operator: %s (%s, %s)\n", u.Username, u.Role, state)
}

// renderSeedText is shared by seed/init text output.
func renderSeedText(v seed.Result) string {
	if v.AlreadySeeded {
		return "demo dataset already present (seed marker found)\n" + renderSeedUser(v.User)
	}
	return fmt.Sprintf("demo dataset seeded: %d customers, %d repairs, %d entries, %d parts, %d estimates, %d attachments\n",
		v.Customers, v.Repairs, v.Entries, v.Parts, v.Estimates, v.Attachments) +
		renderSeedUser(v.User)
}

func runSeed(ctx context.Context, opts options, args []string, getenv Env) (any, error) {
	if len(args) == 0 || args[0] != "demo" {
		return nil, errs.New(errs.CodeUsage, "usage: repairflow seed demo [--demo-user USERNAME]")
	}
	p, err := parseArgs(args[1:], []string{"demo-user"}, nil)
	if err != nil {
		return nil, err
	}
	if err := requirePositional(p, 0, "repairflow seed demo [--demo-user USERNAME]"); err != nil {
		return nil, err
	}
	seedOpts, err := demoOptions(p, getenv)
	if err != nil {
		return nil, err
	}
	home, err := resolveHome(opts, getenv)
	if err != nil {
		return nil, err
	}
	svc, closeDB, err := openService(ctx, home, opts.logger)
	if err != nil {
		return nil, err
	}
	defer closeDB()
	return seed.DemoWithOptions(ctx, svc, seedOpts)
}

// demoOptions reads the optional demo operator account request. The password
// only ever comes from the environment (the same channel as `user add`), and
// nothing is created when no username is asked for.
func demoOptions(p parsedArgs, getenv Env) (seed.Options, error) {
	username := p.get("demo-user")
	if username == "" {
		return seed.Options{}, nil
	}
	password := getenv("REPAIRFLOW_NEW_PASSWORD")
	if password == "" {
		return seed.Options{}, errs.New(errs.CodeUsage,
			"--demo-user needs a password from REPAIRFLOW_NEW_PASSWORD (never from command-line arguments)")
	}
	return seed.Options{User: username, Password: password}, nil
}
