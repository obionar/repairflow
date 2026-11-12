// Package service integrates RepairFlow with the local init system
// Two targets are supported: **systemd user units** and **OpenRC
// service scripts** (Alpine). The target is auto-detected (systemctl on PATH,
// then /sbin/openrc-run) or forced with --init.
//
// Least privilege: the systemd unit runs the plain binary with NoNewPrivileges,
// ProtectSystem=strict and ReadWritePaths limited to the workspace; the OpenRC
// script runs as root only to hand over to the workspace owner via
// command_user. The package only writes files into the user's unit directory or
// the init script directory, and only removes files it can prove it created
// (managed-by marker). Enabling/disabling is best-effort: environments without
// a user session (containers, CI) or without privileges get the file plus
// explicit instructions.
//
// Options.Name selects the service instance: the default instance keeps the
// original file names and bare marker, a named instance writes
// <name>.service / <name> and records itself on the marker line, so one host can
// run more than one workspace. A unit marked for another instance is never
// overwritten or removed.
package service

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"repairflow/internal/errs"
)

// Init names the init system the installer targets.
type Init string

const (
	// InitSystemd writes a systemd user unit (default: ~/.config/systemd/user).
	InitSystemd Init = "systemd"
	// InitOpenRC writes an OpenRC service script (default: /etc/init.d/repairflow).
	InitOpenRC Init = "openrc"
	// InitNone writes nothing: the operator supervises the process itself.
	InitNone Init = "none"
)

// DefaultName is the service instance installed when Options.Name is empty.
const DefaultName = "repairflow"

// UnitName is the installed systemd user unit for the default instance.
const UnitName = DefaultName + ".service"

// OpenRCName is the OpenRC service name and script file name (no extension) for
// the default instance.
const OpenRCName = DefaultName

