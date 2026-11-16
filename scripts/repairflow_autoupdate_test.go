// Test-only package: regression coverage for scripts/repairflow-autoupdate.
//
// The unattended updater must fail closed: it stops the intended service before
// the binary swap, never renames or restores the live workspace, never deletes
// operator backups, and only reports success when the restarted candidate
// answers on the configured endpoint with the expected version and commit.
//
// Every command the script can reach is a synthetic fake on a private PATH;
// no test touches systemctl, OpenRC, curl, sudo/doas or real workspace data.
package scripts

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

const fakeRepairflow = `#!/bin/sh
ver=$(cat "$STATE/installed_version" 2>/dev/null) || ver=old
printf 'repairflow|%s|%s\n' "$*" "$ver" >>"$CALLS"
case " $* " in
  *" version --json "*)
    e=$(cat "$STATE/expected_version" 2>/dev/null) || e=
    c=$(cat "$STATE/expected_commit" 2>/dev/null) || c=
    printf '{"ok":true,"command":"version","contract":2,"data":{"version":"%s","commit":"%s"}}\n' "${e:-0.1.0}" "${c:-oldcommit}"
    exit 0;;
  *" config check "*)
    [ -f "$STATE/config.json" ] || exit 2
    cat "$STATE/config.json"
    exit 0;;
  *" backup create "*)
    if [ -f "$STATE/slow_backup" ]; then
      : >"$STATE/slow_backup_started"
      sleep 2
    fi
    out=
    prev=
    for a in "$@"; do
      if [ "$prev" = "--output" ]; then out=$a; fi
      prev=$a
    done
    [ -n "$out" ] || exit 2
    printf 'backup %s\n' "$ver" >"$out"
    exit 0;;
  *" backup verify "*)
    last=
    for a in "$@"; do last=$a; done
    [ -f "$last" ] || exit 3
    exit 0;;
  *" customer list "*)
    n=$(cat "$STATE/customers" 2>/dev/null) || n=3
    if [ "$ver" = 9.9.9 ] && [ -f "$STATE/customers_after" ]; then n=$(cat "$STATE/customers_after"); fi
    printf '{"ok":true,"command":"customer list","contract":2,"data":{"total":%s}}\n' "$n"
    exit 0;;
  *" doctor "*)
    [ -f "$STATE/doctor_fail" ] && exit 5
    exit 0;;
  *" status "*)
    exit 0;;
esac
exit 0
`

const fakeUpdate = `#!/bin/sh
printf 'update|%s|%s\n' "$*" "$(cat "$STATE/installed_version" 2>/dev/null)" >>"$CALLS"
apply=0
for a in "$@"; do
  if [ "$a" = "--apply" ]; then apply=1; fi
done
if [ "$apply" = 0 ]; then
  if [ -f "$STATE/uptodate" ]; then
    echo "repairflow is up to date"
    exit 0
  fi
  echo "would apply $1"
  exit 0
fi
if [ -f "$STATE/apply_fail" ]; then
  echo "apply failed" >&2
  exit 1
fi
cp "$STATE/expected_version" "$STATE/installed_version"
cp "$STATE/expected_commit" "$STATE/installed_commit"
echo "applied $1"
exit 0
`

const fakeCurl = `#!/bin/sh
printf 'curl|%s\n' "$*" >>"$CALLS"
case " $* " in
  *" -fsSI "*)
    latest=$(cat "$STATE/latest" 2>/dev/null) || latest=
    [ -n "$latest" ] || exit 22
    printf 'HTTP/2.0 302 Found\r\nLocation: https://github.com/obionar/repairflow/releases/tag/%s\r\n\r\n' "$latest"
    exit 0;;
esac
[ -f "$STATE/listening" ] || exit 7
[ -f "$STATE/curl_fail" ] && exit 7
url=
for a in "$@"; do
  case $a in
    http://*|https://*) url=$a;;
  esac
done
case $url in
  */about)
    if [ -f "$STATE/about.html" ]; then
      cat "$STATE/about.html"
    else
      printf '<p>version %s commit %s</p>\n' "$(cat "$STATE/expected_version" 2>/dev/null)" "$(cat "$STATE/expected_commit" 2>/dev/null)"
    fi
    exit 0;;
  */login)
    echo '<html>login</html>'
    exit 0;;
esac
exit 22
`

