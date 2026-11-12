package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// workspace makes the temp directory look like an initialised workspace, which
// `service install` now requires before it writes an init unit.
func initWorkspace(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("config_version = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestServiceInstallOpenRCThroughCLI exercises the flag plumbing: --init openrc
// writes the OpenRC script wherever REPAIRFLOW_INIT_DIR points, and status/remove
// follow the same target.
func TestServiceInstallOpenRCThroughCLI(t *testing.T) {
	home := initWorkspace(t, t.TempDir())
	initDir := filepath.Join(t.TempDir(), "init.d")
	env := map[string]string{"REPAIRFLOW_INIT_DIR": initDir}

	res := run(t, env, "service", "install", "--init", "openrc", "--no-enable", "--home", home, "--json")
	if res.code != 0 {
		t.Fatalf("exit %d, stderr %s", res.code, res.stderr)
	}
	data, _ := decode(t, res.stdout)["data"].(map[string]any)
	want := filepath.Join(initDir, "repairflow")
	if data["unit_path"] != want || data["present"] != true {
		t.Fatalf("install data: %v", data)
	}
	body, err := os.ReadFile(want)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"#!/sbin/openrc-run",
		`command_args="serve --home ` + home + `"`,
		"command_user=",
		`pidfile="/run/${RC_SVCNAME}.pid"`,
		`output_log="` + filepath.Join(home, "logs", "service.log") + `"`,
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("script missing %q:\n%s", want, body)
		}
	}

	res = run(t, env, "service", "status", "--init", "openrc", "--json")
	data, _ = decode(t, res.stdout)["data"].(map[string]any)
	if res.code != 0 || data["present"] != true {
		t.Fatalf("status: code=%d %v %s", res.code, data, res.stderr)
	}

	res = run(t, env, "service", "remove", "--init", "openrc", "--json")
	data, _ = decode(t, res.stdout)["data"].(map[string]any)
	if res.code != 0 || data["present"] != false {
		t.Fatalf("remove: code=%d %v %s", res.code, data, res.stderr)
	}
	if _, err := os.Stat(want); !os.IsNotExist(err) {
		t.Fatal("script must be gone after remove")
	}
}

// TestServiceInstallSystemdJSONUnchanged pins the systemd envelope: the OpenRC
// work must not add fields to the documented service output.
func TestServiceInstallSystemdJSONUnchanged(t *testing.T) {
	home := initWorkspace(t, t.TempDir())
	unitDir := filepath.Join(t.TempDir(), "units")
	res := run(t, map[string]string{"REPAIRFLOW_UNIT_DIR": unitDir},
		"service", "install", "--init", "systemd", "--no-enable", "--home", home, "--json")
	if res.code != 0 {
		t.Fatalf("exit %d, stderr %s", res.code, res.stderr)
	}
	data, _ := decode(t, res.stdout)["data"].(map[string]any)
	got := []string{}
	for key := range data {
		got = append(got, key)
	}
	for _, key := range got {
		switch key {
		case "unit_path", "present", "active", "enabled", "warning":
		default:
			t.Errorf("unexpected service field %q in %v", key, data)
		}
	}
	if data["present"] != true || data["enabled"] != false {
		t.Fatalf("install data: %v", data)
	}
	if !strings.Contains(res.stdout, `"contract":2`) {
		t.Errorf("contract version changed: %s", res.stdout)
	}
}

func TestServiceUnknownInitIsUsageError(t *testing.T) {
	res := run(t, nil, "service", "install", "--init", "runit", "--home", t.TempDir(), "--json")
	if res.code != 2 {
		t.Fatalf("exit %d, want 2 (stderr %s)", res.code, res.stderr)
	}
	body, _ := decode(t, res.stderr)["error"].(map[string]any)
	if body["code"] != "usage_error" {
		t.Fatalf("error body: %v", body)
	}
	if res.stdout != "" {
		t.Errorf("failures must leave stdout empty, got %q", res.stdout)
	}
}

func TestServiceTextOutputShowsCommand(t *testing.T) {
	home := initWorkspace(t, t.TempDir())
	initDir := filepath.Join(t.TempDir(), "init.d")
	if err := os.MkdirAll(initDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(initDir, 0o755) })
	if os.Geteuid() == 0 {
		t.Skip("running as root: the init directory is writable")
	}
	res := run(t, map[string]string{"REPAIRFLOW_INIT_DIR": initDir},
		"service", "install", "--init", "openrc", "--home", home)
	if res.code != 0 {
		t.Fatalf("exit %d, stderr %s", res.code, res.stderr)
	}
	for _, want := range []string{"warning:", "staged:", "command:", "install -m 0755"} {
		if !strings.Contains(res.stdout, want) {
			t.Errorf("text output missing %q:\n%s", want, res.stdout)
		}
	}
}