// namePattern is the service instance grammar: a lowercase letter followed by
// up to 30 lowercase letters, digits or dashes.
var namePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,30}$`)

// openrcRunPath is the interpreter every OpenRC service script starts with.
const openrcRunPath = "/sbin/openrc-run"

// marker identifies units written by this tool (removal safety).
const marker = "# managed-by=repairflow"

// ParseName validates a service instance name; empty selects DefaultName. The
// name becomes a file name in the unit or init directory, so the grammar is
// deliberately narrow.
func ParseName(value string) (string, error) {
	if value == "" {
		return DefaultName, nil
	}
	if !namePattern.MatchString(value) {
		return "", errs.New(errs.CodeUsage,
			"invalid service name %q (want a lowercase letter followed by up to 30 lowercase letters, digits or dashes)", value)
	}
	return value, nil
}

// ParseInit validates an --init value; empty means "detect".
func ParseInit(value string) (Init, error) {
	switch Init(strings.ToLower(strings.TrimSpace(value))) {
	case "":
		return "", nil
	case InitSystemd:
		return InitSystemd, nil
	case InitOpenRC:
		return InitOpenRC, nil
	case InitNone:
		return InitNone, nil
	default:
		return "", errs.New(errs.CodeUsage,
			"unknown init system %q (supported: systemd, openrc, none)", value)
	}
}

// DetectInit reports the init system this host runs: systemd when systemctl is
// on PATH, otherwise OpenRC when /sbin/openrc-run exists, otherwise none.
func DetectInit() Init {
	return detectInit(exec.LookPath, pathExists)
}

// detectInit is the testable form of DetectInit.
func detectInit(lookPath func(string) (string, error), exists func(string) bool) Init {
	if _, err := lookPath("systemctl"); err == nil {
		return InitSystemd
	}
	if exists(openrcRunPath) {
		return InitOpenRC
	}
	return InitNone
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// UnitDirDefault returns the systemd user unit directory.
func UnitDirDefault() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "systemd", "user")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".config", "systemd", "user")
	}
	return filepath.Join(home, ".config", "systemd", "user")
}

// InitDirDefault returns the OpenRC service script directory.
func InitDirDefault() string { return "/etc/init.d" }

// Options describes a service install/status/remove. An empty Init auto-detects
// and an empty Name selects the default instance (repairflow).
type Options struct {
	BinaryPath string
	Home       string
	Init       Init
	Name       string // service instance name; empty means DefaultName
	UnitDir    string // systemd user unit directory override
	InitDir    string // OpenRC script directory override
	User       string // OpenRC command_user override ("user" or "user:group")
	Enable     bool
}

// serviceName validates the requested instance name.
func (o Options) serviceName() (string, error) { return ParseName(o.Name) }

// systemdDir returns the systemd user unit directory for this operation.
func (o Options) systemdDir() string {
	if o.UnitDir != "" {
		return o.UnitDir
	}
	return UnitDirDefault()
}

// openRCDir returns the OpenRC script directory for this operation.
func (o Options) openRCDir() string {
	if o.InitDir != "" {
		return o.InitDir
	}
	return InitDirDefault()
}

// unitFileName is the systemd unit file name for a service instance.
func unitFileName(name string) string { return name + ".service" }

// Result reports a service operation.
type Result struct {
	UnitPath   string `json:"unit_path"`
	Present    bool   `json:"present"`
	Active     string `json:"active,omitempty"`
	Enabled    bool   `json:"enabled"`
	Warning    string `json:"warning,omitempty"`
	StagedPath string `json:"staged_path,omitempty"`
	Command    string `json:"command,omitempty"`
}

// Install writes the unit for the target init system (idempotent overwrite of
// our own unit) and best-effort enables + starts it. Without the privileges the
// target directory needs, the file is staged and the exact command to install it
// is returned instead of failing.
func Install(o Options) (*Result, error) {
	if strings.TrimSpace(o.BinaryPath) == "" || strings.TrimSpace(o.Home) == "" {
		return nil, errs.New(errs.CodeValidation, "binary path and workspace home are required")
	}
	name, err := o.serviceName()
	if err != nil {
		return nil, err
	}
	switch o.init() {
	case InitSystemd:
		return installSystemd(o, name)
	case InitOpenRC:
		return installOpenRC(o, name)
	default:
		return installNone(o)
	}
}

func (o Options) init() Init {
	if o.Init != "" {
		return o.Init
	}
	return DetectInit()
}

// installSystemd writes and enables the least-privilege systemd user unit.
func installSystemd(o Options, name string) (*Result, error) {
	binaryPath, home := o.BinaryPath, o.Home
	unitDir := o.systemdDir()
	unitFile := unitFileName(name)
	unitPath := filepath.Join(unitDir, unitFile)
	body := renderUnit(name, binaryPath, home)
	if err := ensureReplaceable(unitPath, name); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(unitDir, 0o755); err != nil {
		return nil, errs.Wrap(errs.CodeInternal, err, "cannot create unit directory")
	}
	if err := os.WriteFile(unitPath, []byte(body), 0o644); err != nil {
		return nil, errs.Wrap(errs.CodeInternal, err, "cannot write unit file")
	}

	res := &Result{UnitPath: unitPath, Present: true, Active: "unknown"}
	if warn := binaryPathWarning(binaryPath); warn != "" {
		res.Warning = warn
	}
	if o.Enable {
		if err := systemctl("daemon-reload"); err != nil {
			res.Warning = joinWarning(res.Warning, "systemctl unavailable: "+err.Error())
			return res, nil
		}
		if err := systemctl("enable", "--now", unitFile); err != nil {
			res.Warning = joinWarning(res.Warning, "enable failed: "+err.Error())
			return res, nil
		}
		res.Enabled = true
		res.Active = statusOf(unitFile)
	} else {
		res.Warning = joinWarning(res.Warning, "enable/start skipped (--no-enable)")
	}
	return res, nil
}

// installOpenRC writes the OpenRC service script. The directory is root-owned on
// a normal host, so an unprivileged run stages the script next to the workspace
// and reports the exact privileged commands instead of failing.
func installOpenRC(o Options, name string) (*Result, error) {
	dir := o.openRCDir()
	unitPath := filepath.Join(dir, name)
	user := o.User
	if user == "" {
		user = ownerUser(o.Home)
	}
	logPath := filepath.Join(o.Home, "logs", "service.log")
	body := renderOpenRC(name, o.BinaryPath, o.Home, user, logPath)

	res := &Result{UnitPath: unitPath, Active: "unknown"}
	if warn := binaryPathWarning(o.BinaryPath); warn != "" {
		res.Warning = joinWarning(res.Warning, warn)
	}
	if strings.ContainsAny(o.Home, " \t") {
		res.Warning = joinWarning(res.Warning,
			"workspace path contains whitespace; an OpenRC script splits command_args there, so use a path without spaces")
	}

	if err := ensureReplaceable(unitPath, name); err != nil {
		return nil, err
	}
	err := os.MkdirAll(dir, 0o755)
	if err == nil {
		err = os.WriteFile(unitPath, []byte(body), 0o755)
	}
	if err == nil {
		res.Present = true
		res.Enabled, res.Active = enableOpenRC(o.Enable, res, name)
		return res, nil
	}
	if !needsPrivileges(err) {
		return nil, errs.Wrap(errs.CodeInternal, err, "cannot write OpenRC service script")
	}
	return stageOpenRC(o, name, body, res)
}

// stageOpenRC stores the generated script where its owner can read it and
// reports the exact privileged commands to install, enable and start it.
func stageOpenRC(o Options, name, body string, res *Result) (*Result, error) {
	staged := filepath.Join(o.Home, "cache", "service", name+".initd")
	if err := os.MkdirAll(filepath.Dir(staged), 0o755); err != nil {
		return nil, errs.Wrap(errs.CodeInternal, err, "cannot create staging directory")
	}
	if err := os.WriteFile(staged, []byte(body), 0o755); err != nil {
		return nil, errs.Wrap(errs.CodeInternal, err, "cannot stage OpenRC service script")
	}
	priv := privilegeCommand()
	cmds := []string{priv + " install -m 0755 " + shellQuote(staged) + " " + shellQuote(res.UnitPath)}
	if o.Enable {
		cmds = append(cmds,
			priv+" rc-update add "+name+" default",
			priv+" rc-service "+name+" start")
	}
	res.Present = false
	res.StagedPath = staged
	res.Command = strings.Join(cmds, " && ")
	res.Warning = joinWarning(res.Warning,
		"no privileges to write "+res.UnitPath+"; the script was staged and must be installed with the command below")
	return res, nil
}

// enableOpenRC registers the service in the default runlevel and starts it.
// Reinstalling restarts, so a replaced script takes effect.
func enableOpenRC(enable bool, res *Result, name string) (bool, string) {
	if !enable {
		res.Warning = joinWarning(res.Warning, "enable/start skipped (--no-enable)")
		active, _ := openrcStatus(name)
		return false, active
	}
	if err := openrcCommand("rc-update", "add", name, "default"); err != nil {
		res.Warning = joinWarning(res.Warning, "rc-update add failed: "+err.Error())
		return false, "unknown"
	}
	if err := openrcCommand("rc-service", name, "restart"); err != nil {
		if serr := openrcCommand("rc-service", name, "start"); serr != nil {
			res.Warning = joinWarning(res.Warning, "start failed: "+serr.Error())
			return true, "unknown"
		}
	}
	active, detail := openrcStatus(name)
	if active != "started" {
		msg := "service did not reach the started state: " + active
		if detail != "" {
			msg += " (" + detail + ")"
		}
		res.Warning = joinWarning(res.Warning, msg)
	}
	return true, active
}

// installNone reports how to run the binary under an operator-chosen supervisor.
func installNone(o Options) (*Result, error) {
	return &Result{
		Present: false,
		Active:  "unknown",
		Warning: "no supported init system detected (systemctl on PATH, /sbin/openrc-run); " +
			"pass --init systemd|openrc or supervise the process yourself",
		Command: shellQuote(o.BinaryPath) + " serve --home " + shellQuote(o.Home),
	}, nil
}

// Status reports the unit for the target init system.
func Status(o Options) (*Result, error) {
	name, err := o.serviceName()
	if err != nil {
		return nil, err
	}
	switch o.init() {
	case InitSystemd:
		unitFile := unitFileName(name)
		unitPath := filepath.Join(o.systemdDir(), unitFile)
		res := &Result{UnitPath: unitPath, Active: "unknown"}
		if _, err := os.Stat(unitPath); err == nil {
			res.Present = true
			res.Active = statusOf(unitFile)
		} else if !os.IsNotExist(err) {
			return nil, errs.Wrap(errs.CodeInternal, err, "cannot inspect unit file")
		}
		return res, nil
	case InitOpenRC:
		unitPath := filepath.Join(o.openRCDir(), name)
		res := &Result{UnitPath: unitPath, Active: "unknown"}
		if _, err := os.Stat(unitPath); err == nil {
			res.Present = true
			active, detail := openrcStatus(name)
			res.Active = active
			res.Enabled = openrcEnabled(name)
			if active == "unknown" && detail != "" {
				res.Warning = joinWarning(res.Warning, "rc-service status unavailable: "+detail)
			}
		} else if !os.IsNotExist(err) {
			return nil, errs.Wrap(errs.CodeInternal, err, "cannot inspect OpenRC service script")
		}
		return res, nil
	default:
		return &Result{Active: "unknown",
			Warning: "no supported init system detected; nothing is managed"}, nil
	}
}

// Remove disables the unit (best-effort) and deletes the unit file. Files we
// cannot prove we created (missing marker) are refused.
func Remove(o Options) (*Result, error) {
	name, err := o.serviceName()
	if err != nil {
		return nil, err
	}
	switch o.init() {
	case InitSystemd:
		return removeSystemd(o, name)
	case InitOpenRC:
		return removeOpenRC(o, name)
	default:
		return &Result{Present: false, Active: "unknown",
			Warning: "no supported init system detected; nothing to remove"}, nil
	}
}

func removeSystemd(o Options, name string) (*Result, error) {
	unitFile := unitFileName(name)
	unitPath := filepath.Join(o.systemdDir(), unitFile)
	body, err := os.ReadFile(unitPath)
	if err != nil {
		if os.IsNotExist(err) {
			return &Result{UnitPath: unitPath, Present: false, Warning: "unit not installed"}, nil
		}
		return nil, errs.Wrap(errs.CodeInternal, err, "cannot read unit file")
	}
	if owner, managed := managedService(string(body)); !managed {
		return nil, errs.New(errs.CodeConflict,
			"refusing to remove %s: it was not written by repairflow (managed-by marker missing)", unitPath)
	} else if owner != name {
		return nil, ownershipConflict("remove", unitPath, owner, name)
	}
	if err := systemctl("disable", "--now", unitFile); err == nil {
		defer func() { _ = systemctl("daemon-reload") }()
	}
	if err := os.Remove(unitPath); err != nil {
		return nil, errs.Wrap(errs.CodeInternal, err, "cannot remove unit file")
	}
	return &Result{UnitPath: unitPath, Present: false, Active: "unknown"}, nil
}

func removeOpenRC(o Options, name string) (*Result, error) {
	unitPath := filepath.Join(o.openRCDir(), name)
	body, err := os.ReadFile(unitPath)
	if err != nil {
		if os.IsNotExist(err) {
			return &Result{UnitPath: unitPath, Present: false, Warning: "service script not installed"}, nil
		}
		return nil, errs.Wrap(errs.CodeInternal, err, "cannot read OpenRC service script")
	}
	if owner, managed := managedService(string(body)); !managed {
		return nil, errs.New(errs.CodeConflict,
			"refusing to remove %s: it was not written by repairflow (managed-by marker missing)", unitPath)
	} else if owner != name {
		return nil, ownershipConflict("remove", unitPath, owner, name)
	}
	_ = openrcCommand("rc-service", name, "stop")
	_ = openrcCommand("rc-update", "del", name, "default")
	if err := os.Remove(unitPath); err != nil {
		if needsPrivileges(err) {
			return nil, errs.New(errs.CodePermission,
				"cannot remove %s without privileges; run: %s rc-service %s stop && %s rc-update del %s default && %s rm %s",
				unitPath, privilegeCommand(), name, privilegeCommand(), name,
				privilegeCommand(), shellQuote(unitPath))
		}
		return nil, errs.Wrap(errs.CodeInternal, err, "cannot remove OpenRC service script")
	}
	return &Result{UnitPath: unitPath, Present: false, Active: "unknown"}, nil
}

// renderUnit builds the least-privilege systemd user unit. The default instance
// keeps its original Description and marker; a named instance identifies itself
// in both.
func renderUnit(name, binaryPath, home string) string {
	var b strings.Builder
	b.WriteString(markerFor(name) + "\n")
	b.WriteString("[Unit]\nDescription=" + unitDescription(name) + "\nAfter=default.target\n\n")
	b.WriteString("[Service]\nType=simple\n")
	b.WriteString("ExecStart=" + quote(binaryPath) + " serve --home " + quote(home) + "\n")
	b.WriteString("WorkingDirectory=" + quote(home) + "\n")
	b.WriteString("Restart=on-failure\nRestartSec=5\n")
	b.WriteString("NoNewPrivileges=true\nProtectSystem=strict\nPrivateTmp=true\n")
	b.WriteString("ReadWritePaths=" + quote(home) + "\n\n")
	b.WriteString("[Install]\nWantedBy=default.target\n")
	return b.String()
}

// renderOpenRC builds the Alpine/OpenRC service script. command_user is the
// workspace owner (the script starts as root and drops to that account); the log
// stays inside the workspace so it follows the same ownership and retention.
func renderOpenRC(name, binaryPath, home, user, logPath string) string {
	var b strings.Builder
	b.WriteString("#!/sbin/openrc-run\n")
	b.WriteString(markerFor(name) + "\n")
	b.WriteString("# Written by `repairflow service install --init openrc`; re-run that instead of editing.\n\n")
	b.WriteString("name=" + openrcValue(name) + "\n")
	b.WriteString("description=" + openrcValue("RepairFlow repair-shop web interface") + "\n\n")
	b.WriteString("command=" + openrcValue(binaryPath) + "\n")
	b.WriteString("command_args=" + openrcValue("serve --home "+home) + "\n")
	if user != "" {
		b.WriteString("command_user=" + openrcValue(user) + "\n")
	}
	b.WriteString("command_background=\"yes\"\n")
	b.WriteString("pidfile=\"/run/${RC_SVCNAME}.pid\"\n")
	b.WriteString("output_log=" + openrcValue(logPath) + "\n")
	b.WriteString("error_log=" + openrcValue(logPath) + "\n\n")
	b.WriteString("depend() {\n\tneed net\n\tafter firewall\n}\n\n")
	b.WriteString("start_pre() {\n\tcheckpath --file")
	if user != "" {
		b.WriteString(" --owner " + openrcValue(user))
	}
	b.WriteString(" --mode 0600 \"${output_log}\"\n}\n")
	return b.String()
}

// managedService reports which service instance wrote a unit body. The bare
// marker is the default instance; a non-default instance appends
// `service=<name>`. The bool is false when the body carries no marker at all;
// a marker with an unreadable name is reported as managed with an empty name.
func managedService(body string) (string, bool) {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimRight(line, "\r")
		rest, ok := strings.CutPrefix(line, marker)
		if !ok {
			continue
		}
		rest = strings.TrimSpace(rest)
		if rest == "" {
			return DefaultName, true
		}
		if value, ok := strings.CutPrefix(rest, "service="); ok {
			if name := strings.TrimSpace(value); namePattern.MatchString(name) {
				return name, true
			}
		}
		return "", true
	}
	return "", false
}

// ensureReplaceable refuses to overwrite an existing file that a different
// service instance wrote. An unmarked file keeps the previous behaviour: a
// hand-written unit is replaced and Remove refuses it separately.
func ensureReplaceable(path, name string) error {
	body, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return errs.New(errs.CodeConflict,
			"refusing to overwrite %s: its managed-by marker cannot be read (%v)", path, err)
	}
	owner, managed := managedService(string(body))
	if !managed || owner == name {
		return nil
	}
	return ownershipConflict("overwrite", path, owner, name)
}

// ownershipConflict is the single refusal for a unit another instance owns.
func ownershipConflict(action, path, owner, name string) error {
	if owner == "" {
		return errs.New(errs.CodeConflict,
			"refusing to %s %s: its managed-by marker names no readable repairflow service, it does not belong to %q",
			action, path, name)
	}
	return errs.New(errs.CodeConflict,
		"refusing to %s %s: it belongs to repairflow service %q, not %q", action, path, owner, name)
}

// markerFor returns the managed-by line for a service instance. The default
// instance keeps the original bare marker (installed units are compared); other
// instances add a service field.
func markerFor(name string) string {
	if name == DefaultName {
		return marker
	}
	return marker + " service=" + name
}

// unitDescription names the instance in the unit; the default instance keeps the
// original text.
func unitDescription(name string) string {
	if name == DefaultName {
		return "RepairFlow local repair workspace"
	}
	return "RepairFlow " + name + " local repair workspace"
}

// quote applies systemd-style quoting for paths.
func quote(value string) string {
	if strings.ContainsAny(value, " \t\"'\\") {
		return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(value) + `"`
	}
	return value
}

