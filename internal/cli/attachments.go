package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"repairflow/internal/app"
	"repairflow/internal/errs"
	"repairflow/internal/files"
)

// Attachment CLI: full parity with the web
// UI through the same services. Original bytes are streamed, hash-verified
// and exported byte-identically.

func runAttachment(ctx context.Context, opts options, args []string, getenv Env) (any, error) {
	if len(args) == 0 {
		return nil, errs.New(errs.CodeUsage, "usage: repairflow attachment <add|list|show|export|caption|link|unlink|detach|restore|purge> ...")
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
		p, err := parseArgs(rest, []string{"repair", "ticket", "file", "caption", "entry", "source", "actor"}, nil)
		if err != nil {
			return nil, err
		}
		if err := requirePositional(p, 0, "repairflow attachment add --repair ID --file PATH [--caption C] [--entry E] [--source S]"); err != nil {
			return nil, err
		}
		repairID, err := parseID(repairFlag(p), "repair")
		if err != nil {
			return nil, err
		}
		srcPath := p.get("file")
		if srcPath == "" {
			return nil, errs.New(errs.CodeUsage, "attachment add requires --file PATH")
		}
		entryID := int64(0)
		if p.get("entry") != "" {
			if entryID, err = parseID(p.get("entry"), "entry"); err != nil {
				return nil, err
			}
		}
		actor := resolveActor(p, getenv)
		source := p.get("source")
		if source == "" {
			source = "cli"
		}
		f, err := os.Open(srcPath) // read-only; user-selected source file
		if err != nil {
			return nil, errs.Wrap(errs.CodeValidation, err, "cannot read source file %q", srcPath)
		}
		defer func() { _ = f.Close() }()
		return svc.AddAttachment(ctx, actor, repairID, entryID, filepath.Base(srcPath), f, p.get("caption"), source)
	case "list":
		p, err := parseArgs(rest, []string{"repair", "ticket"}, []string{"all"})
		if err != nil {
			return nil, err
		}
		if err := requirePositional(p, 0, "repairflow attachment list --repair ID [--all]"); err != nil {
			return nil, err
		}
		repairID, err := parseID(repairFlag(p), "repair")
		if err != nil {
			return nil, err
		}
		return svc.ListRepairAttachments(ctx, repairID, p.flag("all"))
	case "show":
		p, err := parseArgs(rest, nil, nil)
		if err != nil {
			return nil, err
		}
		if err := requirePositional(p, 1, "repairflow attachment show ID"); err != nil {
			return nil, err
		}
		id, err := parseID(p.positional[0], "attachment")
		if err != nil {
			return nil, err
		}
		return svc.GetAttachment(ctx, id)
	case "export":
		p, err := parseArgs(rest, []string{"out"}, nil)
		if err != nil {
			return nil, err
		}
		if err := requirePositional(p, 1, "repairflow attachment export ID --out PATH"); err != nil {
			return nil, err
		}
		id, err := parseID(p.positional[0], "attachment")
		if err != nil {
			return nil, err
		}
		out := p.get("out")
		if out == "" {
			return nil, errs.New(errs.CodeUsage, "attachment export requires --out PATH")
		}
		reader, meta, err := svc.OpenBytes(ctx, id)
		if err != nil {
			return nil, err
		}
		defer func() { _ = reader.Close() }()
		storageName, err := svc.AttachmentStorageName(ctx, id)
		if err != nil {
			return nil, err
		}
		n, hash, err := files.New(home).Export(meta.RepairID, storageName, out, meta.SHA256)
		if err != nil {
			return nil, err
		}
		return exportResult{ID: id, Path: out, SizeBytes: n, SHA256: hash}, nil
	case "caption":
		p, err := parseArgs(rest, []string{"caption", "actor"}, nil)
		if err != nil {
			return nil, err
		}
		if err := requirePositional(p, 1, "repairflow attachment caption ID --caption TEXT"); err != nil {
			return nil, err
		}
		id, err := parseID(p.positional[0], "attachment")
		if err != nil {
			return nil, err
		}
		if _, ok := p.values["caption"]; !ok {
			return nil, errs.New(errs.CodeUsage, "attachment caption requires --caption TEXT")
		}
		actor := resolveActor(p, getenv)
		return svc.SetCaption(ctx, actor, id, p.get("caption"))
	case "link", "unlink":
		p, err := parseArgs(rest, []string{"entry", "actor"}, nil)
		if err != nil {
			return nil, err
		}
		if err := requirePositional(p, 1, "repairflow attachment "+sub+" ID --entry ENTRY_ID"); err != nil {
			return nil, err
		}
		id, err := parseID(p.positional[0], "attachment")
		if err != nil {
			return nil, err
		}
		entryID, err := parseID(p.get("entry"), "entry")
		if err != nil {
			return nil, err
		}
		actor := resolveActor(p, getenv)
		if sub == "link" {
			return svc.LinkEntry(ctx, actor, id, entryID)
		}
		return svc.UnlinkEntry(ctx, actor, id, entryID)
	case "detach", "restore":
		p, err := parseArgs(rest, []string{"actor"}, nil)
		if err != nil {
			return nil, err
		}
		if err := requirePositional(p, 1, "repairflow attachment "+sub+" ID"); err != nil {
			return nil, err
		}
		id, err := parseID(p.positional[0], "attachment")
		if err != nil {
			return nil, err
		}
		actor := resolveActor(p, getenv)
		if sub == "detach" {
			return svc.Detach(ctx, actor, id)
		}
		return svc.Restore(ctx, actor, id)
	case "purge":
		p, err := parseArgs(rest, []string{"actor"}, []string{"confirm"})
		if err != nil {
			return nil, err
		}
		if err := requirePositional(p, 1, "repairflow attachment purge ID --confirm"); err != nil {
			return nil, err
		}
		id, err := parseID(p.positional[0], "attachment")
		if err != nil {
			return nil, err
		}
		actor := resolveActor(p, getenv)
		return svc.Purge(ctx, actor, id, p.flag("confirm"))
	default:
		return nil, errs.New(errs.CodeUsage, "unknown attachment subcommand %q (supported: add, list, show, export, caption, link, unlink, detach, restore, purge)", sub)
	}
}

// exportResult is the machine-readable export receipt.
type exportResult struct {
	ID        int64  `json:"id"`
	Path      string `json:"path"`
	SizeBytes int64  `json:"size_bytes"`
	SHA256    string `json:"sha256"`
}

func renderAttachment(a app.Attachment) string {
	out := fmt.Sprintf("attachment #%d [%s] %s (%s, %d bytes, sha256 %s)\n",
		a.ID, a.State, a.DisplayName, a.MediaType, a.SizeBytes, a.SHA256[:12])
	if a.Caption != "" {
		out += "  caption: " + a.Caption + "\n"
	}
	if len(a.EntryIDs) > 0 {
		out += fmt.Sprintf("  entries: %v\n", a.EntryIDs)
	}
	if a.State == "detached" {
		out += fmt.Sprintf("  detached %s; purge allowed after %s (recoverable with attachment restore)\n", a.DetachedAt, a.PurgeAfter)
	}
	out += fmt.Sprintf("  source: %s by %s at %s\n", a.Source, a.CreatedBy, a.CreatedAt)
	return out
}

// repairFlag reads the owning repair id, accepting the legacy --ticket spelling
// for one release.
func repairFlag(p parsedArgs) string {
	if v := p.get("repair"); v != "" {
		return v
	}
	return p.get("ticket")
}
