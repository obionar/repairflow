package cli

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"repairflow/internal/config"
	"repairflow/internal/errs"
	"repairflow/internal/service"
)

// serviceReport is a service.Result plus the post-install operator hints. The
// hint fields are additive JSON members on `service install` only.
type serviceReport struct {
	*service.Result
	WebURL        string `json:"web_url,omitempty"`
	LogLocation   string `json:"log_location,omitempty"`
	StatusCommand string `json:"status_command,omitempty"`
}

// Service commands: unit install/status/remove for systemd user units
// and OpenRC service scripts. --init forces the target; otherwise it is detected.
func runService(ctx context.Context, opts options, args []string, getenv Env) (any, error) {
	if len(args) == 0 {
		return nil, errs.New(errs.CodeUsage, "usage: repairflow service <install|status|remove> ...")
	}
	sub, rest := args[0], args[1:]
	p, err := parseArgs(rest, []string{"init", "name"}, []string{"no-enable"})
	if err != nil {
		return nil, err
	}
	initSystem, err := service.ParseInit(p.get("init"))
	if err != nil {
		return nil, err
	}
	serviceName, err := service.ParseName(p.get("name"))
	if err != nil {
		return nil, err
	}
	so := service.Options{
		Init:    initSystem,
		Name:    serviceName,
		UnitDir: getenv("REPAIRFLOW_UNIT_DIR"), // test/deploy override; empty = default
		InitDir: getenv("REPAIRFLOW_INIT_DIR"), // test/deploy override; empty = /etc/init.d
	}
	if err := requirePositional(p, 0, "repairflow service "+sub+" [--init systemd|openrc|none] [--name NAME] [--no-enable]"); err != nil {
		return nil, err
	}
	switch sub {
	case "install":
		home, err := resolveHome(opts, getenv)
		if err != nil {
			return nil, err
		}
		// A unit for a workspace that does not exist installs cleanly and then
		// fails at start (seen on Alpine under doas: the default resolved to
		// /root/.repairflow). Refuse before touching the init system.
		if _, statErr := os.Stat(filepath.Join(home, "config.toml")); statErr != nil {
			return nil, errs.New(errs.CodeValidation,
				"no RepairFlow workspace at %s; run `repairflow init --home %s` first or pass the right --home", home, home)
		}
		binary, err := os.Executable()
		if err != nil {
			return nil, errs.Wrap(errs.CodeInternal, err, "cannot determine executable path")
		}
		so.BinaryPath = binary
		so.Home = home
		so.Enable = !p.flag("no-enable")
		result, err := service.Install(so)
		if err != nil {
			return nil, err
		}
		return installHints(home, effectiveInit(initSystem), serviceName, result), nil
	case "status":
		return service.Status(so)
	case "remove":
		return service.Remove(so)
	default:
		return nil, errs.New(errs.CodeUsage, "unknown service subcommand %q (supported: install, status, remove)", sub)
	}
}

// effectiveInit is the init system service.Install targets after detection.
func effectiveInit(initSystem service.Init) service.Init {
	if initSystem != "" {
		return initSystem
	}
	return service.DetectInit()
}

// installHints adds the URL, log and status hints an operator needs next. A
// missing or unreadable config only drops the URL hint: install already
// succeeded and must not fail for a presentation detail.
func installHints(home string, initSystem service.Init, name string, res *service.Result) *serviceReport {
	report := &serviceReport{Result: res}
	switch initSystem {
	case service.InitSystemd:
		report.LogLocation = "journalctl --user -u " + name + ".service"
		report.StatusCommand = "systemctl --user status " + name + ".service"
	case service.InitOpenRC:
		report.LogLocation = filepath.Join(home, "logs", "service.log")
		report.StatusCommand = "rc-service " + name + " status"
	}
	cfg, err := config.Load(filepath.Join(home, "config.toml"))
	if err != nil {
		return report
	}
	host := strings.Trim(cfg.Web.Host, "[]")
	switch host {
	case "", "0.0.0.0", "::":
		warning := fmt.Sprintf("web listens on all interfaces (web.host = %s)", cfg.Web.Host)
		if res.Warning == "" {
			res.Warning = warning
		} else {
			res.Warning += "; " + warning
		}
		host = "127.0.0.1"
	}
	report.WebURL = "http://" + net.JoinHostPort(host, strconv.Itoa(cfg.Web.Port)) + "/login"
	return report
}
