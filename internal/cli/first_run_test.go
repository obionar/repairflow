package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"repairflow/internal/config"
	"repairflow/internal/db"
	"repairflow/internal/workspace"
)

// TestInitInitializesDatabaseForDoctor pins the first-run contract: a plain
// init leaves a healthy, current-schema database so doctor is green
// immediately, without creating records or accounts.
func TestInitInitializesDatabaseForDoctor(t *testing.T) {
	home := filepath.Join(t.TempDir(), "ws")
	if res := run(t, nil, "init", "--home", home, "--json"); res.code != 0 {
		t.Fatalf("init exit %d: %s", res.code, res.stderr)
	}

	res := run(t, nil, "doctor", "--home", home, "--json")
	if res.code != 0 {
		t.Fatalf("doctor after init exit %d: %s", res.code, res.stderr)
	}
	data := decode(t, res.stdout)["data"].(map[string]any)
	if data["ok"] != true {
		t.Fatalf("doctor after init is unhealthy: %v", data)
	}
	if _, err := os.Stat(db.Path(home)); err != nil {
		t.Fatalf("init did not create the database: %v", err)
	}
	if users := run(t, nil, "user", "list", "--home", home, "--json"); users.code != 0 {
		t.Fatalf("user list: %s", users.stderr)
	}
}

// TestInitRerunPreservesDataAndConfig proves a repeated init migrates in place
// and never rewrites config or removes records and accounts.
func TestInitRerunPreservesDataAndConfig(t *testing.T) {
	home := filepath.Join(t.TempDir(), "ws")
	env := map[string]string{"USER": "tester", "REPAIRFLOW_NEW_PASSWORD": "password12345"}
	if res := run(t, env, "init", "--home", home, "--json"); res.code != 0 {
		t.Fatalf("init: %s", res.stderr)
	}
	if res := run(t, env, "user", "add", "admin", "--role", "admin", "--home", home, "--json"); res.code != 0 {
		t.Fatalf("user add: %s", res.stderr)
	}
	if res := run(t, env, "customer", "add", "--name", "Ada Lovelace", "--home", home, "--json"); res.code != 0 {
		t.Fatalf("customer add: %s", res.stderr)
	}
	configPath := filepath.Join(home, "config.toml")
	before, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}

	if res := run(t, env, "init", "--home", home, "--json"); res.code != 0 {
		t.Fatalf("repeat init: %s", res.stderr)
	}
	after, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("repeat init rewrote config:\nbefore %q\nafter %q", before, after)
	}
	users := run(t, env, "user", "list", "--home", home, "--json")
	if users.code != 0 || !strings.Contains(users.stdout, `"username":"admin"`) {
		t.Fatalf("repeat init lost the account: code=%d stdout=%s stderr=%s", users.code, users.stdout, users.stderr)
	}
	customers := run(t, env, "customer", "list", "--home", home, "--json")
	if customers.code != 0 || !strings.Contains(customers.stdout, "Ada Lovelace") {
		t.Fatalf("repeat init lost the customer: code=%d stdout=%s stderr=%s", customers.code, customers.stdout, customers.stderr)
	}
	if doc := run(t, env, "doctor", "--home", home, "--json"); doc.code != 0 {
		t.Fatalf("doctor after repeat init exit %d: %s", doc.code, doc.stderr)
	}
}

// TestOutdatedSchemaRequiresVerifiedBackup pins the migration gate: ordinary
// commands refuse an older schema, and init migrates only with a matching
// verified pre-migration archive.
func TestOutdatedSchemaRequiresVerifiedBackup(t *testing.T) {
	home := filepath.Join(t.TempDir(), "ws")
	if res := run(t, nil, "init", "--home", home, "--json"); res.code != 0 {
		t.Fatalf("init: %s", res.stderr)
	}
	ctx := context.Background()
	handle, err := db.Open(ctx, db.Path(home))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handle.ExecContext(ctx, `DROP TABLE label_printer`); err != nil {
		t.Fatal(err)
	}
	if _, err := handle.ExecContext(ctx, `DELETE FROM schema_migrations WHERE version = ?`, db.LatestVersion()); err != nil {
		t.Fatal(err)
	}
	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}

	res := run(t, nil, "customer", "list", "--home", home, "--json")
	if res.code != 3 || !strings.Contains(res.stderr, "init --backup") {
		t.Fatalf("outdated schema must refuse with the migration remedy: code=%d stderr=%s", res.code, res.stderr)
	}
	handle, err = db.Open(ctx, db.Path(home))
	if err != nil {
		t.Fatal(err)
	}
	version, err := db.CurrentVersion(ctx, handle)
	if err != nil || version != db.LatestVersion()-1 {
		t.Fatalf("ordinary command migrated the schema: version=%d err=%v", version, err)
	}
	_ = handle.Close()

	backupPath := filepath.Join(t.TempDir(), "pre-migration.rfbak")
	if res := run(t, nil, "backup", "create", "--output", backupPath, "--home", home, "--json"); res.code != 0 {
		t.Fatalf("pre-migration backup: %s", res.stderr)
	}
	if res := run(t, nil, "init", "--backup", backupPath, "--home", home, "--json"); res.code != 0 {
		t.Fatalf("gated init: %s", res.stderr)
	}
	if res := run(t, nil, "customer", "list", "--home", home, "--json"); res.code != 0 {
		t.Fatalf("command after migration: %s", res.stderr)
	}
	if res := run(t, nil, "doctor", "--home", home, "--json"); res.code != 0 {
		t.Fatalf("doctor after migration: %s", res.stderr)
	}
}

