package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

type execResult struct {
	code   int
	stdout string
	stderr string
}

func run(t *testing.T, env map[string]string, args ...string) execResult {
	t.Helper()
	return runStdin(t, "", env, args...)
}

// runStdin feeds a scripted stdin (for --password-stdin).
func runStdin(t *testing.T, stdin string, env map[string]string, args ...string) execResult {
	t.Helper()
	var out, errb bytes.Buffer
	code := Run(context.Background(), args, strings.NewReader(stdin), &out, &errb, func(key string) string { return env[key] })
	return execResult{code: code, stdout: out.String(), stderr: errb.String()}
}

// loginWorks reports whether username/password authenticate against the workspace.
func loginWorks(t *testing.T, home, username, password string) bool {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc, closeDB, err := openService(context.Background(), home, logger)
	if err != nil {
		t.Fatalf("open service: %v", err)
	}
	defer closeDB()
	_, _, err = svc.Login(context.Background(), username, password)
	return err == nil
}

// oneHumanLine reports whether out is a single non-JSON line of text.
func oneHumanLine(out string) bool {
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	return len(lines) == 1 && !strings.Contains(out, "{")
}

func decode(t *testing.T, raw string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("invalid JSON output %q: %v", raw, err)
	}
	return m
}

func TestVersionJSON(t *testing.T) {
	res := run(t, nil, "version", "--json")
	if res.code != 0 {
		t.Fatalf("exit %d, stderr %s", res.code, res.stderr)
	}
	env := decode(t, res.stdout)
	if env["ok"] != true || env["command"] != "version" {
		t.Fatalf("envelope: %v", env)
	}
	data, _ := env["data"].(map[string]any)
	if data["version"] == "" || data["platform"] == "" {
		t.Fatalf("version data: %v", data)
	}
}

func TestVersionText(t *testing.T) {
	res := run(t, nil, "--version")
	if res.code != 0 || !strings.HasPrefix(res.stdout, "repairflow ") {
		t.Fatalf("unexpected version output: %+v", res)
	}
}

func TestHelpAndEmptyArgs(t *testing.T) {
	for _, args := range [][]string{{"help"}, {"--help"}, {"-h"}, {}} {
		res := run(t, nil, args...)
		if res.code != 0 || !strings.Contains(res.stdout, "Usage:") {
			t.Fatalf("help failed for %v: %+v", args, res)
		}
	}
}

// TestUsageCoversEveryDocumentedCommand keeps --help in sync with
// docs/CLI-CONTRACT.md. Every command group and subcommand the contract
// promises must be discoverable from the built-in help.
func TestUsageCoversEveryDocumentedCommand(t *testing.T) {
	required := []string{
		"init --interactive", "config check", "status", "doctor", "cleanup", "serve",
		"customer add", "customer list", "customer show", "customer history",
		"customer update", "customer archive", "customer unarchive",
		"repair add", "repair list", "repair show", "repair update", "repair stage",
		"repair entry ", "repair entry-edit", "repair entry-delete", "repair entry-restore",
		"repair pickup", "repair archive", "repair unarchive",
		"repair part-add", "repair part-list", "repair part-update",
		"repair part-remove", "repair part-restore",
		"estimate create", "estimate list", "estimate show", "estimate accept",
		"attachment add", "attachment list", "attachment show", "attachment export",
		"attachment caption", "attachment link", "attachment unlink",
		"attachment detach", "attachment restore", "attachment purge",
		"user add", "user list", "user passwd", "user enable", "user disable",
		"backup create", "backup verify", "restore ARCHIVE",
		"service install", "service status", "service remove",
		"update check", "update apply", "update keygen", "update sign",
		"seed demo", "demo reset", "version", "help",
	}
	for _, want := range required {
		if !strings.Contains(usageText, want) {
			t.Errorf("help text does not mention %q", want)
		}
	}
	for _, stale := range []string{"Stage 5", "internal decision"} {
		if strings.Contains(usageText, stale) {
			t.Errorf("help text still contains stale reference %q", stale)
		}
	}
	for _, line := range strings.Split(usageText, "\n") {
		if len(line) > 100 {
			t.Errorf("help line exceeds 100 columns (%d): %q", len(line), line)
		}
	}
}

