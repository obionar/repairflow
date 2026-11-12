package cli

import (
	"context"
	"io"
	"os"
	"strings"

	"repairflow/internal/config"
	"repairflow/internal/errs"
	"repairflow/internal/i18n"
	"repairflow/internal/seed"
	"repairflow/internal/workspace"
)

// initDemoResult reports workspace initialization plus an optional demo seed.
type initDemoResult struct {
	Init workspace.InitResult `json:"init"`
	Seed *seed.Result         `json:"seed,omitempty"`
}

// workspaceInitWith initializes the workspace and applies the chosen web port
// and interface language to a config this call created. An existing config is
// never rewritten.
func workspaceInitWith(home string, port int, language string) (*workspace.InitResult, error) {
	res, err := workspace.Init(home)
	if err != nil {
		return nil, err
	}
	defaults := config.Default()
	if res.AlreadyInitialized || (port == defaults.Web.Port && (language == "" || language == defaults.UI.Language)) {
		return res, nil
	}
	cfg, err := config.Load(res.ConfigPath)
	if err != nil {
		return nil, err
	}
	if port != defaults.Web.Port {
		cfg.Web.Port = port
	}
	if language != "" {
		cfg.UI.Language = language
	}
	if err := os.WriteFile(res.ConfigPath+".tmp", config.Render(cfg), 0o600); err != nil {
		return nil, errs.Wrap(errs.CodeInternal, err, "cannot write config")
	}
	if err := os.Rename(res.ConfigPath+".tmp", res.ConfigPath); err != nil {
		return nil, errs.Wrap(errs.CodeInternal, err, "cannot publish config")
	}
	return res, nil
}

func runInit(ctx context.Context, stdin io.Reader, stdout io.Writer, opts options, args []string, getenv Env) (any, error) {
	p, err := parseArgs(args, []string{"demo-user", "language"}, []string{"demo", "interactive"})
	if err != nil {
		return nil, err
	}
	if len(p.positional) > 0 {
		return nil, errs.New(errs.CodeUsage, "init takes no positional arguments, got %q", strings.Join(p.positional, " "))
	}
	if p.flag("interactive") {
		return runInitWizard(ctx, stdin, stdout, opts, getenv)
	}
	if !p.flag("demo") && p.get("demo-user") != "" {
		return nil, errs.New(errs.CodeUsage, "--demo-user requires --demo")
	}
	language := ""
	if raw := p.get("language"); raw != "" {
		tr, err := i18n.New()
		if err != nil {
			return nil, errs.Wrap(errs.CodeInternal, err, "cannot load interface translations")
		}
		language = i18n.Normalize(raw)
		if !tr.Has(language) {
			codes := make([]string, 0, 4)
			for _, l := range tr.Languages() {
				codes = append(codes, l.Code)
			}
			return nil, errs.New(errs.CodeValidation,
				"unknown interface language %q (available: %s)", raw, strings.Join(codes, ", "))
		}
	}
	seedOpts, err := demoOptions(p, getenv)
	if err != nil {
		return nil, err
	}
	home, err := resolveHome(opts, getenv)
	if err != nil {
		return nil, err
	}
	res, err := workspaceInitWith(home, config.Default().Web.Port, language)
	if err != nil {
		return nil, err
	}
	if !p.flag("demo") {
		return res, nil
	}
	svc, closeDB, err := openService(ctx, home, opts.logger)
	if err != nil {
		return nil, err
	}
	defer closeDB()
	seeded, err := seed.DemoWithOptions(ctx, svc, seedOpts)
	if err != nil {
		return nil, err
	}
	return initDemoResult{Init: *res, Seed: &seeded}, nil
}