const fakeSystemctl = `#!/bin/sh
printf 'systemctl|%s|%s\n' "$*" "$(cat "$STATE/installed_version" 2>/dev/null)" >>"$CALLS"
verb=$2
case $verb in
  stop)
    [ -f "$STATE/stop_fail" ] && exit 1
    [ -f "$STATE/stubborn_stop" ] || rm -f "$STATE/listening"
    exit 0;;
  start)
    [ -f "$STATE/start_fail" ] && exit 1
    : >"$STATE/listening"
    exit 0;;
  is-active)
    [ -f "$STATE/status_unknown" ] && { echo unknown; exit 4; }
    if [ -f "$STATE/listening" ]; then echo active; exit 0; fi
    echo inactive
    exit 3;;
esac
exit 0
`

const fakeRCService = `#!/bin/sh
printf 'rc-service|%s|%s\n' "$*" "$(cat "$STATE/installed_version" 2>/dev/null)" >>"$CALLS"
verb=$2
case $verb in
  stop)
    [ -f "$STATE/stop_fail" ] && exit 1
    [ -f "$STATE/stubborn_stop" ] || rm -f "$STATE/listening"
    exit 0;;
  start)
    [ -f "$STATE/start_fail" ] && exit 1
    : >"$STATE/listening"
    exit 0;;
  status)
    [ -f "$STATE/status_unknown" ] && { echo "status: crashed"; exit 4; }
    if [ -f "$STATE/listening" ]; then echo "status: started"; exit 0; fi
    echo "status: stopped"
    exit 3;;
esac
exit 0
`

const fakePrivilege = `#!/bin/sh
printf 'priv|%s|%s\n' "$(basename "$0")" "$*" >>"$CALLS"
[ "$1" = "-n" ] && shift
exec "$@"
`

type autoHarness struct {
	t       *testing.T
	root    string
	home    string
	ws      string
	tmp     string
	state   string
	fakebin string
	calls   string
	bin     string
	initd   string
	script  string
}

func newAutoHarness(t *testing.T) *autoHarness {
	t.Helper()
	root := t.TempDir()
	h := &autoHarness{
		t:       t,
		root:    root,
		home:    filepath.Join(root, "home dir"), // spaces must survive quoting
		tmp:     filepath.Join(root, "tmp"),
		state:   filepath.Join(root, "state"),
		fakebin: filepath.Join(root, "fakebin"),
		calls:   filepath.Join(root, "calls.log"),
		bin:     filepath.Join(root, "bin", "repairflow"),
		initd:   filepath.Join(root, "init.d"),
		script:  filepath.Join(root, "bin", "repairflow-autoupdate"),
	}
	h.ws = filepath.Join(h.home, ".repairflow")
	for _, dir := range []string{h.home, h.tmp, h.state, h.fakebin, filepath.Dir(h.bin), h.initd, h.ws} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	h.linkTools("awk", "basename", "cat", "cp", "cut", "date", "df", "dirname", "du",
		"flock", "head", "id", "mkdir", "mktemp", "rm", "sed", "sleep", "tail", "tr", "xargs")
	h.writeTool("repairflow-update", fakeUpdate)
	h.writeTool("curl", fakeCurl)
	h.writeFile(h.bin, fakeRepairflow, 0o755)
	h.writeFile(h.bin+".prev", "previous binary\n", 0o755)
	h.writeFile(filepath.Join(h.ws, "workspace-marker"), "original workspace\n", 0o600)
	src, err := os.ReadFile("repairflow-autoupdate")
	if err != nil {
		t.Fatalf("read scripts/repairflow-autoupdate: %v", err)
	}
	h.writeFile(h.script, string(src), 0o755)
	return h
}

func (h *autoHarness) linkTools(names ...string) {
	h.t.Helper()
	for _, name := range names {
		real, err := exec.LookPath(name)
		if err != nil {
			h.t.Fatalf("%s not found: %v", name, err)
		}
		if err := os.Symlink(real, filepath.Join(h.fakebin, name)); err != nil {
			h.t.Fatal(err)
		}
	}
}

func (h *autoHarness) writeTool(name, body string) {
	h.t.Helper()
	h.writeFile(filepath.Join(h.fakebin, name), body, 0o755)
}

