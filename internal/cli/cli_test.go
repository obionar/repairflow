package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	var out, errb bytes.Buffer
	code := Run(context.Background(), args, strings.NewReader(""), &out, &errb, func(key string) string { return env[key] })
	return execResult{code: code, stdout: out.String(), stderr: errb.String()}
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
