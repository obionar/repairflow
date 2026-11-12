package cli

import (
	"context"
	"fmt"

	"repairflow/internal/app"
	"repairflow/internal/errs"
)

// Parts and estimates CLI. Money and quantity flags
// take decimal strings ("12.34", "1.5"); the JSON contract carries integer
// cents and quantity_milli.

func runPart(ctx context.Context, svc *app.Service, sub string, rest []string, getenv Env) (any, error) {
	switch sub {
	case "part-add":
		p, err := parseArgs(rest, []string{"description", "qty", "unit", "cost", "price", "notes", "actor"}, nil)
		if err != nil {
			return nil, err
		}
		if err := requirePositional(p, 1, "repairflow repair part-add TICKET_ID --description D --qty Q [--unit U] [--cost C] --price P"); err != nil {
			return nil, err
		}
		repairID, err := parseID(p.positional[0], "repair")
		if err != nil {
			return nil, err
		}
		actor := resolveActor(p, getenv)
		qty, err := app.ParseQuantityMilli("qty", p.get("qty"))
		if err != nil {
			return nil, err
		}
		price, err := app.ParseCents("price", p.get("price"))
		if err != nil {
			return nil, err
		}
		var cost *int64
		if raw := p.get("cost"); raw != "" {
			c, err := app.ParseCents("cost", raw)
			if err != nil {
				return nil, err
			}
			cost = &c
		}
		return svc.AddRepairPart(ctx, actor, repairID, app.PartInput{
			Description: p.get("description"), QuantityMilli: qty, Unit: p.get("unit"),
			UnitCostCents: cost, UnitPriceCents: price, Notes: p.get("notes"),
		})
	case "part-list":
		p, err := parseArgs(rest, nil, []string{"include-deleted"})
		if err != nil {
			return nil, err
		}
		if err := requirePositional(p, 1, "repairflow repair part-list TICKET_ID [--include-deleted]"); err != nil {
			return nil, err
		}
		repairID, err := parseID(p.positional[0], "repair")
		if err != nil {
			return nil, err
		}
		return svc.ListRepairParts(ctx, repairID, p.flag("include-deleted"))
	case "part-update":
		p, err := parseArgs(rest, []string{"description", "qty", "unit", "cost", "price", "notes", "revision", "actor"}, []string{"clear-cost"})
		if err != nil {
			return nil, err
		}
		if err := requirePositional(p, 1, "repairflow repair part-update PART_ID [--qty Q] [--price P] ... [--revision R]"); err != nil {
			return nil, err
		}
		partID, err := parseID(p.positional[0], "part")
		if err != nil {
			return nil, err
		}
		actor := resolveActor(p, getenv)
		patch := app.PartPatch{ClearCost: p.flag("clear-cost")}
		if v, ok := p.values["description"]; ok {
			patch.Description = &v
		}
		if v, ok := p.values["unit"]; ok {
			patch.Unit = &v
		}
		if v, ok := p.values["notes"]; ok {
			patch.Notes = &v
		}
		if v, ok := p.values["qty"]; ok {
			q, err := app.ParseQuantityMilli("qty", v)
			if err != nil {
				return nil, err
			}
			patch.QuantityMilli = &q
		}
		if v, ok := p.values["price"]; ok {
			c, err := app.ParseCents("price", v)
			if err != nil {
				return nil, err
			}
			patch.UnitPriceCents = &c
		}
		if v, ok := p.values["cost"]; ok {
			c, err := app.ParseCents("cost", v)
			if err != nil {
				return nil, err
			}
			patch.UnitCostCents = &c
		}
		if patch.Description == nil && patch.QuantityMilli == nil && patch.Unit == nil &&
			patch.UnitCostCents == nil && !patch.ClearCost && patch.UnitPriceCents == nil && patch.Notes == nil {
			return nil, errs.New(errs.CodeUsage, "part-update requires at least one field flag")
		}
		return svc.UpdateRepairPart(ctx, actor, partID, patch, p.get("revision"))
	case "part-remove", "part-restore":
		p, err := parseArgs(rest, []string{"actor"}, nil)
		if err != nil {
			return nil, err
		}
		if err := requirePositional(p, 1, "repairflow repair "+sub+" PART_ID"); err != nil {
			return nil, err
		}
		partID, err := parseID(p.positional[0], "part")
		if err != nil {
			return nil, err
		}
		actor := resolveActor(p, getenv)
		if sub == "part-remove" {
			return svc.DeleteRepairPart(ctx, actor, partID)
		}
		return svc.RestoreRepairPart(ctx, actor, partID)
	default:
		return nil, errs.New(errs.CodeUsage, "unknown part subcommand %q", sub)
	}
}

