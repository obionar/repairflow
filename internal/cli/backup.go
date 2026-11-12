package cli

import (
	"context"
	"path/filepath"
	"strconv"

	"repairflow/internal/backup"
	"repairflow/internal/errs"
)

// Backup/restore CLI. Backups are single verified archives
// (.rfbak); restore always targets an empty or new workspace.

func runBackup(ctx context.Context, opts options, args []string, getenv Env) (any, error) {
	if len(args) == 0 {
		return nil, errs.New(errs.CodeUsage, "usage: repairflow backup <create|verify> ...")
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "create":
		p, err := parseArgs(rest, []string{"output", "actor"}, nil)
		if err != nil {
			return nil, err
		}
		if err := requirePositional(p, 0, "repairflow backup create --output PATH"); err != nil {
			return nil, err
		}
		out := p.get("output")
		if out == "" {
			return nil, errs.New(errs.CodeUsage, "backup create requires --output PATH")
		}
		if !filepath.IsAbs(out) {
			return nil, errs.New(errs.CodeValidation, "backup output must be an absolute path")
		}
		home, err := resolveHome(opts, getenv)
		if err != nil {
			return nil, err
		}
		actor := p.get("actor")
		if actor == "" {
			actor = getenv("USER")
		}
		return backup.Create(ctx, home, out, actor)
	case "verify":
		p, err := parseArgs(rest, nil, nil)
		if err != nil {
			return nil, err
		}
		if err := requirePositional(p, 1, "repairflow backup verify ARCHIVE"); err != nil {
			return nil, err
		}
		return backup.Verify(ctx, p.positional[0])
	default:
		return nil, errs.New(errs.CodeUsage, "unknown backup subcommand %q (supported: create, verify)", sub)
	}
}

// runRestore restores an archive into an empty workspace target (--home).
func runRestore(ctx context.Context, opts options, args []string, getenv Env) (any, error) {
	p, err := parseArgs(args, nil, nil)
	if err != nil {
		return nil, err
	}
	if err := requirePositional(p, 1, "repairflow restore ARCHIVE --home PATH"); err != nil {
		return nil, err
	}
	home, err := resolveHome(opts, getenv)
	if err != nil {
		return nil, err
	}
	manifest, err := backup.Restore(ctx, p.positional[0], home)
	if err != nil {
		return nil, err
	}
	return restoreResult{Home: home, Manifest: *manifest}, nil
}

// restoreResult is the machine-readable restore receipt.
type restoreResult struct {
	Home     string          `json:"home"`
	Manifest backup.Manifest `json:"manifest"`
}

func renderResult(r *backup.Result) string {
	m := r.Manifest
	return "backup written: " + r.Archive + "\n" +
		"  archive sha256: " + r.SHA256 + "\n" +
		"  schema: " + itoaInt(m.SchemaVersion) + ", created: " + m.CreatedAt + " by " + m.CreatedBy + "\n" +
		"  contents: database, " + itoaInt(len(m.Attachments)) + " attachment file(s)" + configSuffix(m) + "\n"
}

func configSuffix(m backup.Manifest) string {
	if m.Config != nil {
		return ", config"
	}
	return ""
}

func renderManifest(prefix string, m *backup.Manifest) string {
	return prefix + " manifest: format " + itoaInt(m.FormatVersion) + ", schema " + itoaInt(m.SchemaVersion) +
		", " + itoaInt(len(m.Attachments)) + " attachment file(s), created " + m.CreatedAt + " by " + m.CreatedBy + "\n"
}

func itoaInt(v int) string { return strconv.Itoa(v) }

func itoaBool(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
