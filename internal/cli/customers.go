package cli

import (
	"context"

	"repairflow/internal/app"
	"repairflow/internal/errs"
)

func runCustomer(ctx context.Context, opts options, args []string, getenv Env) (any, error) {
	if len(args) == 0 {
		return nil, errs.New(errs.CodeUsage, "usage: repairflow customer <add|list|show|update|history> ...")
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
		p, err := parseArgs(rest, []string{"name", "phone", "email", "address", "notes", "actor", "idempotency-key"}, nil)
		if err != nil {
			return nil, err
		}
		if err := requirePositional(p, 0, "repairflow customer add --name NAME [--phone P] [--email E] [--notes N] [--idempotency-key K]"); err != nil {
			return nil, err
		}
		actor := resolveActor(p, getenv)
		return svc.CreateCustomerWithKey(ctx, actor, app.CustomerInput{
			Name: p.get("name"), Phone: p.get("phone"), Email: p.get("email"), Address: p.get("address"), Notes: p.get("notes"),
		}, app.NormalizeIdempotencyKey(p.get("idempotency-key")))
	case "list":
		p, err := parseArgs(rest, []string{"search", "page", "per-page"}, []string{"include-archived"})
		if err != nil {
			return nil, err
		}
		if err := requirePositional(p, 0, "repairflow customer list [--search TEXT] [--page N] [--per-page N]"); err != nil {
			return nil, err
		}
		page, perPage, err := paginationArgs(p)
		if err != nil {
			return nil, err
		}
		return svc.ListCustomers(ctx, app.CustomerFilter{Search: p.get("search"), Page: page, PerPage: perPage, IncludeArchived: p.flag("include-archived")})
	case "show":
		p, err := parseArgs(rest, nil, nil)
		if err != nil {
			return nil, err
		}
		if err := requirePositional(p, 1, "repairflow customer show ID"); err != nil {
			return nil, err
		}
		id, err := parseID(p.positional[0], "customer")
		if err != nil {
			return nil, err
		}
		c, err := svc.GetCustomer(ctx, id)
		if err != nil {
			return nil, err
		}
		history, err := svc.CustomerHistory(ctx, id)
		if err != nil {
			return nil, err
		}
		return customerShow{Customer: c, History: history}, nil
	case "update":
		p, err := parseArgs(rest, []string{"name", "phone", "email", "address", "notes", "revision", "actor"}, nil)
		if err != nil {
			return nil, err
		}
		if err := requirePositional(p, 1, "repairflow customer update ID [--name ...] [--revision R]"); err != nil {
			return nil, err
		}
		id, err := parseID(p.positional[0], "customer")
		if err != nil {
			return nil, err
		}
		actor := resolveActor(p, getenv)
		patch := app.CustomerPatch{}
		if _, ok := p.values["name"]; ok {
			v := p.get("name")
			patch.Name = &v
		}
		if _, ok := p.values["phone"]; ok {
			v := p.get("phone")
			patch.Phone = &v
		}
		if _, ok := p.values["email"]; ok {
			v := p.get("email")
			patch.Email = &v
		}
		if _, ok := p.values["address"]; ok {
			v := p.get("address")
			patch.Address = &v
		}
		if _, ok := p.values["notes"]; ok {
			v := p.get("notes")
			patch.Notes = &v
		}
		return svc.UpdateCustomer(ctx, actor, id, patch, p.get("revision"))
	case "history":
		p, err := parseArgs(rest, nil, nil)
		if err != nil {
			return nil, err
		}
		if err := requirePositional(p, 1, "repairflow customer history ID"); err != nil {
			return nil, err
		}
		id, err := parseID(p.positional[0], "customer")
		if err != nil {
			return nil, err
		}
		return svc.CustomerHistory(ctx, id)
	case "archive", "unarchive":
		p, err := parseArgs(rest, []string{"actor"}, nil)
		if err != nil {
			return nil, err
		}
		if err := requirePositional(p, 1, "repairflow customer "+sub+" ID"); err != nil {
			return nil, err
		}
		id, err := parseID(p.positional[0], "customer")
		if err != nil {
			return nil, err
		}
		actor := resolveActor(p, getenv)
		return svc.ArchiveCustomer(ctx, actor, id, sub == "archive")
	default:
		return nil, errs.New(errs.CodeUsage, "unknown customer subcommand %q (supported: add, list, show, update, history, archive, unarchive)", sub)
	}
}

type customerShow struct {
	Customer app.Customer          `json:"customer"`
	History  []app.CustomerHistory `json:"history"`
}