func (h *autoHarness) writeFile(path, body string, mode os.FileMode) {
	h.t.Helper()
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		h.t.Fatal(err)
	}
}

func (h *autoHarness) set(name, body string) {
	h.t.Helper()
	h.writeFile(filepath.Join(h.state, name), body, 0o644)
}

func configJSON(host string, port int) string {
	return fmt.Sprintf(`{"ok":true,"command":"config check","contract":2,"data":{"home":"/ws","config_path":"/ws/config.toml","initialized":true,"config":{"ConfigVersion":1,"App":{"Environment":"development"},"Web":{"Host":%q,"Port":%d,"AuthRequired":true,"AllowRemote":false,"AllowInsecureLAN":false},"UI":{"Language":"en"}}}}`, host, port)
}

// setupUpdate prepares a workspace that is one release behind v9.9.9.
func (h *autoHarness) setupUpdate() {
	h.set("latest", "v9.9.9")
	h.set("installed_version", "0.1.0")
	h.set("installed_commit", "oldcommit")
	h.set("expected_version", "9.9.9")
	h.set("expected_commit", "newcommit")
	h.set("customers", "3")
	h.set("config.json", configJSON("127.0.0.1", 8091))
	h.set("listening", "1") // the old service is already serving
}

func (h *autoHarness) setupOpenRC() {
	h.writeTool("rc-service", fakeRCService)
	h.writeTool("doas", fakePrivilege)
	unit := "#!/sbin/openrc-run\ncommand=\"" + h.bin + "\"\ncommand_args=\"serve --home " + h.ws + "\"\n"
	h.writeFile(filepath.Join(h.initd, "repairflow"), unit, 0o755)
}

func (h *autoHarness) setupSystemd() {
	h.writeTool("systemctl", fakeSystemctl)
	unit := "[Unit]\nDescription=RepairFlow\n\n[Service]\nType=simple\n" +
		"ExecStart=\"" + h.bin + "\" serve --home \"" + h.ws + "\"\n" +
		"WorkingDirectory=\"" + h.ws + "\"\n\n[Install]\nWantedBy=default.target\n"
	dir := filepath.Join(h.home, ".config", "systemd", "user")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		h.t.Fatal(err)
	}
	h.writeFile(filepath.Join(dir, "repairflow.service"), unit, 0o644)
}

func (h *autoHarness) env() []string {
	return []string{
		"PATH=" + h.fakebin,
		"HOME=" + h.home,
		"TMPDIR=" + h.tmp,
		"REPAIRFLOW_HOME=" + h.ws,
		"REPAIRFLOW_BIN=" + h.bin,
		"REPAIRFLOW_INITD=" + h.initd,
		"RF_UPDATE_TRIES=1",
		"CALLS=" + h.calls,
		"STATE=" + h.state,
	}
}

func (h *autoHarness) command() *exec.Cmd {
	cmd := exec.Command("/bin/sh", h.script)
	cmd.Dir = h.tmp
	cmd.Env = h.env()
	return cmd
}