// documentedFlagExceptions lists contract fenced-block tokens that are not
// required to appear in usageText. Add a token here only for a spelling form
// that is deliberately not a help flag (for example an "--home=PATH" spelling
// next to the documented "--home PATH" flag). Keep it empty otherwise.
var documentedFlagExceptions = map[string]bool{}

// contractFlagPattern finds flag tokens in the contract's fenced examples.
var contractFlagPattern = regexp.MustCompile(`(?:^|[^A-Za-z0-9_-])(--[A-Za-z][A-Za-z0-9_-]*)`)

// TestUsageCoversEveryDocumentedFlag keeps the built-in help in sync with every
// flag the fenced examples in docs/CLI-CONTRACT.md promise. A flag may leave the
// help text only by leaving the contract, or through documentedFlagExceptions.
func TestUsageCoversEveryDocumentedFlag(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "CLI-CONTRACT.md"))
	if err != nil {
		t.Fatalf("read docs/CLI-CONTRACT.md: %v", err)
	}
	flags := documentedFlagsInContract(t, string(raw))
	// The contract documents ~50 flags; a large drop means the extractor, not
	// the help text, changed.
	if len(flags) < 40 {
		t.Fatalf("extracted only %d documented flags from docs/CLI-CONTRACT.md, want at least 40", len(flags))
	}
	for _, flag := range flags {
		if documentedFlagExceptions[flag] {
			continue
		}
		if !usageMentionsFlag(usageText, flag) {
			t.Errorf("docs/CLI-CONTRACT.md documents %q but the help text does not offer it", flag)
		}
	}
}

// documentedFlagsInContract returns the sorted, de-duplicated flags that appear
// inside fenced code blocks of the contract document.
func documentedFlagsInContract(t *testing.T, doc string) []string {
	t.Helper()
	seen := map[string]bool{}
	flags := []string{}
	inFence := false
	for _, line := range strings.Split(doc, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inFence = !inFence
			continue
		}
		if !inFence {
			continue
		}
		for _, match := range contractFlagPattern.FindAllStringSubmatch(line, -1) {
			if seen[match[1]] {
				continue
			}
			seen[match[1]] = true
			flags = append(flags, match[1])
		}
	}
	if inFence {
		t.Fatal("docs/CLI-CONTRACT.md has an unterminated fenced code block")
	}
	sort.Strings(flags)
	return flags
}

// usageMentionsFlag reports whether the help text contains flag as a whole
// token, so "--note" is not satisfied by "--notes".
func usageMentionsFlag(text, flag string) bool {
	from := 0
	for {
		idx := strings.Index(text[from:], flag)
		if idx < 0 {
			return false
		}
		start := from + idx
		end := start + len(flag)
		beforeOK := start == 0 || !isFlagTokenByte(text[start-1])
		afterOK := end == len(text) || !isFlagTokenByte(text[end])
		if beforeOK && afterOK {
			return true
		}
		from = end
	}
}

