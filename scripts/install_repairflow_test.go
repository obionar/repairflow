// Test-only package: regression coverage for scripts/install-repairflow.
//
// The installer must build from the checkout and replace the installed binary
// atomically (never leaving a .new file behind), report the previous version on
// update, and validate an existing workspace config with the new binary via an
// idempotent `init` without touching fresh installs or user data.
package scripts_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"repairflow/internal/config"
	"repairflow/internal/testutil"
)

func writeFake(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func setupInstall(t *testing.T) (script, root, home, prefix, calls string) {
	t.Helper()
	src, err := os.ReadFile(filepath.Join("..", "install-repairflow"))
	if err != nil {
		t.Fatalf("read install-repairflow: %v", err)
	}
	root = t.TempDir()
	script = filepath.Join(root, "install-repairflow")
	if err := os.WriteFile(script, src, 0o755); err != nil {
		t.Fatalf("copy install-repairflow: %v", err)
	}
	home = filepath.Join(root, "home")
	prefix = filepath.Join(root, "prefix")
	calls = filepath.Join(root, "calls.log")
	fakebin := filepath.Join(root, "fakebin")
	if err := os.MkdirAll(fakebin, 0o755); err != nil {
		t.Fatal(err)
	}

	// The "new" build product that the fake `make build` drops in the checkout.
	built := filepath.Join(root, "built-repairflow")
	writeFake(t, built, "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \""+calls+"\"\n"+
		"case \"${1:-}\" in version) echo 'repairflow test-new' ;; esac\nexit 0\n")
	// Fake make: only `build` is understood, producing the fake binary above in
	// the checkout (the real make is invoked with -C, which the fake ignores).
	writeFake(t, filepath.Join(fakebin, "make"),
		"#!/bin/sh\ncp \""+built+"\" \""+root+"/repairflow\"\n")
	return script, root, home, prefix, calls
}

func runInstall(t *testing.T, script, home, prefix, fakebin string) string {
	t.Helper()
	cmd := exec.Command("sh", script)
	cmd.Env = []string{
		"HOME=" + home,
		"PATH=" + fakebin + ":/usr/bin:/bin",
		"REPAIRFLOW_PREFIX=" + prefix,
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("install-repairflow: %v (output %q)", err, out.String())
	}
	return out.String()
}

func TestInstallSharesUpdateLock(t *testing.T) {
	script, root, home, prefix, _ := setupInstall(t)
	bin := filepath.Join(prefix, "bin", "repairflow")
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFake(t, bin, "#!/bin/sh\necho old\n")
	lock, err := os.OpenFile(bin+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer testutil.Close(t, lock)
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	defer func() { testutil.Must(t, unix.Flock(int(lock.Fd()), unix.LOCK_UN)) }()
	cmd := exec.Command("sh", script)
	cmd.Env = []string{"HOME=" + home, "PATH=" + filepath.Join(root, "fakebin") + ":/usr/bin:/bin", "REPAIRFLOW_PREFIX=" + prefix}
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		t.Fatalf("installer ignored update lock: %v\n%s", err, output.String())
	case <-time.After(200 * time.Millisecond):
	}
	before, err := os.ReadFile(bin)
	if err != nil || string(before) != "#!/bin/sh\necho old\n" {
		t.Fatalf("locked target was changed: %q %v", before, err)
	}
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("install after unlocking: %v\n%s", err, output.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("installer did not resume after unlocking")
	}
}

func TestInstallHelpDocumentsDefaultPrefix(t *testing.T) {
	script, _, _, _, _ := setupInstall(t)
	cmd := exec.Command("sh", script, "--help")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("help: %v (%s)", err, out)
	}
	for _, want := range []string{"$HOME/.local/bin/repairflow", "root-owned", "REPAIRFLOW_PREFIX"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("help missing %q:\n%s", want, out)
		}
	}
}

func TestInstallReplacesBinaryAndKeepsWorkspace(t *testing.T) {
	script, root, home, prefix, calls := setupInstall(t)
	fakebin := filepath.Join(root, "fakebin")

	out := runInstall(t, script, home, prefix, fakebin)
	bin := filepath.Join(prefix, "bin", "repairflow")
	info, err := os.Stat(bin)
	if err != nil {
		t.Fatalf("installed binary missing: %v", err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("installed binary not executable: %v", info.Mode())
	}
	if !strings.Contains(out, "Installed repairflow test-new") {
		t.Fatalf("install report missing: %q", out)
	}
	if !strings.Contains(out, "No workspace yet") {
		t.Fatalf("fresh install must not fabricate a workspace: %q", out)
	}
	if _, err := os.Stat(bin + ".new"); !os.IsNotExist(err) {
		t.Fatalf("staged .new file left behind: %v", err)
	}

	// Update path: existing workspace config and a previously installed binary.
	workdir := filepath.Join(home, ".repairflow")
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	if err := os.WriteFile(filepath.Join(workdir, config.ConfigName), []byte(config.Render(cfg)), 0o600); err != nil {
		t.Fatal(err)
	}
	writeFake(t, bin, "#!/bin/sh\necho 'repairflow test-old'\nexit 0\n")

	out = runInstall(t, script, home, prefix, fakebin)
	if !strings.Contains(out, "Previous installation: repairflow test-old") {
		t.Fatalf("previous version report missing: %q", out)
	}
	if !strings.Contains(out, "Workspace "+workdir+" kept") {
		t.Fatalf("workspace keep report missing: %q", out)
	}
	got, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	built, err := os.ReadFile(filepath.Join(root, "built-repairflow"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, built) {
		t.Fatal("installed binary was not replaced with the new build")
	}
	callsText, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(callsText), "init") {
		t.Fatalf("existing workspace must be validated via init: %q", callsText)
	}
}