func (h *autoHarness) run() (string, error) {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", h.script)
	cmd.Dir = h.tmp
	cmd.Env = h.env()
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (h *autoHarness) callsLog() string {
	body, _ := os.ReadFile(h.calls)
	return string(body)
}

// logLines are the updater's own messages, not the fake command trace.
func (h *autoHarness) messageLog() string {
	body, _ := os.ReadFile(filepath.Join(h.ws, "logs", "update.log"))
	return string(body)
}

func (h *autoHarness) has(sub string) bool {
	return strings.Contains(h.callsLog(), sub)
}

// lineIndex returns the first call-log line containing every part, or -1.
func (h *autoHarness) lineIndex(parts ...string) int {
	for i, line := range strings.Split(h.callsLog(), "\n") {
		if line == "" {
			continue
		}
		ok := true
		for _, part := range parts {
			if !strings.Contains(line, part) {
				ok = false
				break
			}
		}
		if ok {
			return i
		}
	}
	return -1
}

func (h *autoHarness) requireInOrder(t *testing.T, steps ...[]string) {
	t.Helper()
	prev := -1
	for _, step := range steps {
		idx := h.lineIndex(step...)
		if idx < 0 {
			t.Fatalf("expected call %v, got:\n%s", step, h.callsLog())
		}
		if idx <= prev {
			t.Fatalf("call %v out of order:\n%s", step, h.callsLog())
		}
		prev = idx
	}
}

// requireNoMutation proves the updated binary never touched an unapplied candidate.
func (h *autoHarness) requireNoMutation(t *testing.T) {
	t.Helper()
	if h.has("--apply") {
		t.Fatalf("candidate was applied unexpectedly:\n%s", h.callsLog())
	}
	if h.has("backup create") {
		t.Fatalf("a backup was taken unexpectedly:\n%s", h.callsLog())
	}
}

// requirePreserved proves the fail-closed promise for a post-stop failure.
func (h *autoHarness) requirePreserved(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(h.bin + ".prev"); err != nil {
		t.Fatalf("previous binary was not preserved: %v", err)
	}
	backups, _ := filepath.Glob(filepath.Join(h.home, "backups", "pre-v9.9.9-*", "repairflow.rfbak"))
	if len(backups) != 1 {
		t.Fatalf("verified pre-update backup was not preserved: %v", backups)
	}
	if _, err := os.Stat(filepath.Join(h.ws, "workspace-marker")); err != nil {
		t.Fatalf("live workspace was moved or replaced: %v", err)
	}
	if moved, _ := filepath.Glob(h.ws + ".broken-*"); len(moved) != 0 {
		t.Fatalf("live workspace was renamed: %v", moved)
	}
	if _, err := os.Stat(filepath.Join(h.ws, "cache", "update", "failed-v9.9.9")); err != nil {
		t.Fatalf("failed-tag marker missing: %v", err)
	}
	if strings.Contains(h.messageLog(), "rolled back") || strings.Contains(h.messageLog(), "ok: v9.9.9") {
		t.Fatalf("failure reported as rollback or success:\n%s", h.messageLog())
	}
	if !strings.Contains(h.messageLog(), "MANUAL RECOVERY") {
		t.Fatalf("no manual recovery message:\n%s", h.messageLog())
	}
}

func TestAutoupdateOpenRCStopsBeforeBackupAndApply(t *testing.T) {
	h := newAutoHarness(t)
	h.setupUpdate()
	h.setupOpenRC()

	out, err := h.run()
	if err != nil {
		t.Fatalf("unexpected failure: %v\n%s\n%s", err, out, h.callsLog())
	}
	h.requireInOrder(t,
		[]string{"rc-service", "repairflow stop", "|0.1.0"},
		[]string{"repairflow", "backup create", "|0.1.0"},
		[]string{"update", "--apply", "|0.1.0"},
		[]string{"rc-service", "repairflow start", "|9.9.9"},
		[]string{"repairflow", "customer list", "|9.9.9"},
	)
	if h.has("systemctl") {
		t.Fatalf("OpenRC host must not consult systemd:\n%s", h.callsLog())
	}
	if !strings.Contains(h.messageLog(), "ok: v9.9.9") {
		t.Fatalf("success was not logged:\n%s\n%s", out, h.messageLog())
	}
}

func TestAutoupdateRefusesWhenStopFails(t *testing.T) {
	h := newAutoHarness(t)
	h.setupUpdate()
	h.setupOpenRC()
	h.set("stop_fail", "1")

	out, err := h.run()
	if err == nil {
		t.Fatalf("failed stop reported success:\n%s\n%s", out, h.callsLog())
	}
	h.requireNoMutation(t)
	if h.has("repairflow start") {
		t.Fatalf("candidate was started after a failed stop:\n%s", h.callsLog())
	}
}

func TestAutoupdateRefusesWhenServiceStatusUnknown(t *testing.T) {
	h := newAutoHarness(t)
	h.setupUpdate()
	h.setupOpenRC()
	h.set("status_unknown", "1")

	out, err := h.run()
	if err == nil {
		t.Fatalf("unknown service state reported success:\n%s\n%s", out, h.callsLog())
	}
	h.requireNoMutation(t)
}

func TestAutoupdateRefusesWhenEndpointStillListens(t *testing.T) {
	h := newAutoHarness(t)
	h.setupUpdate()
	h.setupOpenRC()
	h.set("stubborn_stop", "1")

	out, err := h.run()
	if err == nil {
		t.Fatalf("a still-listening endpoint was ignored:\n%s\n%s", out, h.callsLog())
	}
	h.requireNoMutation(t)
	if h.has("repairflow start") {
		t.Fatalf("a foreign listener was restarted or touched:\n%s", h.callsLog())
	}
}

func TestAutoupdateRefusesUnitWorkspaceMismatch(t *testing.T) {
	for _, tc := range []struct {
		name string
		unit func(*autoHarness) string
	}{
		{
			name: "other-workspace",
			unit: func(h *autoHarness) string {
				return "#!/sbin/openrc-run\ncommand=\"" + h.bin + "\"\ncommand_args=\"serve --home /srv/other\"\n"
			},
		},
		{
			name: "other-binary",
			unit: func(h *autoHarness) string {
				return "#!/sbin/openrc-run\ncommand=\"/srv/other/repairflow\"\ncommand_args=\"serve --home " + h.ws + "\"\n"
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newAutoHarness(t)
			h.setupUpdate()
			h.setupOpenRC()
			h.writeFile(filepath.Join(h.initd, "repairflow"), tc.unit(h), 0o755)

			out, err := h.run()
			if err != nil {
				t.Fatalf("a refused layout must not fail the job: %v\n%s\n%s", err, out, h.callsLog())
			}
			h.requireNoMutation(t)
			if h.has("repairflow stop") {
				t.Fatalf("unrelated service was stopped:\n%s", h.callsLog())
			}
			if !strings.Contains(h.messageLog(), "refuse:") {
				t.Fatalf("refusal was not logged:\n%s\n%s", out, h.messageLog())
			}
		})
	}
}

func TestAutoupdateRefusesSystemdHomeMismatch(t *testing.T) {
	h := newAutoHarness(t)
	h.setupUpdate()
	h.setupSystemd()
	dir := filepath.Join(h.home, ".config", "systemd", "user")
	unit := "[Service]\nExecStart=\"" + h.bin + "\" serve --home /srv/other\nWorkingDirectory=/srv/other\n"
	h.writeFile(filepath.Join(dir, "repairflow.service"), unit, 0o644)

	out, err := h.run()
	if err != nil {
		t.Fatalf("a refused layout must not fail the job: %v\n%s\n%s", err, out, h.messageLog())
	}
	h.requireNoMutation(t)
	if h.has("systemctl") {
		t.Fatalf("unrelated systemd unit was touched:\n%s", h.callsLog())
	}
	if !strings.Contains(h.messageLog(), "refuse:") {
		t.Fatalf("refusal was not logged:\n%s", h.messageLog())
	}
}

func TestAutoupdateSystemdStopsAndStartsUnit(t *testing.T) {
	h := newAutoHarness(t)
	h.setupUpdate()
	h.setupSystemd()

	out, err := h.run()
	if err != nil {
		t.Fatalf("unexpected failure: %v\n%s\n%s", err, out, h.callsLog())
	}
	h.requireInOrder(t,
		[]string{"systemctl", "--user stop repairflow.service", "|0.1.0"},
		[]string{"repairflow", "backup create", "|0.1.0"},
		[]string{"update", "--apply", "|0.1.0"},
		[]string{"systemctl", "--user start repairflow.service", "|9.9.9"},
	)
	if h.has("rc-service") {
		t.Fatalf("systemd host must not consult OpenRC:\n%s", h.callsLog())
	}
}

func TestAutoupdateProbesConfiguredHostAndPort(t *testing.T) {
	for _, tc := range []struct {
		name string
		host string
		port int
		want string
	}{
		{name: "default", host: "127.0.0.1", port: 8091, want: "http://127.0.0.1:8091/login"},
		{name: "custom-port", host: "127.0.0.1", port: 9123, want: "http://127.0.0.1:9123/login"},
		{name: "wildcard-v4", host: "0.0.0.0", port: 8091, want: "http://127.0.0.1:8091/login"},
		{name: "wildcard-v6", host: "::", port: 8091, want: "http://[::1]:8091/login"},
		{name: "lan", host: "192.0.2.10", port: 8443, want: "http://192.0.2.10:8443/login"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newAutoHarness(t)
			h.setupUpdate()
			h.setupOpenRC()
			h.set("config.json", configJSON(tc.host, tc.port))

			out, err := h.run()
			if err != nil {
				t.Fatalf("unexpected failure: %v\n%s\n%s", err, out, h.callsLog())
			}
			if !h.has(tc.want) {
				t.Fatalf("did not probe %s:\n%s", tc.want, h.callsLog())
			}
		})
	}
}