// openrcValue quotes a shell assignment value for an OpenRC script.
func openrcValue(value string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "$", `\$`, "`", "\\`").Replace(value) + `"`
}

// shellQuote quotes a value for a POSIX shell command line.
func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune("/._-:@%+=,", r):
		default:
			return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
		}
	}
	return value
}

// needsPrivileges reports whether a filesystem error is only about permissions.
func needsPrivileges(err error) bool {
	return errors.Is(err, fs.ErrPermission) || errors.Is(err, syscall.EROFS) ||
		errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES)
}

// privilegeCommand names the escalation tool available on this host.
func privilegeCommand() string {
	if _, err := exec.LookPath("doas"); err == nil {
		return "doas"
	}
	return "sudo"
}

// systemctl runs a best-effort user-level systemctl command.
func systemctl(args ...string) error {
	cmd := exec.Command("systemctl", append([]string{"--user"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return &systemctlError{err: err, output: strings.TrimSpace(string(out))}
	}
	return nil
}

// openrcCommand runs a best-effort OpenRC command.
func openrcCommand(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return &systemctlError{err: err, output: strings.TrimSpace(string(out))}
	}
	return nil
}

func statusOf(unit string) string {
	cmd := exec.Command("systemctl", "--user", "is-active", unit)
	out, err := cmd.CombinedOutput()
	state := strings.TrimSpace(string(out))
	if err != nil && state == "" {
		return "unknown"
	}
	return state
}

// openrcStates are the words `rc-service <name> status` reports.
var openrcStates = map[string]bool{
	"started": true, "stopped": true, "crashed": true, "starting": true,
	"stopping": true, "inactive": true, "scheduled": true,
}

// openrcStatus reads `rc-service <name> status`. The second value is the raw
// output, used to explain an unknown state (for example a script the caller
// may not execute).
func openrcStatus(name string) (string, string) {
	out, _ := exec.Command("rc-service", name, "status").CombinedOutput()
	text := strings.TrimSpace(string(out))
	if idx := strings.LastIndex(text, ":"); idx >= 0 {
		if state := strings.TrimSpace(text[idx+1:]); openrcStates[state] {
			return state, ""
		}
	}
	return "unknown", text
}

// openrcEnabled reports whether the service is registered in the default runlevel.
func openrcEnabled(name string) bool {
	out, err := exec.Command("rc-update", "show", "default").CombinedOutput()
	if err != nil {
		return false
	}
	for _, field := range strings.Fields(string(out)) {
		if field == name {
			return true
		}
	}
	return false
}

// binaryPathWarning flags a unit that pins a binary the operator is likely to
// move or delete. The unit stores an absolute ExecStart path, so a binary run
// from a temporary or download folder breaks the service once that folder is
// cleaned up.
func binaryPathWarning(binaryPath string) string {
	clean := filepath.Clean(binaryPath)
	if !filepath.IsAbs(clean) {
		return "unit pins a non-absolute binary path " + binaryPath + "; move the binary to a stable location first"
	}
	temp := filepath.Clean(os.TempDir())
	if clean == temp || strings.HasPrefix(clean, temp+string(os.PathSeparator)) {
		return "unit pins " + clean + " inside a temporary directory; move the binary to a stable path before installing"
	}
	for _, segment := range strings.Split(clean, string(os.PathSeparator)) {
		switch segment {
		case "Downloads", "downloads", "Desktop":
			return "unit pins " + clean + " inside a download/desktop folder; move the binary to a stable path such as ~/.local/bin/repairflow"
		}
	}
	return ""
}

// joinWarning appends a second warning without dropping the first.
func joinWarning(existing, next string) string {
	if existing == "" {
		return next
	}
	return existing + "; " + next
}

type systemctlError struct {
	err    error
	output string
}

func (e *systemctlError) Error() string {
	if e.output != "" {
		return e.output
	}
	return e.err.Error()
}