func TestMigrationWaitsForExclusiveWorkspaceLock(t *testing.T) {
	home := filepath.Join(t.TempDir(), "ws")
	if res := run(t, nil, "init", "--home", home, "--json"); res.code != 0 {
		t.Fatalf("init: %s", res.stderr)
	}
	ctx := context.Background()
	unlock, err := workspace.Lock(ctx, home, true)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	waitCtx, cancel := context.WithTimeout(ctx, 75*time.Millisecond)
	defer cancel()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, closeDB, err := openService(waitCtx, home, logger); err == nil {
		closeDB()
		t.Fatal("shared open must wait while the exclusive migration lock is held")
	} else if got := err.Error(); !strings.Contains(got, "workspace is busy") {
		t.Fatalf("unexpected lock refusal: %v", err)
	}
}

// TestInitRefusesCorruptOrNewerDatabase proves init opens the database and
// never reports success over an unreadable or future schema.
func TestInitRefusesCorruptOrNewerDatabase(t *testing.T) {
	t.Run("corrupt", func(t *testing.T) {
		home := filepath.Join(t.TempDir(), "ws")
		if res := run(t, nil, "init", "--home", home, "--json"); res.code != 0 {
			t.Fatalf("init: %s", res.stderr)
		}
		path := db.Path(home)
		for _, suffix := range []string{"-wal", "-shm"} {
			_ = os.Remove(path + suffix)
		}
		if err := os.WriteFile(path, []byte("this is not a sqlite database"), 0o600); err != nil {
			t.Fatal(err)
		}
		if res := run(t, nil, "init", "--home", home, "--json"); res.code == 0 {
			t.Fatalf("init silently accepted a corrupt database: %+v", res)
		}
	})

	t.Run("newer schema", func(t *testing.T) {
		home := filepath.Join(t.TempDir(), "ws")
		if res := run(t, nil, "init", "--home", home, "--json"); res.code != 0 {
			t.Fatalf("init: %s", res.stderr)
		}
		ctx := context.Background()
		handle, err := db.Open(ctx, db.Path(home))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := handle.ExecContext(ctx,
			`INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, 'future', 'now')`,
			db.LatestVersion()+1); err != nil {
			_ = handle.Close()
			t.Fatal(err)
		}
		if err := handle.Close(); err != nil {
			t.Fatal(err)
		}
		if res := run(t, nil, "init", "--home", home, "--json"); res.code == 0 {
			t.Fatalf("init silently accepted a newer schema: %+v", res)
		}
	})

	t.Run("config", func(t *testing.T) {
		home := filepath.Join(t.TempDir(), "ws")
		if res := run(t, nil, "init", "--home", home, "--json"); res.code != 0 {
			t.Fatalf("init: %s", res.stderr)
		}
		if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte("not = a valid config\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if res := run(t, nil, "init", "--home", home, "--json"); res.code == 0 {
			t.Fatalf("init silently accepted a malformed config: %+v", res)
		}
	})
}

// TestInitLanguageOnExistingConfigIsReported pins the additive ignored flag and
// the human notice; an existing config is never rewritten.
func TestInitLanguageOnExistingConfigIsReported(t *testing.T) {
	home := filepath.Join(t.TempDir(), "ws")
	if res := run(t, nil, "init", "--home", home, "--json"); res.code != 0 {
		t.Fatalf("init: %s", res.stderr)
	}
	configPath := filepath.Join(home, "config.toml")
	before, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}

	res := run(t, nil, "init", "--home", home, "--language", "el", "--json")
	if res.code != 0 {
		t.Fatalf("repeat init --language exit %d: %s", res.code, res.stderr)
	}
	data := decode(t, res.stdout)["data"].(map[string]any)
	ignored, _ := data["ignored"].([]any)
	if len(ignored) != 1 || ignored[0] != "language" {
		t.Fatalf("ignored flags: %v", data["ignored"])
	}
	after, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("repeat init --language rewrote config")
	}

	text := run(t, nil, "init", "--home", home, "--language", "el")
	if text.code != 0 {
		t.Fatalf("text init exit %d: %s", text.code, text.stderr)
	}
	if !strings.Contains(text.stdout, "language") || !strings.Contains(text.stdout, "not applied") {
		t.Fatalf("human notice missing: %q", text.stdout)
	}
	if strings.Contains(text.stdout, "{") {
		t.Fatalf("human init output fell back to JSON: %q", text.stdout)
	}
}