func TestAutoupdateRefusesUnparseableConfig(t *testing.T) {
	h := newAutoHarness(t)
	h.setupUpdate()
	h.setupOpenRC()
	h.set("config.json", `{"ok":true,"data":{"config":{"Web":{"Host":"","Port":0}}}}`)

	out, err := h.run()
	if err != nil {
		t.Fatalf("a refused config must not fail the job: %v\n%s\n%s", err, out, h.callsLog())
	}
	h.requireNoMutation(t)
	if h.has("repairflow stop") {
		t.Fatalf("service was stopped on an unparseable config:\n%s", h.callsLog())
	}
	if !strings.Contains(h.messageLog(), "refuse:") {
		t.Fatalf("refusal was not logged:\n%s\n%s", out, h.messageLog())
	}
}

func TestAutoupdateUpToDateIsNoop(t *testing.T) {
	h := newAutoHarness(t)
	h.setupUpdate()
	h.setupOpenRC()
	h.set("uptodate", "1")

	out, err := h.run()
	if err != nil {
		t.Fatalf("up-to-date run failed: %v\n%s\n%s", err, out, h.callsLog())
	}
	h.requireNoMutation(t)
	if h.has("repairflow stop") {
		t.Fatalf("service was stopped for an up-to-date workspace:\n%s", h.callsLog())
	}
	if !strings.Contains(h.messageLog(), "noop: up to date") {
		t.Fatalf("no-op was not logged:\n%s", h.messageLog())
	}
}

