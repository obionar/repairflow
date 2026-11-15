package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeP15 puts a stand-in p15 first on PATH so the CLI label commands are
// exercised through the real driver path without a printer.
func fakeP15(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" >> " + argsFile + "\n"
	if err := os.WriteFile(filepath.Join(dir, "p15"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return argsFile
}

// TestPrinterCommandsAndRepairLabel drives the agent-facing path: select the
// printer, print a test label and print a repair sticker, all from the CLI.
func TestPrinterCommandsAndRepairLabel(t *testing.T) {
	home := filepath.Join(t.TempDir(), "ws")
	env := map[string]string{"USER": "tester"}
	base := []string{"--home", home, "--json"}
	argsFile := fakeP15(t)

	if res := runEnv(t, env, append([]string{"init", "--demo"}, base...)...); res.code != 0 {
		t.Fatalf("init: %+v", res)
	}

	// Printing without a selected printer is a validation error, not a print.
	res := runEnv(t, env, append([]string{"repair", "label", "1"}, base...)...)
	if res.code != 2 || !strings.Contains(res.stderr, "no label printer is selected") {
		t.Fatalf("label without printer: exit %d stderr %s", res.code, res.stderr)
	}

	if res := runEnv(t, env, append([]string{"printer", "set", "p15"}, base...)...); res.code != 0 {
		t.Fatalf("printer set: %+v", res)
	}
	if got := dataOf(t, runEnv(t, env, append([]string{"printer", "show"}, base...)...))["driver"]; got != "p15" {
		t.Fatalf("printer show driver = %v, want p15", got)
	}

	res = runEnv(t, env, append([]string{"printer", "test"}, base...)...)
	if res.code != 0 {
		t.Fatalf("printer test: %+v", res)
	}

	res = runEnv(t, env, append([]string{"repair", "label", "1"}, base...)...)
	if res.code != 0 {
		t.Fatalf("repair label: %+v", res)
	}
	data := dataOf(t, res)
	if data["repair_id"] != float64(1) || data["printer"] != "p15" || data["customer_name"] == "" {
		t.Fatalf("label data = %+v", data)
	}

	// The driver received the test label and the repair sticker.
	printed, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	jobs := string(printed)
	if !strings.Contains(jobs, "text\nRepairFlow test") {
		t.Fatalf("test label job missing from driver arguments:\n%s", jobs)
	}
	if !strings.Contains(jobs, "repair\n1\n") {
		t.Fatalf("repair sticker job missing from driver arguments:\n%s", jobs)
	}

	// "none" disables printing again.
	if res := runEnv(t, env, append([]string{"printer", "set", "none"}, base...)...); res.code != 0 {
		t.Fatalf("printer set none: %+v", res)
	}
	if got := dataOf(t, runEnv(t, env, append([]string{"printer", "show"}, base...)...))["driver"]; got != "" {
		t.Fatalf("printer show driver = %v, want off", got)
	}
	if res := runEnv(t, env, append([]string{"printer", "set", "hp2000"}, base...)...); res.code != 2 {
		t.Fatalf("unknown printer: exit %d, want 2", res.code)
	}
}