// TestHelpTargetUsesFocusedRenderer keeps `help CMD` identical in scope to
// `CMD --help`; bare help stays the global page and unknown topics fail.
func TestHelpTargetUsesFocusedRenderer(t *testing.T) {
	res := run(t, nil, "help", "init")
	if res.code != 0 {
		t.Fatalf("help init exit %d: %s", res.code, res.stderr)
	}
	if !strings.Contains(res.stdout, "repairflow init") || strings.Contains(res.stdout, "attachment purge") {
		t.Fatalf("help init is not focused: %q", res.stdout)
	}
	if lines := strings.Count(res.stdout, "\n"); lines >= strings.Count(usageText, "\n") {
		t.Fatalf("help init printed the global page (%d lines)", lines)
	}
	if res := run(t, nil, "help", "user", "add"); res.code != 0 || !strings.Contains(res.stdout, "user add") {
		t.Fatalf("help user add: code=%d stdout=%q stderr=%q", res.code, res.stdout, res.stderr)
	}
	if res := run(t, nil, "help", "bogus"); res.code != 2 {
		t.Fatalf("help bogus exit %d, want 2 (stdout %q stderr %q)", res.code, res.stdout, res.stderr)
	}
	if res := run(t, nil, "help"); res.stdout != usageText {
		t.Fatal("bare help must still print the global page")
	}
}

// serveHelperEnv marks the re-executed test binary as the serve subprocess.
const serveHelperEnv = "REPAIRFLOW_TEST_SERVE_HELPER"

// TestServeHelperProcess is the subprocess entry point; it never runs as a
// real test because the parent does not set serveHelperEnv.
func TestServeHelperProcess(t *testing.T) {
	if os.Getenv(serveHelperEnv) != "1" {
		return
	}
	sep := -1
	for i, arg := range os.Args {
		if arg == "--" {
			sep = i
			break
		}
	}
	if sep < 0 {
		_, _ = fmt.Fprintln(os.Stderr, "serve helper: missing -- argument separator")
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(Run(ctx, os.Args[sep+1:], os.Stdin, os.Stdout, os.Stderr, os.Getenv))
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// reservePort returns a free loopback port and releases it for the child.
func reservePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

// setWorkspacePort writes a synthetic listener port into the workspace config.
func setWorkspacePort(t *testing.T, home string, port int) {
	t.Helper()
	path := filepath.Join(home, "config.toml")
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Web.Port = port
	if err := os.WriteFile(path, config.Render(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
}

func startServeHelper(t *testing.T, home string, extra ...string) (*exec.Cmd, *lockedBuffer, *lockedBuffer) {
	t.Helper()
	args := append([]string{"-test.run=^TestServeHelperProcess$", "--", "serve", "--home", home}, extra...)
	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = append(os.Environ(), serveHelperEnv+"=1")
	stdout, stderr := &lockedBuffer{}, &lockedBuffer{}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start serve helper: %v", err)
	}
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	return cmd, stdout, stderr
}

func waitForText(t *testing.T, buf *lockedBuffer, want string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(buf.String(), want) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %q; output: %q", want, buf.String())
}

func waitForServe(t *testing.T, cmd *exec.Cmd) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("serve process did not stop")
		return nil
	}
}

// TestServeReportsReadinessAndShutdown proves readiness is printed only after
// the listener accepts connections, JSON stdout stays a single envelope, and an
// orderly stop is logged and rendered as human output-free text.
func TestServeReportsReadinessAndShutdown(t *testing.T) {
	home := filepath.Join(t.TempDir(), "ws")
	if res := run(t, nil, "init", "--home", home, "--json"); res.code != 0 {
		t.Fatalf("init: %s", res.stderr)
	}
	port := reservePort(t)
	setWorkspacePort(t, home, port)

	cmd, stdout, stderr := startServeHelper(t, home, "--json")
	waitForText(t, stderr, "web interface ready")
	if running := stdout.String(); running != "" {
		t.Fatalf("JSON mode wrote lifecycle output to stdout before completion: %q", running)
	}
	url := fmt.Sprintf("http://127.0.0.1:%d/login", port)
	ready := stderr.String()
	for _, want := range []string{url, "Ctrl+C", "repairflow user add admin --role admin --home " + home, "schema_version="} {
		if !strings.Contains(ready, want) {
			t.Errorf("readiness output missing %q: %q", want, ready)
		}
	}

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("listener was reported ready but not accepting: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /login status %d", resp.StatusCode)
	}

	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	if err := waitForServe(t, cmd); err != nil {
		t.Fatalf("serve exit: %v (stderr %s)", err, stderr.String())
	}
	if !strings.Contains(stderr.String(), "web interface stopped") {
		t.Errorf("orderly stop not logged: %q", stderr.String())
	}
	envelope := decode(t, strings.TrimSpace(stdout.String()))
	if envelope["ok"] != true || envelope["command"] != "serve" {
		t.Fatalf("serve JSON envelope: %v", envelope)
	}
	data := envelope["data"].(map[string]any)
	if data["home"] != home || int(data["port"].(float64)) != port {
		t.Fatalf("serve JSON data: %v", data)
	}
	if strings.Contains(stdout.String(), "web interface ready") {
		t.Fatalf("readiness leaked to stdout: %q", stdout.String())
	}
	if strings.Contains(stdout.String()+stderr.String(), "REPAIRFLOW_NEW_PASSWORD=") {
		t.Fatal("serve output leaked a password variable")
	}
}