func TestAutoupdateFailedTagIsNotRetried(t *testing.T) {
	h := newAutoHarness(t)
	h.setupUpdate()
	h.setupOpenRC()
	if err := os.MkdirAll(filepath.Join(h.ws, "cache", "update"), 0o700); err != nil {
		t.Fatal(err)
	}
	h.writeFile(filepath.Join(h.ws, "cache", "update", "failed-v9.9.9"), "", 0o600)

	out, err := h.run()
	if err != nil {
		t.Fatalf("failed-tag skip must not fail the job: %v\n%s\n%s", err, out, h.callsLog())
	}
	h.requireNoMutation(t)
	if !strings.Contains(h.messageLog(), "skip: v9.9.9 already failed") {
		t.Fatalf("failed-tag refusal was not logged:\n%s\n%s", out, h.messageLog())
	}
}

func TestAutoupdateSerializesWithFlock(t *testing.T) {
	h := newAutoHarness(t)
	h.setupUpdate()
	h.setupOpenRC()
	state := filepath.Join(h.ws, "cache", "update")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	lock, err := os.OpenFile(filepath.Join(state, "lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)

	out, err := h.run()
	if err != nil {
		t.Fatalf("second instance must be a no-op: %v\n%s", err, out)
	}
	if h.callsLog() != "" {
		t.Fatalf("concurrent invocation made calls:\n%s", h.callsLog())
	}
}

// An interruption after the stop must leave the workspace untouched and a
// truthful manual-recovery message, never a half-restored copy.
func TestAutoupdateInterruptionAfterStop(t *testing.T) {
	h := newAutoHarness(t)
	h.setupUpdate()
	h.setupOpenRC()
	h.set("slow_backup", "1")

	cmd := h.command()
	var stderr bytes.Buffer
	cmd.Stdout = io.Discard
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	started := filepath.Join(h.state, "slow_backup_started")
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			t.Fatalf("backup never started: %s", stderr.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatalf("interrupted run reported success: %s", stderr.String())
	}
	if !strings.Contains(h.messageLog()+stderr.String(), "MANUAL RECOVERY") {
		t.Fatalf("no manual recovery message:\n%s\n%s", h.messageLog(), stderr.String())
	}
	if h.has("--apply") {
		t.Fatalf("interrupted run still applied a candidate:\n%s", h.callsLog())
	}
	if _, err := os.Stat(filepath.Join(h.ws, "workspace-marker")); err != nil {
		t.Fatalf("live workspace was moved: %v", err)
	}
	if moved, _ := filepath.Glob(h.ws + ".broken-*"); len(moved) != 0 {
		t.Fatalf("live workspace was renamed: %v", moved)
	}
}

func TestAutoupdateKeepsOperatorBackups(t *testing.T) {
	h := newAutoHarness(t)
	h.setupUpdate()
	h.setupOpenRC()
	backups := filepath.Join(h.home, "backups")
	if err := os.MkdirAll(backups, 0o700); err != nil {
		t.Fatal(err)
	}
	manual := []string{
		filepath.Join(backups, "pre-v0.1.0-before service window.rfbak"),
		filepath.Join(backups, "pre-v0.0.9-operator copy.rfbak"),
		filepath.Join(backups, "pre-v0.0.8-annual audit.rfbak"),
		filepath.Join(backups, "pre-v0.0.7-before disk swap.rfbak"),
	}
	for _, path := range manual {
		h.writeFile(path, "operator backup\n", 0o600)
	}

	out, err := h.run()
	if err != nil {
		t.Fatalf("unexpected failure: %v\n%s\n%s", err, out, h.callsLog())
	}
	for _, path := range manual {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("operator backup was deleted: %v", err)
		}
	}
}

func TestAutoupdateHealthFailuresPreserveState(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*autoHarness)
	}{
		{
			name: "wrong-version",
			setup: func(h *autoHarness) {
				h.set("about.html", "<p>version 0.1.0 commit newcommit</p>\n")
			},
		},
		{
			name: "wrong-commit",
			setup: func(h *autoHarness) {
				h.set("about.html", "<p>version 9.9.9 commit oldcommit</p>\n")
			},
		},
		{
			name:  "unreachable",
			setup: func(h *autoHarness) { h.set("curl_fail", "1") },
		},
		{
			name:  "failed-restart",
			setup: func(h *autoHarness) { h.set("start_fail", "1") },
		},
		{
			name:  "record-count-changed",
			setup: func(h *autoHarness) {},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newAutoHarness(t)
			h.setupUpdate()
			h.setupOpenRC()
			tc.setup(h)
			if tc.name == "record-count-changed" {
				// Reads before the swap see 3; the candidate loses rows.
				h.set("customers_after", "7")
			}

			out, err := h.run()
			if err == nil {
				t.Fatalf("unhealthy candidate reported success:\n%s\n%s", out, h.callsLog())
			}
			h.requirePreserved(t)
			if !h.has("repairflow start") {
				t.Fatalf("candidate was never started:\n%s", h.callsLog())
			}
		})
	}
}