// TestServiceInstallRefusesMissingWorkspace is the regression guard for the
// Alpine outage: under doas the default workspace resolved to /root/.repairflow,
// the installer wrote a unit anyway and the service failed at start.
func TestServiceInstallRefusesMissingWorkspace(t *testing.T) {
	home := filepath.Join(t.TempDir(), "absent")
	initDir := filepath.Join(t.TempDir(), "init.d")
	res := run(t, map[string]string{"REPAIRFLOW_INIT_DIR": initDir},
		"service", "install", "--init", "openrc", "--no-enable", "--home", home, "--json")
	if res.code != 2 {
		t.Fatalf("exit %d, want 2 (stderr %s)", res.code, res.stderr)
	}
	body, _ := decode(t, res.stderr)["error"].(map[string]any)
	if body["code"] != "validation_error" || !strings.Contains(body["message"].(string), "no RepairFlow workspace") {
		t.Fatalf("error body: %v", body)
	}
	if _, err := os.Stat(filepath.Join(initDir, "repairflow")); !os.IsNotExist(err) {
		t.Fatal("a missing workspace must not produce an init script")
	}
}

// TestServiceNamedInstancesThroughCLI covers --name plumbing: two instances in
// one unit directory, independent status and removal, and the default name when
// the flag is absent.
func TestServiceNamedInstancesThroughCLI(t *testing.T) {
	unitDir := filepath.Join(t.TempDir(), "units")
	env := map[string]string{"REPAIRFLOW_UNIT_DIR": unitDir}
	homes := map[string]string{}
	for _, name := range []string{"alpha", "beta"} {
		home := initWorkspace(t, t.TempDir())
		homes[name] = home
		res := run(t, env, "service", "install", "--init", "systemd", "--no-enable",
			"--name", name, "--home", home, "--json")
		if res.code != 0 {
			t.Fatalf("install %s: exit %d, stderr %s", name, res.code, res.stderr)
		}
		data, _ := decode(t, res.stdout)["data"].(map[string]any)
		if want := filepath.Join(unitDir, name+".service"); data["unit_path"] != want {
			t.Fatalf("install %s unit_path = %v, want %q", name, data["unit_path"], want)
		}
		if data["present"] != true {
			t.Fatalf("install %s data: %v", name, data)
		}
	}

	res := run(t, env, "service", "status", "--init", "systemd", "--name", "alpha", "--json")
	data, _ := decode(t, res.stdout)["data"].(map[string]any)
	if res.code != 0 || data["present"] != true {
		t.Fatalf("status alpha: code=%d %v %s", res.code, data, res.stderr)
	}

	res = run(t, env, "service", "remove", "--init", "systemd", "--name", "alpha", "--json")
	data, _ = decode(t, res.stdout)["data"].(map[string]any)
	if res.code != 0 || data["present"] != false {
		t.Fatalf("remove alpha: code=%d %v %s", res.code, data, res.stderr)
	}
	if _, err := os.Stat(filepath.Join(unitDir, "alpha.service")); !os.IsNotExist(err) {
		t.Fatal("alpha.service must be gone")
	}
	res = run(t, env, "service", "status", "--init", "systemd", "--name", "beta", "--json")
	data, _ = decode(t, res.stdout)["data"].(map[string]any)
	if res.code != 0 || data["present"] != true {
		t.Fatalf("beta must survive: code=%d %v %s", res.code, data, res.stderr)
	}
	body, err := os.ReadFile(filepath.Join(unitDir, "beta.service"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "serve --home "+homes["beta"]) {
		t.Errorf("beta.service lost its workspace:\n%s", body)
	}

	// No --name means the default instance keeps its original file name.
	res = run(t, env, "service", "install", "--init", "systemd", "--no-enable",
		"--home", homes["alpha"], "--json")
	if res.code != 0 {
		t.Fatalf("default install: exit %d, stderr %s", res.code, res.stderr)
	}
	data, _ = decode(t, res.stdout)["data"].(map[string]any)
	if want := filepath.Join(unitDir, "repairflow.service"); data["unit_path"] != want {
		t.Fatalf("default unit_path = %v, want %q", data["unit_path"], want)
	}
}

// TestServiceInvalidNameIsUsageError keeps a crafted --name from becoming a
// path in the unit or init directory.
func TestServiceInvalidNameIsUsageError(t *testing.T) {
	home := initWorkspace(t, t.TempDir())
	env := map[string]string{
		"REPAIRFLOW_UNIT_DIR": filepath.Join(t.TempDir(), "units"),
		"REPAIRFLOW_INIT_DIR": filepath.Join(t.TempDir(), "init.d"),
	}
	for _, name := range []string{"Alpha", "../escape", "repair_flow"} {
		for _, sub := range []string{"install", "status", "remove"} {
			res := run(t, env, "service", sub, "--init", "systemd", "--name", name, "--home", home, "--json")
			if res.code != 2 {
				t.Fatalf("%s --name %q: exit %d, want 2 (stderr %s)", sub, name, res.code, res.stderr)
			}
			body, _ := decode(t, res.stderr)["error"].(map[string]any)
			if body["code"] != "usage_error" {
				t.Fatalf("%s --name %q error body: %v", sub, name, body)
			}
			if res.stdout != "" {
				t.Errorf("%s --name %q must leave stdout empty, got %q", sub, name, res.stdout)
			}
		}
	}
}