func runEstimate(ctx context.Context, opts options, args []string, getenv Env) (any, error) {
	if len(args) == 0 {
		return nil, errs.New(errs.CodeUsage, "usage: repairflow estimate <create|list|show|accept> ...")
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
	case "create":
		p, err := parseArgs(rest, []string{"note", "discount-amount", "discount-percent", "actor"}, nil)
		if err != nil {
			return nil, err
		}
		if err := requirePositional(p, 1, "repairflow estimate create TICKET_ID [--note N] [--discount-amount C | --discount-percent P]"); err != nil {
			return nil, err
		}
		if p.get("discount-amount") != "" && p.get("discount-percent") != "" {
			return nil, errs.New(errs.CodeUsage, "--discount-amount and --discount-percent are mutually exclusive")
		}
		repairID, err := parseID(p.positional[0], "repair")
		if err != nil {
			return nil, err
		}
		actor := resolveActor(p, getenv)
		in := app.EstimateInput{Note: p.get("note"), DiscountKind: "none"}
		if raw := p.get("discount-amount"); raw != "" {
			cents, err := app.ParseCents("discount-amount", raw)
			if err != nil {
				return nil, err
			}
			in.DiscountKind, in.DiscountValue = "amount", cents
		}
		if raw := p.get("discount-percent"); raw != "" {
			pct, err := app.ParsePercent("discount-percent", raw)
			if err != nil {
				return nil, err
			}
			in.DiscountKind, in.DiscountValue = "percent", pct
		}
		return svc.CreateEstimate(ctx, actor, repairID, in)
	case "list":
		p, err := parseArgs(rest, nil, nil)
		if err != nil {
			return nil, err
		}
		if err := requirePositional(p, 1, "repairflow estimate list TICKET_ID"); err != nil {
			return nil, err
		}
		repairID, err := parseID(p.positional[0], "repair")
		if err != nil {
			return nil, err
		}
		return svc.ListEstimates(ctx, repairID)
	case "show", "accept":
		p, err := parseArgs(rest, []string{"actor"}, nil)
		if err != nil {
			return nil, err
		}
		if err := requirePositional(p, 1, "repairflow estimate "+sub+" ESTIMATE_ID"); err != nil {
			return nil, err
		}
		id, err := parseID(p.positional[0], "estimate")
		if err != nil {
			return nil, err
		}
		if sub == "show" {
			return svc.GetEstimate(ctx, id)
		}
		actor := resolveActor(p, getenv)
		return svc.AcceptEstimate(ctx, actor, id)
	default:
		return nil, errs.New(errs.CodeUsage, "unknown estimate subcommand %q (supported: create, list, show, accept)", sub)
	}
}

// ---- text rendering ------------------------------------------------------

func renderPart(p app.Part) string {
	line := fmt.Sprintf("part #%d %s: %s %s x %s = %s %s",
		p.ID, p.Description, app.FormatQuantityMilli(p.QuantityMilli), p.Unit,
		app.FormatCents(p.UnitPriceCents), app.FormatCents(app.LineTotalCents(p.QuantityMilli, p.UnitPriceCents)), p.Currency)
	if p.UnitCostCents != nil {
		line += fmt.Sprintf(" (cost %s)", app.FormatCents(*p.UnitCostCents))
	} else {
		line += " (cost unknown)"
	}
	if p.DeletedAt != "" {
		line += " [removed]"
	}
	return line + fmt.Sprintf(" rev %s\n", p.Revision[:12])
}

func renderEstimate(e app.Estimate) string {
	out := fmt.Sprintf("estimate #%d v%d [%s] repair #%d (%s)\n", e.ID, e.Version, e.Status, e.RepairID, e.CreatedBy)
	for _, l := range e.Lines {
		out += fmt.Sprintf("  %s %s %s x %s = %s\n", l.Description, app.FormatQuantityMilli(l.QuantityMilli), l.Unit,
			app.FormatCents(l.UnitPriceCents), app.FormatCents(l.LineTotalCents))
	}
	out += fmt.Sprintf("  subtotal %s", app.FormatCents(e.SubtotalCents))
	if e.DiscountKind != "none" {
		out += fmt.Sprintf(" - discount(%s %d) %s", e.DiscountKind, e.DiscountValue, app.FormatCents(e.DiscountCents))
	}
	out += fmt.Sprintf(" = total %s %s\n", app.FormatCents(e.TotalCents), e.Currency)
	if e.Note != "" {
		out += "  note: " + e.Note + "\n"
	}
	return out
}