// TestServeAccountWarningDisappearsWithActiveAccount proves the first-run
// remedy is scoped to the zero-active-accounts condition.
func TestServeAccountWarningDisappearsWithActiveAccount(t *testing.T) {
	home := filepath.Join(t.TempDir(), "ws")
	env := map[string]string{"REPAIRFLOW_NEW_PASSWORD": "password12345"}
	if res := run(t, env, "init", "--home", home, "--json"); res.code != 0 {
		t.Fatalf("init: %s", res.stderr)
	}
	if res := run(t, env, "user", "add", "admin", "--role", "admin", "--home", home, "--json"); res.code != 0 {
		t.Fatalf("user add: %s", res.stderr)
	}
	port := reservePort(t)
	setWorkspacePort(t, home, port)

	cmd, _, stderr := startServeHelper(t, home, "--json")
	waitForText(t, stderr, "web interface ready")
	if strings.Contains(stderr.String(), "no active accounts") {
		t.Fatalf("active account still warned as no accounts: %q", stderr.String())
	}
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	if err := waitForServe(t, cmd); err != nil {
		t.Fatalf("serve exit: %v (stderr %s)", err, stderr.String())
	}
}

// TestServeBindFailureHasNoReadinessLine keeps the stable port_in_use failure
// and proves readiness is never claimed for a listener that was not bound.
func TestServeBindFailureHasNoReadinessLine(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	port := ln.Addr().(*net.TCPAddr).Port

	home := filepath.Join(t.TempDir(), "ws")
	if res := run(t, nil, "init", "--home", home, "--json"); res.code != 0 {
		t.Fatalf("init: %s", res.stderr)
	}
	setWorkspacePort(t, home, port)

	cmd, stdout, stderr := startServeHelper(t, home, "--json")
	err = waitForServe(t, cmd)
	var exitErr *exec.ExitError
	if err == nil || !errors.As(err, &exitErr) || exitErr.ExitCode() != 3 {
		t.Fatalf("occupied port exit %v (want 3); stderr %s", err, stderr.String())
	}
	if !strings.Contains(stderr.String(), "port_in_use") {
		t.Errorf("stable port_in_use code missing: %q", stderr.String())
	}
	if strings.Contains(stderr.String(), "web interface ready") || strings.Contains(stdout.String(), "web interface ready") {
		t.Fatalf("readiness claimed after failed bind; stdout %q stderr %q", stdout.String(), stderr.String())
	}
	if stdout.String() != "" {
		t.Fatalf("JSON mode wrote stdout on bind failure: %q", stdout.String())
	}
}

// TestInitWizardInitializesDatabase proves the interactive path without an
// account or demo data still leaves doctor healthy.
func TestInitWizardInitializesDatabase(t *testing.T) {
	home := filepath.Join(t.TempDir(), "ws")
	port := reservePort(t)
	stdin := fmt.Sprintf("%s\n\n%d\n\nn\n", home, port)
	res := runStdin(t, stdin, nil, "init", "--interactive", "--json")
	if res.code != 0 {
		t.Fatalf("interactive init exit %d: %s", res.code, res.stderr)
	}
	if doc := run(t, nil, "doctor", "--home", home, "--json"); doc.code != 0 {
		t.Fatalf("doctor after interactive init exit %d: %s", doc.code, doc.stderr)
	}
}
