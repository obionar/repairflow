package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The license step runs two commands; a failure in the first must fail the
// gate even when the second command succeeds.
func TestVerifyAllPropagatesLicenseTestFailure(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "scripts", "verify-all.sh"))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "scripts", "verify-all.sh"), src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "dist"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "dist", "SYMBOLS.md"), []byte("synthetic\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fakebin := filepath.Join(root, "fakebin")
	if err := os.MkdirAll(fakebin, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"go", "make", "gofmt", "go-licenses", "staticcheck", "govulncheck"} {
		writeFakeTool(t, filepath.Join(fakebin, name), "exit 0\n")
	}
	// License unit tests fail; the inventory command alone would succeed.
	writeFakeTool(t, filepath.Join(fakebin, "python3"),
		"if [ \"${1:-}\" = \"-m\" ]; then echo 'injected license test failure' >&2; exit 1; fi\nexit 0\n")

	cmd := exec.Command("bash", "scripts/verify-all.sh")
	cmd.Dir = root
	cmd.Env = []string{
		"HOME=" + root,
		"PATH=" + fakebin + ":/usr/bin:/bin",
	}
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("gate reported success after a failing license test:\n%s", out)
	}
	if !strings.Contains(string(out), "license inventory") || !strings.Contains(string(out), "FAILED") {
		t.Fatalf("failure was not attributed to the license step:\n%s", out)
	}
}

func writeFakeTool(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
}