func TestAutoupdateApplyFailurePreservesWorkspace(t *testing.T) {
	h := newAutoHarness(t)
	h.setupUpdate()
	h.setupOpenRC()
	h.set("apply_fail", "1")

	out, err := h.run()
	if err == nil {
		t.Fatalf("failed apply reported success:\n%s\n%s", out, h.callsLog())
	}
	h.requirePreserved(t)
	if h.lineIndex("repairflow", "customer list", "|9.9.9") >= 0 {
		t.Fatalf("no binary opened the workspace after a failed apply:\n%s", h.callsLog())
	}
}

func TestAutoupdateLogFailureDoesNotMoveOrClaimRollback(t *testing.T) {
	h := newAutoHarness(t)
	h.setupUpdate()
	h.setupOpenRC()
	h.set("about.html", "<p>version 0.1.0 commit oldcommit</p>\n")
	logs := filepath.Join(h.ws, "logs")
	if err := os.MkdirAll(logs, 0o700); err != nil {
		t.Fatal(err)
	}
	// An unwritable update.log must not abort before the failure is reported.
	if err := os.MkdirAll(filepath.Join(logs, "update.log"), 0o700); err != nil {
		t.Fatal(err)
	}

	out, err := h.run()
	if err == nil {
		t.Fatalf("unhealthy candidate reported success:\n%s", out)
	}
	if !strings.Contains(out, "MANUAL RECOVERY") {
		t.Fatalf("manual recovery was not reported on stderr:\n%s", out)
	}
	if strings.Contains(out, "rolled back") {
		t.Fatalf("failure was reported as a rollback:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(h.ws, "workspace-marker")); err != nil {
		t.Fatalf("live workspace was moved: %v", err)
	}
	if moved, _ := filepath.Glob(h.ws + ".broken-*"); len(moved) != 0 {
		t.Fatalf("live workspace was renamed: %v", moved)
	}
	if _, err := os.Stat(h.bin + ".prev"); err != nil {
		t.Fatalf("previous binary was not preserved: %v", err)
	}
}
