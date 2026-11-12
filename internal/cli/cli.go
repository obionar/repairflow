// Package cli is the command-line adapter. It parses input, renders output
// and maps errors to documented exit codes. Business rules live in internal/app.
package cli

import (
	"context"
	"io"
	"log/slog"
	"strings"

	"repairflow/internal/buildinfo"
	"repairflow/internal/config"
	"repairflow/internal/errs"
	"repairflow/internal/logging"
	"repairflow/internal/workspace"
) // Version of the CLI output contract (see docs/CLI-CONTRACT.md).
const contractVersion = 2

// Env abstracts process environment lookup for tests.
type Env func(key string) string

// EnvDebug enables verbose diagnostics (structured logs and internal stack traces).
const EnvDebug = logging.EnvDebug

// Run executes one CLI invocation and returns the process exit code.
func Run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, getenv Env) int {
	debug := logging.DebugEnabled(false, getenv(EnvDebug))
	opts, rest, err := parseGlobalFlags(args)
	if err != nil {
		return fail(stderr, "", false, err, debug)
	}
	opts.debug = logging.DebugEnabled(opts.debug, getenv(EnvDebug))
	opts.logger = logging.New(opts.debug, stderr).With("component", "cli")
	opts.logger.Debug("command start", "operation", "run", "version", buildinfo.Get().Version)
	if opts.help {
		return writeText(stdout, usageText)
	}
	if len(rest) == 0 {
		if opts.version {
			return ok(stdout, "version", opts.json, buildinfo.Get())
		}
		return writeText(stdout, usageText)
	}

	command := rest[0]
	rest = rest[1:]
	var data any
	switch command {
	case "help":
		return writeText(stdout, usageText)
	case "version":
		if len(rest) > 0 {
			err = errs.New(errs.CodeUsage, "version takes no arguments, got %q", strings.Join(rest, " "))
			break
		}
		data = buildinfo.Get()
	case "init":
		data, err = runInit(ctx, stdin, promptWriter(opts, stdout, stderr), opts, rest, getenv)
	case "config":
		var name string
		name, data, err = runConfig(opts, rest, getenv)
		command = name
	case "customer":
		command = "customer " + sub(rest)
		data, err = runCustomer(ctx, opts, rest, getenv)
	case "repair", "ticket": // "ticket" is the documented one-release alias.
		command = "repair " + sub(rest)
		data, err = runRepair(ctx, opts, rest, getenv)
	case "estimate":
		command = "estimate " + sub(rest)
		data, err = runEstimate(ctx, opts, rest, getenv)
	case "attachment":
		command = "attachment " + sub(rest)
		data, err = runAttachment(ctx, opts, rest, getenv)
	case "backup":
		command = "backup " + sub(rest)
		data, err = runBackup(ctx, opts, rest, getenv)
	case "restore":
		data, err = runRestore(ctx, opts, rest, getenv)
	case "seed":
		command = "seed " + sub(rest)
		data, err = runSeed(ctx, opts, rest, getenv)
	case "demo":
		command = "demo " + sub(rest)
		data, err = runDemo(ctx, opts, rest, getenv)
	case "doctor":
		if len(rest) > 0 {
			err = errs.New(errs.CodeUsage, "doctor takes no arguments, got %q", strings.Join(rest, " "))
			break
		}
		data, err = runDoctor(ctx, opts, rest, getenv)
	case "status":
		if len(rest) > 0 {
			err = errs.New(errs.CodeUsage, "status takes no arguments, got %q", strings.Join(rest, " "))
			break
		}
		data, err = runStatus(ctx, opts, rest, getenv)
	case "cleanup":
		data, err = runCleanup(opts, rest, getenv)
	case "user":
		command = "user " + sub(rest)
		data, err = runUser(ctx, opts, rest, getenv)
	case "serve":
		data, err = runServe(ctx, opts, rest, getenv)
	case "service":
		command = "service " + sub(rest)
		data, err = runService(ctx, opts, rest, getenv)
	case "update":
		command = "update " + sub(rest)
		data, err = runUpdate(ctx, opts, rest, getenv)
	default:
		err = errs.New(errs.CodeUsage, "unknown command %q (run \"repairflow help\")", command)
	}
	if err != nil {
		opts.logger.Debug("command failed", "operation", strings.TrimSpace(command),
			"error_code", errs.CodeOf(err))
		return fail(stderr, strings.TrimSpace(command), opts.json, err, opts.debug)
	}
	opts.logger.Debug("command completed", "operation", command)
	exit := ok(stdout, command, opts.json, data)
	if report, ok := data.(doctorReport); ok && !report.OK {
		return errs.ExitCode(errs.New(errs.CodeConflict, "doctor found unhealthy checks"))
	}
	return exit
}

// sub names the invoked subcommand for the envelope, or "".
func sub(rest []string) string {
	if len(rest) > 0 {
		return rest[0]
	}
	return ""
}

type options struct {
	home    string
	json    bool
	help    bool
	version bool
	debug   bool
	logger  *slog.Logger
}

// parseGlobalFlags extracts the documented global flags from anywhere in the
// argument list so both "repairflow --json init" and "repairflow init --json"
// behave identically. Everything else is left for the subcommand parser.
func parseGlobalFlags(args []string) (options, []string, error) {
	var opts options
	var rest []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--":
			rest = append(rest, args[i+1:]...)
			return opts, rest, nil
		case arg == "--json":
			opts.json = true
		case arg == "--debug":
			opts.debug = true
		case arg == "--help" || arg == "-h":
			opts.help = true
		case arg == "--version":
			opts.version = true
		case arg == "--home":
			if i+1 >= len(args) {
				return opts, nil, errs.New(errs.CodeUsage, "--home requires a path argument")
			}
			i++
			if value, err := homeValue(args[i]); err != nil {
				return opts, nil, err
			} else {
				opts.home = value
			}
		case strings.HasPrefix(arg, "--home="):
			value, err := homeValue(strings.TrimPrefix(arg, "--home="))
			if err != nil {
				return opts, nil, err
			}
			opts.home = value
		default:
			rest = append(rest, arg)
		}
	}
	return opts, rest, nil
}

// promptWriter keeps machine output clean: interactive prompts go to stderr
// in --json mode so stdout stays parseable JSON (CLI contract).
func promptWriter(opts options, stdout, stderr io.Writer) io.Writer {
	if opts.json {
		return stderr
	}
	return stdout
}

func runConfig(opts options, args []string, getenv Env) (string, any, error) {
	if len(args) == 0 {
		return "config", nil, errs.New(errs.CodeUsage, "usage: repairflow config check")
	}
	if args[0] != "check" || len(args) > 1 {
		return "config", nil, errs.New(errs.CodeUsage, "unknown config subcommand %q (supported: check)", strings.Join(args, " "))
	}
	home, err := resolveHome(opts, getenv)
	if err != nil {
		return "config", nil, err
	}
	status, err := workspace.Check(home)
	return "config check", status, err
}

func resolveHome(opts options, getenv Env) (string, error) {
	return config.ResolveHome(opts.home, getenv(config.EnvHome), getenv(config.EnvRefusedRoots))
}

// homeValue validates a --home value so a forgotten argument cannot silently
// consume the next flag as a path.
func homeValue(value string) (string, error) {
	if value == "" {
		return "", errs.New(errs.CodeUsage, "--home requires a path argument")
	}
	if strings.HasPrefix(value, "-") {
		return "", errs.New(errs.CodeUsage, "--home value %q looks like a flag; use --home PATH or --home=PATH", value)
	}
	return value, nil
}
