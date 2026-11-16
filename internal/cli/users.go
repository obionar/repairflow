package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"

	"repairflow/internal/app"
	"repairflow/internal/errs"

	"golang.org/x/term"
)

func runUser(ctx context.Context, stdin io.Reader, promptOut io.Writer, opts options, args []string, getenv Env) (any, error) {
	if len(args) == 0 {
		return nil, errs.New(errs.CodeUsage, "usage: "+usageUser)
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
		p, err := parseArgs(rest, []string{"role", "actor"}, []string{"password-stdin"})
		if err != nil {
			return nil, err
		}
		if err := requirePositional(p, 1, usageUserAdd); err != nil {
			return nil, err
		}
		password, err := passwordFrom(stdin, promptOut, p, p.positional[0], getenv)
		if err != nil {
			return nil, err
		}
		actor := resolveActor(p, getenv)
		return svc.CreateUser(ctx, actor, p.positional[0], password, p.get("role"))
	case "list":
		p, err := parseArgs(rest, nil, nil)
		if err != nil {
			return nil, err
		}
		if err := requirePositional(p, 0, usageUserList); err != nil {
			return nil, err
		}
		return svc.ListUsers(ctx)
	case "passwd":
		p, err := parseArgs(rest, []string{"actor"}, []string{"password-stdin"})
		if err != nil {
			return nil, err
		}
		if err := requirePositional(p, 1, usageUserPasswd); err != nil {
			return nil, err
		}
		password, err := passwordFrom(stdin, promptOut, p, p.positional[0], getenv)
		if err != nil {
			return nil, err
		}
		actor := resolveActor(p, getenv)
		if err := svc.ResetPassword(ctx, actor, app.RoleAdmin, p.positional[0], password); err != nil {
			return nil, err
		}
		return textResult{
			data: map[string]any{"username": p.positional[0], "password_changed": true, "sessions_revoked": true},
			text: fmt.Sprintf("password changed for %s; sessions revoked\n", p.positional[0]),
		}, nil
	case "enable", "disable":
		p, err := parseArgs(rest, []string{"actor"}, nil)
		if err != nil {
			return nil, err
		}
		usage := usageUserEnable
		if sub == "disable" {
			usage = usageUserDisable
		}
		if err := requirePositional(p, 1, usage); err != nil {
			return nil, err
		}
		actor := resolveActor(p, getenv)
		return svc.SetUserActive(ctx, actor, app.RoleAdmin, p.positional[0], sub == "enable")
	default:
		return nil, errs.New(errs.CodeUsage, "unknown user subcommand %q (supported: add, list, passwd, enable, disable)", sub)
	}
}

// passwordFrom resolves the new password: the explicit --password-stdin flag
// wins, then REPAIRFLOW_NEW_PASSWORD, then a double prompt on a terminal.
// Passwords never come from command-line arguments.
func passwordFrom(stdin io.Reader, promptOut io.Writer, p parsedArgs, username string, getenv Env) (string, error) {
	if p.flag("password-stdin") {
		line, err := bufio.NewReader(stdin).ReadString('\n')
		if err != nil && line == "" {
			return "", errs.New(errs.CodeUsage, "--password-stdin expects the password on one line of stdin")
		}
		if pw := trimPasswordLine(line); pw != "" {
			return validatePassword(pw)
		}
		return "", errs.New(errs.CodeUsage, "--password-stdin read an empty password")
	}
	if pw := getenv("REPAIRFLOW_NEW_PASSWORD"); pw != "" {
		return validatePassword(pw)
	}
	f, isFile := stdin.(*os.File)
	if !isFile || !term.IsTerminal(int(f.Fd())) {
		return "", errs.New(errs.CodeUsage,
			"password must come from --password-stdin, REPAIRFLOW_NEW_PASSWORD or an interactive prompt (never from command-line arguments)")
	}
	fd := int(f.Fd())
	first, err := readPassword(fd, promptOut, fmt.Sprintf("New password for %s: ", username))
	if err != nil {
		return "", err
	}
	again, err := readPassword(fd, promptOut, "Repeat password: ")
	if err != nil {
		return "", err
	}
	if first != again {
		return "", errs.New(errs.CodeValidation, "passwords do not match")
	}
	return validatePassword(first)
}

func readPassword(fd int, promptOut io.Writer, label string) (string, error) {
	if _, err := fmt.Fprint(promptOut, label); err != nil {
		return "", errs.Wrap(errs.CodeInternal, err, "cannot write password prompt")
	}
	b, err := term.ReadPassword(fd)
	if _, newlineErr := fmt.Fprintln(promptOut); newlineErr != nil {
		return "", errs.Wrap(errs.CodeInternal, newlineErr, "cannot write password newline")
	}
	if err != nil {
		return "", errs.Wrap(errs.CodeValidation, err, "cannot read password")
	}
	return string(b), nil
}

// trimPasswordLine removes the line ending only: spaces are legitimate.
func trimPasswordLine(line string) string {
	if n := len(line); n > 0 && line[n-1] == '\n' {
		line = line[:n-1]
	}
	if n := len(line); n > 0 && line[n-1] == '\r' {
		line = line[:n-1]
	}
	return line
}

// validatePassword applies the password policy early so the operator is not
// asked twice, or over the network, for a password that cannot be stored.
func validatePassword(pw string) (string, error) {
	if len(pw) < app.MinPasswordLength {
		return "", errs.New(errs.CodeValidation, "password must be at least %d characters", app.MinPasswordLength)
	}
	if len(pw) > 256 {
		return "", errs.New(errs.CodeValidation, "password must be at most 256 characters")
	}
	return pw, nil
}
