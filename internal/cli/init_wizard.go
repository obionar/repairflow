package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"repairflow/internal/app"
	"repairflow/internal/config"
	"repairflow/internal/errs"
	"repairflow/internal/i18n"
	"repairflow/internal/seed"

	"golang.org/x/term"
)

// Interactive init wizard: workspace location, operator account,
// web port, optional demo data. Non-interactive `init` is unchanged; the
// wizard only runs with --interactive. Every answer has a safe default and
// every value is validated by the same code paths as the non-interactive
// commands.

type wizardAnswers struct {
	home       string
	username   string
	password   string
	webPort    int
	language   string
	seedDemo   bool
	createUser bool
}

// runInitWizard prompts on out and reads answers from in.
func runInitWizard(ctx context.Context, in io.Reader, out io.Writer, opts options, getenv Env) (any, error) {
	reader := bufio.NewReader(in)
	def, err := resolveHome(opts, getenv)
	if err != nil {
		return nil, err
	}
	a := wizardAnswers{home: def, webPort: 8091}

	if a.home, err = prompt(reader, out, "Workspace location", def); err != nil {
		return nil, err
	}
	if a.username, err = prompt(reader, out, "Operator username (blank to skip account creation)", ""); err != nil {
		return nil, err
	}
	if a.username != "" {
		if _, err := fmt.Fprintln(out, "Password (input hidden on terminals; visible when piped):"); err != nil {
			return nil, errs.Wrap(errs.CodeInternal, err, "cannot write password prompt")
		}
		if a.password, err = readSecret(in, reader, out); err != nil {
			return nil, err
		}
		var again string
		if _, err := fmt.Fprintln(out, "Repeat password:"); err != nil {
			return nil, errs.Wrap(errs.CodeInternal, err, "cannot write password prompt")
		}
		if again, err = readSecret(in, reader, out); err != nil {
			return nil, err
		}
		if a.password != again {
			return nil, errs.New(errs.CodeValidation, "passwords do not match")
		}
		a.createUser = true
	}
	answer, err := prompt(reader, out, "Web port", strconv.Itoa(config.Default().Web.Port))
	if err != nil {
		return nil, err
	}
	if a.webPort, err = strconv.Atoi(answer); err != nil {
		return nil, errs.New(errs.CodeValidation, "web port must be a number (got %q)", answer)
	}
	tr, err := i18n.New()
	if err != nil {
		return nil, errs.Wrap(errs.CodeInternal, err, "cannot load interface translations")
	}
	codes := make([]string, 0, 4)
	for _, l := range tr.Languages() {
		codes = append(codes, l.Code+" ("+l.Name+")")
	}
	langAnswer, err := prompt(reader, out, "Interface language ["+strings.Join(codes, ", ")+"]", tr.Default())
	if err != nil {
		return nil, err
	}
	a.language = i18n.Normalize(langAnswer)
	if !tr.Has(a.language) {
		return nil, errs.New(errs.CodeValidation,
			"unknown interface language %q (available: el, en, ru)", langAnswer)
	}
	demo, err := prompt(reader, out, "Seed synthetic demo data? [y/N]", "n")
	if err != nil {
		return nil, err
	}
	a.seedDemo = strings.HasPrefix(strings.ToLower(demo), "y")

	// Apply: same validated paths as the non-interactive commands.
	home, err := config.ResolveHome(a.home, getenv(config.EnvHome), getenv(config.EnvRefusedRoots))
	if err != nil {
		return nil, err
	}
	res, err := workspaceInitWith(home, a.webPort, a.language)
	if err != nil {
		return nil, err
	}
	result := map[string]any{"init": res}

	if a.createUser || a.seedDemo {
		svc, closeDB, err := openService(ctx, home, opts.logger)
		if err != nil {
			return nil, err
		}
		defer closeDB()
		if a.createUser {
			u, err := svc.CreateUser(ctx, "init-wizard", a.username, a.password, app.RoleAdmin)
			if err != nil {
				return nil, err
			}
			result["user"] = u
		}
		if a.seedDemo {
			seeded, err := seed.Demo(ctx, svc)
			if err != nil {
				return nil, err
			}
			result["seed"] = seeded
		}
	}
	return result, nil
}

// prompt prints "label [default]:" and reads one trimmed line.
func prompt(reader *bufio.Reader, out io.Writer, label, def string) (string, error) {
	var err error
	if def != "" {
		_, err = fmt.Fprintf(out, "%s [%s]: ", label, def)
	} else {
		_, err = fmt.Fprintf(out, "%s: ", label)
	}
	if err != nil {
		return "", errs.Wrap(errs.CodeInternal, err, "cannot write prompt for %q", label)
	}
	line, err := reader.ReadString('\n')
	if err != nil && line == "" {
		return "", errs.Wrap(errs.CodeValidation, err, "input ended while waiting for %q", label)
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return def, nil
	}
	return line, nil
}

// readSecret reads a password without echo when stdin is a terminal.
func readSecret(in io.Reader, reader *bufio.Reader, out io.Writer) (string, error) {
	if f, ok := in.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		b, err := term.ReadPassword(int(f.Fd()))
		if err != nil {
			return "", errs.Wrap(errs.CodeValidation, err, "cannot read password")
		}
		if _, err := fmt.Fprintln(out); err != nil {
			return "", errs.Wrap(errs.CodeInternal, err, "cannot write password newline")
		}
		return string(b), nil
	}
	line, err := reader.ReadString('\n')
	if err != nil && line == "" {
		return "", errs.Wrap(errs.CodeValidation, err, "input ended while reading password")
	}
	return strings.TrimRight(line, "\r\n"), nil
}
