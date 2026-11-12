// Test-only package: regression coverage for scripts/run-demo.sh.
//
// The launcher must be a one-shot foreground demo: it creates a disposable
// workspace, seeds it with the public demo login, serves it, and removes the
// workspace when the process exits. Credentials live only in this shell
// launcher, never in the application code.
package scripts_test

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func setupRunDemo(t *testing.T) (script, logFile, tmpRoot string) {
	t.Helper()
	src, err := os.ReadFile("run-demo.sh")
	if err != nil {
		t.Fatalf("read scripts/run-demo.sh: %v", err)
	}
	dir := t.TempDir()
	script = filepath.Join(dir, "run-demo.sh")
	if err := os.WriteFile(script, src, 0o755); err != nil {
		t.Fatalf("copy run-demo.sh: %v", err)
	}
	logFile = filepath.Join(dir, "calls.log")
	fake := "#!/bin/sh\nprintf '%s|%s\\n' \"$*\" \"${REPAIRFLOW_NEW_PASSWORD:-}\" >> \"" + logFile + "\"\nexit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "repairflow"), []byte(fake), 0o755); err != nil {
		t.Fatalf("write fake binary: %v", err)
	}
	return script, logFile, dir
}

func TestRunDemoIsSingleRunWithPublicLogin(t *testing.T) {
	script, logFile, root := setupRunDemo(t)
	tmpRoot := filepath.Join(root, "tmp")
	if err := os.Mkdir(tmpRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, "home")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("sh", script)
	cmd.Env = []string{
		"HOME=" + home,
		"PATH=/usr/bin:/bin",
		"TMPDIR=" + tmpRoot,
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("run-demo.sh: %v (stderr %q)", err, stderr.String())
	}

	calls, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("read calls: %v", err)
	}
	text := string(calls)
	if !strings.Contains(text, "init --demo --demo-user demo --home ") {
		t.Fatalf("init invocation missing: %q", text)
	}
	if !strings.Contains(text, "|repairflow\n") {
		t.Fatalf("REPAIRFLOW_NEW_PASSWORD=repairflow missing: %q", text)
	}
	if !strings.Contains(text, "serve --home ") {
		t.Fatalf("foreground serve invocation missing: %q", text)
	}

	// The temporary workspace must be gone after the foreground process exits.
	leftovers, err := filepath.Glob(filepath.Join(tmpRoot, "repairflow-demo.*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(leftovers) != 0 {
		t.Fatalf("temporary workspace was not removed: %v", leftovers)
	}
}

func TestRunDemoRefusesMissingBinary(t *testing.T) {
	dir := t.TempDir()
	src, err := os.ReadFile("run-demo.sh")
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "run-demo.sh")
	if err := os.WriteFile(script, src, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", script)
	cmd.Env = []string{"HOME=" + dir, "PATH=/usr/bin:/bin", "TMPDIR=" + dir}
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("missing binary must fail: %s", output)
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
		t.Fatalf("missing binary exit: %v", err)
	}
	if !strings.Contains(string(output), "no executable found") {
		t.Fatalf("missing binary message: %q", output)
	}
}