func isFlagTokenByte(b byte) bool {
	return b == '-' || b == '_' ||
		(b >= '0' && b <= '9') || (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z')
}

func TestUnknownCommandIsUsageError(t *testing.T) {
	res := run(t, nil, "frobnicate", "--json")
	if res.code != 2 {
		t.Fatalf("exit %d, want 2", res.code)
	}
	env := decode(t, res.stderr)
	if env["ok"] != false {
		t.Fatalf("envelope: %v", env)
	}
	errObj, _ := env["error"].(map[string]any)
	if errObj["code"] != "usage_error" {
		t.Fatalf("error: %v", errObj)
	}
}

func TestInitAndConfigCheckFlow(t *testing.T) {
	home := filepath.Join(t.TempDir(), "ws")

	res := run(t, nil, "init", "--home", home, "--json")
	if res.code != 0 {
		t.Fatalf("init exit %d: %s", res.code, res.stderr)
	}
	env := decode(t, res.stdout)
	data, _ := env["data"].(map[string]any)
	if data["already_initialized"] != false {
		t.Fatalf("init data: %v", data)
	}

	// Second init is idempotent and reports it.
	res = run(t, nil, "init", "--home="+home, "--json")
	if res.code != 0 {
		t.Fatalf("second init exit %d: %s", res.code, res.stderr)
	}
	data, _ = decode(t, res.stdout)["data"].(map[string]any)
	if data["already_initialized"] != true {
		t.Fatalf("second init data: %v", data)
	}

	res = run(t, nil, "config", "check", "--home", home, "--json")
	if res.code != 0 {
		t.Fatalf("config check exit %d: %s", res.code, res.stderr)
	}
	data, _ = decode(t, res.stdout)["data"].(map[string]any)
	cfg, _ := data["config"].(map[string]any)
	if data["initialized"] != true || cfg["ConfigVersion"] != 1.0 {
		t.Fatalf("check data: %v", data)
	}
}

func TestHomePrecedenceFlagOverEnv(t *testing.T) {
	base := t.TempDir()
	flagHome := filepath.Join(base, "flag")
	envHome := filepath.Join(base, "env")

	res := run(t, map[string]string{"REPAIRFLOW_HOME": envHome}, "init", "--home", flagHome, "--json")
	if res.code != 0 {
		t.Fatalf("init exit %d: %s", res.code, res.stderr)
	}
	if _, err := filepath.Abs(flagHome); err != nil {
		t.Fatal(err)
	}
	if !dirExists(flagHome) || dirExists(envHome) {
		t.Fatalf("flag home must win: flag=%v env=%v", dirExists(flagHome), dirExists(envHome))
	}

	res = run(t, map[string]string{"REPAIRFLOW_HOME": envHome}, "init", "--json")
	if res.code != 0 || !dirExists(envHome) {
		t.Fatalf("env home must be used when no flag: %+v", res)
	}
}

func TestUnsafeAndInvalidPathsAreRefused(t *testing.T) {
	res := run(t, nil, "init", "--home", "relative/path", "--json")
	if res.code != 2 {
		t.Fatalf("relative path exit %d, want 2", res.code)
	}
	if code := decode(t, res.stderr)["error"].(map[string]any)["code"]; code != "validation_error" {
		t.Fatalf("error code: %v", code)
	}

	base := t.TempDir()
	res = run(t, map[string]string{"REPAIRFLOW_REFUSED_ROOTS": base}, "init", "--home", filepath.Join(base, "inside"), "--json")
	if res.code != 2 {
		t.Fatalf("refused root exit %d, want 2", res.code)
	}
	if code := decode(t, res.stderr)["error"].(map[string]any)["code"]; code != "unsafe_path" {
		t.Fatalf("error code: %v", code)
	}
}

func TestMissingWorkspaceConfigCheck(t *testing.T) {
	res := run(t, nil, "config", "check", "--home", filepath.Join(t.TempDir(), "nope"), "--json")
	if res.code != 3 {
		t.Fatalf("exit %d, want 3", res.code)
	}
	if code := decode(t, res.stderr)["error"].(map[string]any)["code"]; code != "workspace_not_initialized" {
		t.Fatalf("error code: %v", code)
	}
}

func TestDebugFlagAndEnvironmentShareDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		args []string
		env  map[string]string
	}{
		{args: []string{"--debug", "version", "--json"}},
		{args: []string{"version", "--json"}, env: map[string]string{EnvDebug: "1"}},
	} {
		res := run(t, tc.env, tc.args...)
		if res.code != 0 {
			t.Fatalf("debug version: %+v", res)
		}
		decode(t, res.stdout)
		for _, want := range []string{"level=DEBUG", "component=cli", "operation=run"} {
			if !strings.Contains(res.stderr, want) {
				t.Errorf("debug stderr missing %q: %q", want, res.stderr)
			}
		}
	}
	res := run(t, nil, "version", "--json")
	if res.stderr != "" {
		t.Fatalf("normal mode must not emit debug diagnostics: %q", res.stderr)
	}
}

