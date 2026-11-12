package cli

import (
	"context"

	"repairflow/internal/app"
	"repairflow/internal/errs"
)

func runRepair(ctx context.Context, opts options, args []string, getenv Env) (any, error) {
	if len(args) == 0 {
		return nil, errs.New(errs.CodeUsage, "usage: repairflow repair <add|list|show|update|stage|entry> ...")
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
		p, err := parseArgs(rest, []string{"customer", "device", "device-type", "brand", "model", "serial", "issue", "notes", "actor", "idempotency-key"}, nil)
		if err != nil {
			return nil, err
		}
		if err := requirePositional(p, 0, "repairflow repair add [--customer ID] --device D --issue I [--notes N] [--idempotency-key K]"); err != nil {
			return nil, err
		}
		var customerID int64
		if p.get("customer") != "" {
			var err error
			customerID, err = parseID(p.get("customer"), "customer")
			if err != nil {
				return nil, err
			}
		}
		actor := resolveActor(p, getenv)
		return svc.CreateRepairWithKey(ctx, actor, app.RepairInput{
			CustomerID: customerID, Device: p.get("device"), DeviceType: p.get("device-type"),
			Brand: p.get("brand"), Model: p.get("model"), Serial: p.get("serial"),
			Issue: p.get("issue"), Notes: p.get("notes"),
		}, app.NormalizeIdempotencyKey(p.get("idempotency-key")))
	case "list":
		p, err := parseArgs(rest, []string{"search", "stage", "customer", "page", "per-page"}, []string{"include-archived"})
		if err != nil {
			return nil, err
		}
		if err := requirePositional(p, 0, "repairflow repair list [--search TEXT] [--stage S] [--customer ID]"); err != nil {
			return nil, err
		}
		page, perPage, err := paginationArgs(p)
		if err != nil {
			return nil, err
		}
		var customerID int64
		if p.get("customer") != "" {
			if customerID, err = parseID(p.get("customer"), "customer"); err != nil {
				return nil, err
			}
		}
		return svc.ListRepairs(ctx, app.RepairFilter{
			Search: p.get("search"), Stage: p.get("stage"), CustomerID: customerID, Page: page, PerPage: perPage,
			IncludeArchived: p.flag("include-archived"),
		})
	case "show":
		p, err := parseArgs(rest, nil, []string{"include-deleted"})
		if err != nil {
			return nil, err
		}
		if err := requirePositional(p, 1, "repairflow repair show ID [--include-deleted]"); err != nil {
			return nil, err
		}
		id, err := parseID(p.positional[0], "repair")
		if err != nil {
			return nil, err
		}
		return svc.GetRepair(ctx, id, p.flag("include-deleted"))
	case "update":
		p, err := parseArgs(rest, []string{"customer", "device", "device-type", "brand", "model", "serial", "issue", "notes", "revision", "actor"}, []string{"no-customer"})
		if err != nil {
			return nil, err
		}
		if err := requirePositional(p, 1, "repairflow repair update ID [--customer ID|--no-customer] [--device D] [--issue I] [--notes N] [--revision R]"); err != nil {
			return nil, err
		}
		id, err := parseID(p.positional[0], "repair")
		if err != nil {
			return nil, err
		}
		actor := resolveActor(p, getenv)
		patch := app.RepairPatch{}
		if _, ok := p.values["customer"]; ok && p.flag("no-customer") {
			return nil, errs.New(errs.CodeUsage, "--customer and --no-customer cannot be combined")
		}
		if _, ok := p.values["customer"]; ok {
			customerID, err := parseID(p.get("customer"), "customer")
			if err != nil {
				return nil, err
			}
			patch.CustomerID = &customerID
		}
		if p.flag("no-customer") {
			zero := int64(0)
			patch.CustomerID = &zero
		}
		if _, ok := p.values["device"]; ok {
			v := p.get("device")
			patch.Device = &v
		}
		if _, ok := p.values["device-type"]; ok {
			v := p.get("device-type")
			patch.DeviceType = &v
		}
		for _, field := range []struct {
			name   string
			target **string
		}{
			{"brand", &patch.Brand}, {"model", &patch.Model}, {"serial", &patch.Serial},
		} {
			if v, ok := p.values[field.name]; ok {
				*field.target = &v
			}
		}
		if _, ok := p.values["issue"]; ok {
			v := p.get("issue")
			patch.Issue = &v
		}
		if _, ok := p.values["notes"]; ok {
			v := p.get("notes")
			patch.Notes = &v
		}
		return svc.UpdateRepairDetails(ctx, actor, id, patch, p.get("revision"))
	case "stage":
		p, err := parseArgs(rest, []string{"actor"}, []string{"reopen"})
		if err != nil {
			return nil, err
		}
		if err := requirePositional(p, 2, "repairflow repair stage ID STAGE [--reopen]"); err != nil {
			return nil, err
		}
		id, err := parseID(p.positional[0], "repair")
		if err != nil {
			return nil, err
		}
		actor := resolveActor(p, getenv)
		return svc.SetRepairStage(ctx, actor, id, p.positional[1], p.flag("reopen"))
	case "entry":
		p, err := parseArgs(rest, []string{"kind", "body", "actor"}, nil)
		if err != nil {
			return nil, err
		}
		if err := requirePositional(p, 1, "repairflow repair entry ID --kind K --body B"); err != nil {
			return nil, err
		}
		id, err := parseID(p.positional[0], "repair")
		if err != nil {
			return nil, err
		}
		actor := resolveActor(p, getenv)
		return svc.AddRepairEntry(ctx, actor, id, p.get("kind"), p.get("body"))
	case "pickup":
		p, err := parseArgs(rest, []string{"at", "by", "notes", "actor"}, nil)
		if err != nil {
			return nil, err
		}
		if err := requirePositional(p, 1, "repairflow repair pickup ID --at TIME --by NAME [--notes N]"); err != nil {
			return nil, err
		}
		id, err := parseID(p.positional[0], "repair")
		if err != nil {
			return nil, err
		}
		actor := resolveActor(p, getenv)
		return svc.SetRepairPickup(ctx, actor, id, p.get("at"), p.get("by"), p.get("notes"))
	case "entry-edit":
		p, err := parseArgs(rest, []string{"kind", "body", "actor"}, nil)
		if err != nil {
			return nil, err
		}
		if err := requirePositional(p, 1, "repairflow repair entry-edit ENTRY_ID [--kind K] --body B"); err != nil {
			return nil, err
		}
		id, err := parseID(p.positional[0], "entry")
		if err != nil {
			return nil, err
		}
		actor := resolveActor(p, getenv)
		var kind, body *string
		if _, ok := p.values["kind"]; ok {
			v := p.get("kind")
			kind = &v
		}
		if _, ok := p.values["body"]; ok {
			v := p.get("body")
			body = &v
		}
		if kind == nil && body == nil {
			return nil, errs.New(errs.CodeUsage, "entry-edit requires --kind and/or --body")
		}
		return svc.EditRepairEntry(ctx, actor, id, kind, body)
	case "entry-delete":
		p, err := parseArgs(rest, []string{"actor"}, nil)
		if err != nil {
			return nil, err
		}
		if err := requirePositional(p, 1, "repairflow repair entry-delete ENTRY_ID"); err != nil {
			return nil, err
		}
		id, err := parseID(p.positional[0], "entry")
		if err != nil {
			return nil, err
		}
		actor := resolveActor(p, getenv)
		return svc.DeleteRepairEntry(ctx, actor, id)
	case "entry-restore":
		p, err := parseArgs(rest, []string{"actor"}, nil)
		if err != nil {
			return nil, err
		}
		if err := requirePositional(p, 1, "repairflow repair entry-restore ENTRY_ID"); err != nil {
			return nil, err
		}
		id, err := parseID(p.positional[0], "entry")
		if err != nil {
			return nil, err
		}
		actor := resolveActor(p, getenv)
		return svc.RestoreRepairEntry(ctx, actor, id)
	case "archive", "unarchive":
		p, err := parseArgs(rest, []string{"actor"}, nil)
		if err != nil {
			return nil, err
		}
		if err := requirePositional(p, 1, "repairflow repair "+sub+" ID"); err != nil {
			return nil, err
		}
		id, err := parseID(p.positional[0], "repair")
		if err != nil {
			return nil, err
		}
		actor := resolveActor(p, getenv)
		return svc.ArchiveRepair(ctx, actor, id, sub == "archive")
	case "part-add", "part-list", "part-update", "part-remove", "part-restore":
		return runPart(ctx, svc, sub, rest, getenv)
	default:
		return nil, errs.New(errs.CodeUsage, "unknown repair subcommand %q (supported: add, list, show, update, stage, entry, entry-edit, entry-delete, entry-restore, pickup, archive, unarchive, part-add, part-list, part-update, part-remove, part-restore)", sub)
	}
}