func TestDebugNeverLogsPasswordOrArguments(t *testing.T) {
	home := filepath.Join(t.TempDir(), "ws")
	const password = "debug-secret-password"
	env := map[string]string{"USER": "tester", "REPAIRFLOW_NEW_PASSWORD": password}
	if res := run(t, env, "init", "--home", home); res.code != 0 {
		t.Fatalf("init: %s", res.stderr)
	}
	res := run(t, env, "user", "add", "debug-admin", "--role", "admin", "--home", home, "--debug", "--json")
	if res.code != 0 {
		t.Fatalf("user add: %s", res.stderr)
	}
	if strings.Contains(res.stdout+res.stderr, password) {
		t.Fatal("password leaked into debug output")
	}
	if !strings.Contains(res.stderr, "operation=open_service") || !strings.Contains(res.stderr, "duration_ms=") {
		t.Fatalf("database diagnostics missing: %q", res.stderr)
	}
}

func TestDoctorUninitializedWorkspaceIsReportedWithoutPanic(t *testing.T) {
	home := filepath.Join(t.TempDir(), "missing")
	res := run(t, nil, "doctor", "--home", home, "--json")
	if res.code != 3 {
		t.Fatalf("unhealthy doctor exit=%d stderr=%q", res.code, res.stderr)
	}
	envelope := decode(t, res.stdout)
	data := envelope["data"].(map[string]any)
	if data["ok"] != false || len(data["checks"].([]any)) == 0 {
		t.Fatalf("doctor report: %v", envelope)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("output unavailable") }

func TestOutputFailuresReturnInternalExit(t *testing.T) {
	for _, args := range [][]string{{"version", "--json"}, {"help"}} {
		if code := Run(context.Background(), args, strings.NewReader(""), failingWriter{}, &strings.Builder{}, func(string) string { return "" }); code != 1 {
			t.Errorf("output failure for %v: exit %d", args, code)
		}
	}
	if code := Run(context.Background(), []string{"unknown", "--json"}, strings.NewReader(""), &strings.Builder{}, failingWriter{}, func(string) string { return "" }); code != 1 {
		t.Fatalf("error output failure must return exit 1, got %d", code)
	}
}

func TestMissingHomeFlagValue(t *testing.T) {
	res := run(t, nil, "init", "--home", "--json")
	if res.code != 2 {
		t.Fatalf("exit %d, want 2", res.code)
	}
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// TestUserTextOutput checks the human account output: one short line per
// command, with the JSON payload unchanged underneath.
func TestUserTextOutput(t *testing.T) {
	home := filepath.Join(t.TempDir(), "ws")
	env := map[string]string{"USER": "tester", "REPAIRFLOW_NEW_PASSWORD": "password12345"}
	if res := run(t, env, "init", "--home", home); res.code != 0 {
		t.Fatalf("init: %s", res.stderr)
	}
	res := run(t, env, "user", "list", "--home", home)
	if res.code != 0 || res.stdout != "no accounts\n" {
		t.Fatalf("empty account list: code=%d stdout=%q stderr=%q", res.code, res.stdout, res.stderr)
	}
	res = run(t, env, "user", "add", "alice", "--role", "admin", "--home", home)
	if res.code != 0 {
		t.Fatalf("user add: %s", res.stderr)
	}
	if !oneHumanLine(res.stdout) || !strings.Contains(res.stdout, "alice") || !strings.Contains(res.stdout, "admin") {
		t.Fatalf("user add must print one human line: %q", res.stdout)
	}
	if res := run(t, env, "user", "add", "bob", "--role", "technician", "--home", home); res.code != 0 {
		t.Fatalf("user add bob: %s", res.stderr)
	}

	res = run(t, env, "user", "list", "--home", home)
	if !regexp.MustCompile(`(?m)^#\d+ alice \(admin, active\) created \S+$`).MatchString(res.stdout) {
		t.Fatalf("list line: %q", res.stdout)
	}
	if !strings.Contains(res.stdout, "(technician, active)") || strings.Contains(res.stdout, "{") {
		t.Fatalf("user list must be human text: %q", res.stdout)
	}

	for _, sub := range []string{"disable", "enable"} {
		res = run(t, env, "user", sub, "bob", "--home", home)
		if res.code != 0 || !oneHumanLine(res.stdout) || !strings.Contains(res.stdout, "bob") {
			t.Fatalf("user %s: code=%d stdout=%q stderr=%q", sub, res.code, res.stdout, res.stderr)
		}
	}
	res = run(t, env, "user", "passwd", "bob", "--home", home)
	if res.code != 0 || !oneHumanLine(res.stdout) || !strings.Contains(res.stdout, "bob") {
		t.Fatalf("user passwd text: code=%d stdout=%q stderr=%q", res.code, res.stdout, res.stderr)
	}
	if strings.Contains(res.stdout+res.stderr, "password12345") {
		t.Fatal("password leaked into output")
	}

	// JSON output is byte-identical to the pre-change contract.
	res = run(t, env, "user", "passwd", "bob", "--home", home, "--json")
	if want := `"data":{"password_changed":true,"sessions_revoked":true,"username":"bob"}`; !strings.Contains(res.stdout, want) {
		t.Fatalf("passwd JSON changed: %q", res.stdout)
	}
	res = run(t, env, "user", "list", "--home", home, "--json")
	if want := `{"ok":true,"command":"user list","contract":2,"data":[{"id":1,"username":"alice"`; !strings.HasPrefix(res.stdout, want) {
		t.Fatalf("list JSON changed: %q", res.stdout)
	}
}

// TestPasswordStdin covers the explicit --password-stdin source and its
// precedence over REPAIRFLOW_NEW_PASSWORD.
func TestPasswordStdin(t *testing.T) {
	home := filepath.Join(t.TempDir(), "ws")
	env := map[string]string{"USER": "tester", "REPAIRFLOW_NEW_PASSWORD": "env-password-1"}
	base := []string{"--home", home}
	if res := run(t, env, "init", "--home", home); res.code != 0 {
		t.Fatalf("init: %s", res.stderr)
	}
	// A trailing CRLF is stripped, and the flag wins over the environment.
	res := runStdin(t, "flag-password-1\r\n", env, append([]string{"user", "add", "alice", "--role", "admin", "--password-stdin"}, base...)...)
	if res.code != 0 {
		t.Fatalf("user add --password-stdin: exit %d %s", res.code, res.stderr)
	}
	if strings.Contains(res.stdout+res.stderr, "flag-password-1") {
		t.Fatal("password leaked into output")
	}
	if !loginWorks(t, home, "alice", "flag-password-1") {
		t.Fatal("--password-stdin password must be stored")
	}
	if loginWorks(t, home, "alice", "env-password-1") {
		t.Fatal("the explicit --password-stdin value must win over REPAIRFLOW_NEW_PASSWORD")
	}

	// passwd accepts the same flag.
	res = runStdin(t, "changed-password-2\n", env, append([]string{"user", "passwd", "alice", "--password-stdin"}, base...)...)
	if res.code != 0 || !loginWorks(t, home, "alice", "changed-password-2") {
		t.Fatalf("user passwd --password-stdin: exit %d %s", res.code, res.stderr)
	}

	// Empty stdin line is a usage error and changes nothing.
	res = runStdin(t, "\n", env, append([]string{"user", "passwd", "alice", "--password-stdin"}, base...)...)
	if res.code != 2 || !strings.Contains(res.stderr, "--password-stdin") {
		t.Fatalf("empty stdin password: exit %d %s", res.code, res.stderr)
	}

	// Too short: refused by the CLI with a clear message, before the service.
	short := map[string]string{"USER": "tester"}
	res = runStdin(t, "short\n", short, append([]string{"user", "add", "bob", "--role", "admin", "--password-stdin"}, base...)...)
	if res.code != 2 || !strings.Contains(res.stderr, "at least 6 characters") {
		t.Fatalf("short password: exit %d %s", res.code, res.stderr)
	}

	// No source and a non-terminal stdin keeps today's usage error.
	res = run(t, short, append([]string{"user", "add", "bob", "--role", "admin"}, base...)...)
	if res.code != 2 || !strings.Contains(res.stderr, "REPAIRFLOW_NEW_PASSWORD") {
		t.Fatalf("missing password: exit %d %s", res.code, res.stderr)
	}
}

// TestFocusedCommandHelp checks that "<command> --help" prints a short usage
// block for that command instead of the global page.
func TestFocusedCommandHelp(t *testing.T) {
	globalLines := strings.Count(usageText, "\n")
	for _, tc := range []struct {
		args      []string
		name      string
		unrelated string
	}{
		{[]string{"init", "--help"}, "init", "attachment purge"},
		{[]string{"serve", "--help"}, "serve", "customer add"},
		{[]string{"user", "--help"}, "user", "backup create"},
		{[]string{"user", "add", "--help"}, "user add", "estimate create"},
		{[]string{"service", "--help"}, "service", "restore ARCHIVE"},
		{[]string{"update", "--help"}, "update", "repair stage"},
	} {
		res := run(t, nil, tc.args...)
		if res.code != 0 {
			t.Fatalf("%v: exit %d %s", tc.args, res.code, res.stderr)
		}
		if !strings.Contains(res.stdout, tc.name) {
			t.Errorf("%v: focused help does not name %q: %q", tc.args, tc.name, res.stdout)
		}
		if strings.Contains(res.stdout, tc.unrelated) {
			t.Errorf("%v: focused help leaks unrelated command %q", tc.args, tc.unrelated)
		}
		if lines := strings.Count(res.stdout, "\n"); lines >= globalLines {
			t.Errorf("%v: focused help (%d lines) must be shorter than the global page (%d lines)", tc.args, lines, globalLines)
		}
	}
	// The global page is still what bare help prints.
	if res := run(t, nil, "help"); res.stdout != usageText {
		t.Fatal("bare help must still print the global page")
	}
}

// TestServePortInUseClassifiesEADDRINUSE pins the stable port_in_use code
// (exit 3) without touching internal/web.
func TestServePortInUse(t *testing.T) {
	// serve replaces TMPDIR with the workspace staging dir; restore it so the
	// testing package can clean up this test's own temporary directories.
	t.Setenv("TMPDIR", os.TempDir())
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	port := ln.Addr().(*net.TCPAddr).Port

	home := filepath.Join(t.TempDir(), "ws")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := fmt.Sprintf("config_version = 1\n[web]\nhost = \"127.0.0.1\"\nport = %d\n", port)
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	res := run(t, nil, "serve", "--home", home, "--json")
	if res.code != 3 {
		t.Fatalf("port in use exit %d, want 3 (stderr %s)", res.code, res.stderr)
	}
	body, _ := decode(t, res.stderr)["error"].(map[string]any)
	if body["code"] != "port_in_use" {
		t.Fatalf("error body: %v", body)
	}
}

// TestServiceInstallHints checks the additive web/log/status hints.
func TestServiceInstallHints(t *testing.T) {
	home := initWorkspace(t, filepath.Join(t.TempDir(), "ws"))
	unitDir := filepath.Join(t.TempDir(), "units")
	env := map[string]string{"REPAIRFLOW_UNIT_DIR": unitDir}

	res := run(t, env, "service", "install", "--init", "systemd", "--no-enable", "--home", home)
	if res.code != 0 {
		t.Fatalf("install: exit %d %s", res.code, res.stderr)
	}
	for _, want := range []string{
		"web: http://127.0.0.1:8091/login",
		"log: journalctl --user -u repairflow.service",
		"status: systemctl --user status repairflow.service",
	} {
		if !strings.Contains(res.stdout, want) {
			t.Errorf("install text missing %q:\n%s", want, res.stdout)
		}
	}

	res = run(t, env, "service", "install", "--init", "systemd", "--no-enable", "--home", home, "--json")
	data, _ := decode(t, res.stdout)["data"].(map[string]any)
	if data["web_url"] != "http://127.0.0.1:8091/login" ||
		data["log_location"] != "journalctl --user -u repairflow.service" ||
		data["status_command"] != "systemctl --user status repairflow.service" {
		t.Fatalf("install hints: %v", data)
	}

	// OpenRC reports its own log file and status command.
	initDir := filepath.Join(t.TempDir(), "init.d")
	res = run(t, map[string]string{"REPAIRFLOW_INIT_DIR": initDir},
		"service", "install", "--init", "openrc", "--no-enable", "--home", home)
	if res.code != 0 {
		t.Fatalf("openrc install: exit %d %s", res.code, res.stderr)
	}
	for _, want := range []string{
		"log: " + filepath.Join(home, "logs", "service.log"),
		"status: rc-service repairflow status",
	} {
		if !strings.Contains(res.stdout, want) {
			t.Errorf("openrc install text missing %q:\n%s", want, res.stdout)
		}
	}
}

// TestServiceInstallWildcardAndStaged covers a wildcard bind and an
// unprivileged (staged) install.
func TestServiceInstallWildcardAndStaged(t *testing.T) {
	home := filepath.Join(t.TempDir(), "ws")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := "config_version = 1\n[web]\nhost = \"0.0.0.0\"\nport = 8099\nallow_remote = true\n"
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	res := run(t, map[string]string{"REPAIRFLOW_UNIT_DIR": filepath.Join(t.TempDir(), "units")},
		"service", "install", "--init", "systemd", "--no-enable", "--home", home)
	if res.code != 0 {
		t.Fatalf("install: exit %d %s", res.code, res.stderr)
	}
	if !strings.Contains(res.stdout, "web: http://127.0.0.1:8099/login") {
		t.Errorf("wildcard bind must print the loopback URL:\n%s", res.stdout)
	}
	if !strings.Contains(res.stdout, "all interfaces") {
		t.Errorf("wildcard bind must warn about all interfaces:\n%s", res.stdout)
	}

	// An unwritable init directory stages the OpenRC script: never claim it runs.
	stagedDir := filepath.Join(t.TempDir(), "readonly")
	if err := os.MkdirAll(stagedDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(stagedDir, 0o755) })
	if os.Geteuid() == 0 {
		t.Skip("running as root: the init directory is writable")
	}
	res = run(t, map[string]string{"REPAIRFLOW_INIT_DIR": stagedDir},
		"service", "install", "--init", "openrc", "--no-enable", "--home", initWorkspace(t, filepath.Join(t.TempDir(), "ws2")))
	if res.code != 0 {
		t.Fatalf("staged install: exit %d %s", res.code, res.stderr)
	}
	if !strings.Contains(res.stdout, "staged") || !strings.Contains(res.stdout, "command:") {
		t.Errorf("staged install must say it is staged and print the command:\n%s", res.stdout)
	}
	if strings.Contains(res.stdout, "active=running") {
		t.Errorf("staged install must not claim it is running:\n%s", res.stdout)
	}
}
