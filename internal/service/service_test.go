package service_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"repairflow/internal/errs"
	"repairflow/internal/service"
)

// systemdUnitGolden is the v0.1 unit text. It must stay byte-identical: hosts
// upgrading from a previous release compare the installed unit.
const systemdUnitGolden = `# managed-by=repairflow
[Unit]
Description=RepairFlow local repair workspace
After=default.target

[Service]
Type=simple
ExecStart=/opt/repairflow/repairflow serve --home /home/u/.repairflow
WorkingDirectory=/home/u/.repairflow
Restart=on-failure
RestartSec=5
NoNewPrivileges=true
ProtectSystem=strict
PrivateTmp=true
ReadWritePaths=/home/u/.repairflow

[Install]
WantedBy=default.target
`

func TestInstallWritesLeastPrivilegeUnit(t *testing.T) {
	unitDir := filepath.Join(t.TempDir(), "units")
	res, err := service.Install(service.Options{
		BinaryPath: "/opt/repairflow/repairflow",
		Home:       "/home/u/.repairflow",
		Init:       service.InitSystemd,
		UnitDir:    unitDir,
	})
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if !res.Present {
		t.Fatalf("result: %+v", res)
	}
	body, err := os.ReadFile(res.UnitPath)
	if err != nil {
		t.Fatal(err)
	}
	unit := string(body)
	for _, want := range []string{
		"managed-by=repairflow",
		"ExecStart=/opt/repairflow/repairflow serve --home /home/u/.repairflow",
		"NoNewPrivileges=true", "ProtectSystem=strict", "ReadWritePaths=/home/u/.repairflow",
		"WantedBy=default.target",
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("unit missing %q:\n%s", want, unit)
		}
	}
	// Quoting for paths with spaces.
	res2, err := service.Install(service.Options{
		BinaryPath: "/opt/my tools/repairflow",
		Home:       "/home/u/my data",
		Init:       service.InitSystemd,
		UnitDir:    unitDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	body2, _ := os.ReadFile(res2.UnitPath)
	if !strings.Contains(string(body2), `ExecStart="/opt/my tools/repairflow" serve --home "/home/u/my data"`) {
		t.Errorf("spaces not quoted:\n%s", body2)
	}
}

// TestSystemdUnitIsByteIdentical pins the unit text so OpenRC work cannot drift
// the systemd output.
func TestSystemdUnitIsByteIdentical(t *testing.T) {
	unitDir := t.TempDir()
	res, err := service.Install(service.Options{
		BinaryPath: "/opt/repairflow/repairflow",
		Home:       "/home/u/.repairflow",
		Init:       service.InitSystemd,
		UnitDir:    unitDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(res.UnitPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != systemdUnitGolden {
		t.Errorf("systemd unit changed:\n--- got ---\n%s\n--- want ---\n%s", body, systemdUnitGolden)
	}
	info, err := os.Stat(res.UnitPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Errorf("unit mode = %o, want 644", info.Mode().Perm())
	}
}

func TestInstallWarnsOnUnstableBinaryPath(t *testing.T) {
	unitDir := filepath.Join(t.TempDir(), "units")
	cases := []struct {
		binary string
		want   string
	}{
		{filepath.Join(os.TempDir(), "repairflow"), "temporary directory"},
		{"/home/u/Downloads/repairflow", "download/desktop folder"},
		{"/home/u/Desktop/repairflow", "download/desktop folder"},
		{"relative/repairflow", "non-absolute"},
		{"/opt/repairflow/repairflow", ""},
	}
	for _, tc := range cases {
		res, err := service.Install(service.Options{
			BinaryPath: tc.binary,
			Home:       "/home/u/ws",
			Init:       service.InitSystemd,
			UnitDir:    unitDir,
		})
		if err != nil {
			t.Fatalf("install %q: %v", tc.binary, err)
		}
		if !res.Present {
			t.Fatalf("install %q did not report the unit: %+v", tc.binary, res)
		}
		if tc.want == "" {
			if strings.Contains(res.Warning, "unit pins") {
				t.Errorf("unexpected path warning for %q: %q", tc.binary, res.Warning)
			}
			continue
		}
		if !strings.Contains(res.Warning, tc.want) {
			t.Errorf("warning for %q = %q, want %q", tc.binary, res.Warning, tc.want)
		}
	}
}

func TestStatusAndRemoveLifecycle(t *testing.T) {
	unitDir := filepath.Join(t.TempDir(), "units")
	opts := service.Options{Init: service.InitSystemd, UnitDir: unitDir}

	// Status of a clean directory: not present, no error.
	st, err := service.Status(opts)
	if err != nil || st.Present {
		t.Fatalf("clean status: %+v %v", st, err)
	}
	// Removing nothing is graceful.
	res, err := service.Remove(opts)
	if err != nil || res.Present {
		t.Fatalf("remove without unit: %+v %v", res, err)
	}

	install := opts
	install.BinaryPath = "/bin/repairflow"
	install.Home = "/ws"
	if _, err := service.Install(install); err != nil {
		t.Fatal(err)
	}
	st, err = service.Status(opts)
	if err != nil || !st.Present {
		t.Fatalf("status after install: %+v %v", st, err)
	}
	res, err = service.Remove(opts)
	if err != nil || res.Present {
		t.Fatalf("remove: %+v %v", res, err)
	}
	if _, err := os.Stat(filepath.Join(unitDir, service.UnitName)); err == nil {
		t.Fatal("unit file must be gone")
	}
}

func TestRemoveRefusesForeignUnit(t *testing.T) {
	unitDir := filepath.Join(t.TempDir(), "units")
	if err := os.MkdirAll(unitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	unitPath := filepath.Join(unitDir, service.UnitName)
	if err := os.WriteFile(unitPath, []byte("[Unit]\nDescription=not ours\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := service.Remove(service.Options{Init: service.InitSystemd, UnitDir: unitDir})
	if err == nil || errs.CodeOf(err) != errs.CodeConflict {
		t.Fatalf("foreign unit must be refused: %v", err)
	}
	if _, statErr := os.Stat(unitPath); statErr != nil {
		t.Fatal("refused removal must leave the file")
	}
}

// TestOpenRCInstallWritesScript covers the generated Alpine service script: the
// command, its arguments, the account it drops to, the pidfile and the logs.
func TestOpenRCInstallWritesScript(t *testing.T) {
	initDir := filepath.Join(t.TempDir(), "init.d")
	home := filepath.Join(t.TempDir(), "ws")
	logPath := filepath.Join(home, "logs", "service.log")
	res, err := service.Install(service.Options{
		BinaryPath: "/home/u/bin/repairflow",
		Home:       home,
		Init:       service.InitOpenRC,
		InitDir:    initDir,
		User:       "u:u",
	})
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if !res.Present || res.Command != "" || res.StagedPath != "" {
		t.Fatalf("privileged install result: %+v", res)
	}
	if res.UnitPath != filepath.Join(initDir, service.OpenRCName) {
		t.Fatalf("unit path = %q", res.UnitPath)
	}
	body, err := os.ReadFile(res.UnitPath)
	if err != nil {
		t.Fatal(err)
	}
	script := string(body)
	for _, want := range []string{
		"#!/sbin/openrc-run",
		"# managed-by=repairflow",
		`name="repairflow"`,
		`command="/home/u/bin/repairflow"`,
		`command_args="serve --home ` + home + `"`,
		`command_user="u:u"`,
		`command_background="yes"`,
		`pidfile="/run/${RC_SVCNAME}.pid"`,
		`output_log="` + logPath + `"`,
		`error_log="` + logPath + `"`,
		`checkpath --file --owner "u:u" --mode 0600 "${output_log}"`,
		"need net",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("script missing %q:\n%s", want, script)
		}
	}
	info, err := os.Stat(res.UnitPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Errorf("script mode = %o, want 755", info.Mode().Perm())
	}
	// The generated script must be valid shell for the OpenRC interpreter.
	if out, err := exec.Command("sh", "-n", res.UnitPath).CombinedOutput(); err != nil {
		t.Fatalf("generated script does not parse: %v\n%s", err, out)
	}
	if !strings.Contains(res.Warning, "skipped") {
		t.Errorf("no enable requested must warn, got %q", res.Warning)
	}
}

// TestOpenRCInstallDerivesCommandUser proves the account comes from the
// workspace owner rather than from the invoking (possibly root) user.
func TestOpenRCInstallDerivesCommandUser(t *testing.T) {
	initDir := filepath.Join(t.TempDir(), "init.d")
	home := t.TempDir()
	res, err := service.Install(service.Options{
		BinaryPath: "/home/u/bin/repairflow",
		Home:       home,
		Init:       service.InitOpenRC,
		InitDir:    initDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(res.UnitPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "command_user=") {
		t.Fatalf("command_user missing:\n%s", body)
	}
	if !strings.Contains(string(body), `checkpath --file --owner "`) {
		t.Errorf("start_pre must own the log file:\n%s", body)
	}
}

// TestOpenRCUnprivilegedStagesAndPrintsCommand covers the non-root run: the
// script is staged readably and the exact privileged commands are printed.
func TestOpenRCUnprivilegedStagesAndPrintsCommand(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: the init directory is writable")
	}
	initDir := filepath.Join(t.TempDir(), "init.d")
	if err := os.MkdirAll(initDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(initDir, 0o755) })
	home := t.TempDir()
	res, err := service.Install(service.Options{
		BinaryPath: "/home/u/bin/repairflow",
		Home:       home,
		Init:       service.InitOpenRC,
		InitDir:    initDir,
		User:       "u:u",
		Enable:     true,
	})
	if err != nil {
		t.Fatalf("install without privileges must not fail: %v", err)
	}
	if res.Present {
		t.Fatalf("script must not be reported as installed: %+v", res)
	}
	staged := filepath.Join(home, "cache", "service", "repairflow.initd")
	if res.StagedPath != staged {
		t.Fatalf("staged path = %q, want %q", res.StagedPath, staged)
	}
	body, err := os.ReadFile(staged)
	if err != nil {
		t.Fatalf("staged script unreadable: %v", err)
	}
	if !strings.HasPrefix(string(body), "#!/sbin/openrc-run\n") {
		t.Errorf("staged script is not the unit:\n%s", body)
	}
	target := filepath.Join(initDir, service.OpenRCName)
	for _, want := range []string{
		"install -m 0755 " + staged,
		target,
		"rc-update add repairflow default",
		"rc-service repairflow start",
	} {
		if !strings.Contains(res.Command, want) {
			t.Errorf("command %q missing %q", res.Command, want)
		}
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("nothing must be written into the unwritable init directory")
	}
	if !strings.Contains(res.Warning, "no privileges") {
		t.Errorf("warning must explain the missing privileges: %q", res.Warning)
	}
}

func TestOpenRCStatusAndRemove(t *testing.T) {
	initDir := filepath.Join(t.TempDir(), "init.d")
	opts := service.Options{Init: service.InitOpenRC, InitDir: initDir, User: "u:u"}

	st, err := service.Status(opts)
	if err != nil || st.Present {
		t.Fatalf("clean status: %+v %v", st, err)
	}
	res, err := service.Remove(opts)
	if err != nil || res.Present {
		t.Fatalf("remove without script: %+v %v", res, err)
	}

	install := opts
	install.BinaryPath = "/home/u/bin/repairflow"
	install.Home = t.TempDir()
	if _, err := service.Install(install); err != nil {
		t.Fatal(err)
	}
	st, err = service.Status(opts)
	if err != nil || !st.Present {
		t.Fatalf("status after install: %+v %v", st, err)
	}
	if res, err = service.Remove(opts); err != nil || res.Present {
		t.Fatalf("remove: %+v %v", res, err)
	}
	if _, err := os.Stat(filepath.Join(initDir, service.OpenRCName)); !os.IsNotExist(err) {
		t.Fatal("script must be gone")
	}
}

func TestOpenRCRemoveRefusesForeignScript(t *testing.T) {
	initDir := filepath.Join(t.TempDir(), "init.d")
	if err := os.MkdirAll(initDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(initDir, service.OpenRCName)
	if err := os.WriteFile(path, []byte("#!/sbin/openrc-run\n# hand written\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := service.Remove(service.Options{Init: service.InitOpenRC, InitDir: initDir})
	if err == nil || errs.CodeOf(err) != errs.CodeConflict {
		t.Fatalf("foreign script must be refused: %v", err)
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Fatal("refused removal must leave the script")
	}
}

func TestInstallNoneWritesNothing(t *testing.T) {
	home := t.TempDir()
	res, err := service.Install(service.Options{
		BinaryPath: "/home/u/bin/repairflow",
		Home:       home,
		Init:       service.InitNone,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Present || res.UnitPath != "" {
		t.Fatalf("--init none must write nothing: %+v", res)
	}
	if !strings.Contains(res.Command, "serve --home "+home) {
		t.Errorf("command must show how to run it: %q", res.Command)
	}
	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("--init none created files: %v", entries)
	}
}

func TestParseInit(t *testing.T) {
	cases := map[string]service.Init{
		"":        "",
		"systemd": service.InitSystemd,
		"openrc":  service.InitOpenRC,
		"none":    service.InitNone,
		" OpenRC": service.InitOpenRC,
	}
	for in, want := range cases {
		got, err := service.ParseInit(in)
		if err != nil || got != want {
			t.Errorf("ParseInit(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := service.ParseInit("runit"); err == nil || errs.CodeOf(err) != errs.CodeUsage {
		t.Fatalf("unknown init must be a usage error: %v", err)
	}
}

// TestOpenRCWarnsOnWorkspaceWithSpaces documents the one combination the OpenRC
// script cannot express (command_args is whitespace-split).
func TestOpenRCWarnsOnWorkspaceWithSpaces(t *testing.T) {
	initDir := filepath.Join(t.TempDir(), "init.d")
	res, err := service.Install(service.Options{
		BinaryPath: "/home/u/bin/repairflow",
		Home:       filepath.Join(t.TempDir(), "my data"),
		Init:       service.InitOpenRC,
		InitDir:    initDir,
		User:       "u:u",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Warning, "whitespace") {
		t.Errorf("want a whitespace warning, got %q", res.Warning)
	}
}

// TestParseName pins the instance-name grammar: a lowercase letter followed by
// up to 30 lowercase letters, digits or dashes. Empty selects the default.
func TestParseName(t *testing.T) {
	valid := map[string]string{
		"":        service.DefaultName,
		"a":       "a",
		"alpha-1": "alpha-1",
		"shop2":   "shop2",
	}
	valid["repairflow"] = "repairflow"
	valid[strings.Repeat("a", 31)] = strings.Repeat("a", 31)
	for in, want := range valid {
		got, err := service.ParseName(in)
		if err != nil || got != want {
			t.Errorf("ParseName(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	invalid := []string{
		"A", "1alpha", "-alpha", "alpha_beta", "alpha.beta", "alpha beta",
		strings.Repeat("a", 32), "réparation",
	}
	for _, in := range invalid {
		if got, err := service.ParseName(in); err == nil || errs.CodeOf(err) != errs.CodeUsage {
			t.Errorf("ParseName(%q) = %q, %v; want a usage error", in, got, err)
		}
	}
}

// TestInstallNamedSystemdInstance proves a custom name gets its own file, its
// own Description and a marker that records the service, while the default unit
// stays byte-identical to the golden.
func TestInstallNamedSystemdInstance(t *testing.T) {
	unitDir := filepath.Join(t.TempDir(), "units")
	base := service.Options{
		BinaryPath: "/opt/repairflow/repairflow",
		Home:       "/home/u/.repairflow",
		Init:       service.InitSystemd,
		UnitDir:    unitDir,
	}
	if _, err := service.Install(base); err != nil {
		t.Fatal(err)
	}
	named := base
	named.Name = "alpha"
	named.Home = "/home/u/.repairflow-alpha"
	res, err := service.Install(named)
	if err != nil {
		t.Fatalf("named install: %v", err)
	}
	wantPath := filepath.Join(unitDir, "alpha.service")
	if res.UnitPath != wantPath || !res.Present {
		t.Fatalf("named result: %+v", res)
	}
	body, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatal(err)
	}
	unit := string(body)
	for _, want := range []string{
		"# managed-by=repairflow service=alpha",
		"Description=RepairFlow alpha local repair workspace",
		"ExecStart=/opt/repairflow/repairflow serve --home /home/u/.repairflow-alpha",
		"ReadWritePaths=/home/u/.repairflow-alpha",
		"WantedBy=default.target",
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("named unit missing %q:\n%s", want, unit)
		}
	}
	defaultBody, err := os.ReadFile(filepath.Join(unitDir, service.UnitName))
	if err != nil {
		t.Fatal(err)
	}
	if string(defaultBody) != systemdUnitGolden {
		t.Errorf("default unit changed:\n--- got ---\n%s\n--- want ---\n%s", defaultBody, systemdUnitGolden)
	}
}

// TestNamedSystemdInstancesAreIndependent installs two custom instances in one
// unit directory, then removes one and checks the other survived.
func TestNamedSystemdInstancesAreIndependent(t *testing.T) {
	unitDir := filepath.Join(t.TempDir(), "units")
	homes := map[string]string{}
	for _, name := range []string{"alpha", "beta"} {
		home := filepath.Join(t.TempDir(), name)
		homes[name] = home
		if _, err := service.Install(service.Options{
			Name:       name,
			BinaryPath: "/opt/repairflow/repairflow",
			Home:       home,
			Init:       service.InitSystemd,
			UnitDir:    unitDir,
		}); err != nil {
			t.Fatalf("install %s: %v", name, err)
		}
	}
	for name, home := range homes {
		st, err := service.Status(service.Options{Name: name, Init: service.InitSystemd, UnitDir: unitDir})
		if err != nil || !st.Present {
			t.Fatalf("status %s: %+v %v", name, st, err)
		}
		if st.UnitPath != filepath.Join(unitDir, name+".service") {
			t.Errorf("status %s path = %q", name, st.UnitPath)
		}
		body, err := os.ReadFile(st.UnitPath)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), "serve --home "+home) {
			t.Errorf("unit %s does not belong to its workspace %s:\n%s", name, home, body)
		}
	}
	if _, err := service.Remove(service.Options{Name: "alpha", Init: service.InitSystemd, UnitDir: unitDir}); err != nil {
		t.Fatalf("remove alpha: %v", err)
	}
	if _, err := os.Stat(filepath.Join(unitDir, "alpha.service")); !os.IsNotExist(err) {
		t.Fatal("alpha.service must be gone")
	}
	st, err := service.Status(service.Options{Name: "beta", Init: service.InitSystemd, UnitDir: unitDir})
	if err != nil || !st.Present {
		t.Fatalf("beta must survive: %+v %v", st, err)
	}
	if _, err := os.Stat(filepath.Join(unitDir, "beta.service")); err != nil {
		t.Fatalf("beta.service must survive: %v", err)
	}
}

// TestOpenRCNamedInstance covers name= and the script file name for a custom
// instance.
func TestOpenRCNamedInstance(t *testing.T) {
	initDir := filepath.Join(t.TempDir(), "init.d")
	res, err := service.Install(service.Options{
		Name:       "shop-a",
		BinaryPath: "/home/u/bin/repairflow",
		Home:       t.TempDir(),
		Init:       service.InitOpenRC,
		InitDir:    initDir,
		User:       "u:u",
	})
	if err != nil {
		t.Fatal(err)
	}
	wantPath := filepath.Join(initDir, "shop-a")
	if res.UnitPath != wantPath {
		t.Fatalf("unit path = %q, want %q", res.UnitPath, wantPath)
	}
	body, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatal(err)
	}
	script := string(body)
	for _, want := range []string{
		"# managed-by=repairflow service=shop-a",
		`name="shop-a"`,
		`command="/home/u/bin/repairflow"`,
	} {
		if !strings.Contains(script, want) {
			t.Errorf("script missing %q:\n%s", want, script)
		}
	}
	if out, err := exec.Command("sh", "-n", wantPath).CombinedOutput(); err != nil {
		t.Fatalf("generated script does not parse: %v\n%s", err, out)
	}
}

// TestOpenRCNamedStagingPathAndCommands checks the staged file name and the
// privilege instructions use the selected instance name.
func TestOpenRCNamedStagingPathAndCommands(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: the init directory is writable")
	}
	initDir := filepath.Join(t.TempDir(), "init.d")
	if err := os.MkdirAll(initDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(initDir, 0o755) })
	home := t.TempDir()
	res, err := service.Install(service.Options{
		Name:       "beta",
		BinaryPath: "/home/u/bin/repairflow",
		Home:       home,
		Init:       service.InitOpenRC,
		InitDir:    initDir,
		User:       "u:u",
		Enable:     true,
	})
	if err != nil {
		t.Fatalf("install without privileges must not fail: %v", err)
	}
	wantStaged := filepath.Join(home, "cache", "service", "beta.initd")
	if res.StagedPath != wantStaged {
		t.Fatalf("staged path = %q, want %q", res.StagedPath, wantStaged)
	}
	if _, err := os.Stat(wantStaged); err != nil {
		t.Fatalf("staged script missing: %v", err)
	}
	target := filepath.Join(initDir, "beta")
	for _, want := range []string{
		"install -m 0755 " + wantStaged,
		target,
		"rc-update add beta default",
		"rc-service beta start",
	} {
		if !strings.Contains(res.Command, want) {
			t.Errorf("command %q missing %q", res.Command, want)
		}
	}
}

// TestInstallRefusesUnitOwnedByAnotherService: a managed unit whose marker names
// another service is not ours to overwrite.
func TestInstallRefusesUnitOwnedByAnotherService(t *testing.T) {
	unitDir := filepath.Join(t.TempDir(), "units")
	if err := os.MkdirAll(unitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(unitDir, service.UnitName)
	body := "# managed-by=repairflow service=alpha\n[Unit]\nDescription=alpha instance\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := service.Install(service.Options{
		BinaryPath: "/opt/repairflow/repairflow",
		Home:       "/home/u/.repairflow",
		Init:       service.InitSystemd,
		UnitDir:    unitDir,
	})
	if err == nil || errs.CodeOf(err) != errs.CodeConflict {
		t.Fatalf("install over another service's unit must be refused: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != body {
		t.Errorf("refused install changed the unit:\n--- got ---\n%s\n--- want ---\n%s", after, body)
	}
}

// TestRemoveRefusesUnitOwnedByAnotherService checks removal of a managed unit
// that belongs to a different instance, and that the bare marker means the
// default instance rather than "any name".
func TestRemoveRefusesUnitOwnedByAnotherService(t *testing.T) {
	unitDir := filepath.Join(t.TempDir(), "units")
	if err := os.MkdirAll(unitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		body string
		file string
	}{
		{"alpha", "# managed-by=repairflow service=beta\n[Unit]\n", "alpha.service"},
		{"alpha", "# managed-by=repairflow\n[Unit]\n", "alpha.service"},
	}
	for _, tc := range cases {
		path := filepath.Join(unitDir, tc.file)
		if err := os.WriteFile(path, []byte(tc.body), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := service.Remove(service.Options{Name: tc.name, Init: service.InitSystemd, UnitDir: unitDir})
		if err == nil || errs.CodeOf(err) != errs.CodeConflict {
			t.Fatalf("remove %q of %q must conflict: %v", tc.name, tc.body, err)
		}
		after, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(after) != tc.body {
			t.Errorf("refused removal changed %s:\n%s", path, after)
		}
	}
}

// TestOpenRCInstallRefusesScriptOwnedByAnotherService keeps the marker check in
// the OpenRC path too: a script staged for install with a different instance's
// marker is refused and keeps its bytes.
func TestOpenRCInstallRefusesScriptOwnedByAnotherService(t *testing.T) {
	initDir := filepath.Join(t.TempDir(), "init.d")
	if err := os.MkdirAll(initDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(initDir, "alpha")
	body := "#!/sbin/openrc-run\n# managed-by=repairflow service=beta\nname=\"beta\"\n"
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := service.Install(service.Options{
		Name:       "alpha",
		BinaryPath: "/home/u/bin/repairflow",
		Home:       t.TempDir(),
		Init:       service.InitOpenRC,
		InitDir:    initDir,
		User:       "u:u",
	})
	if err == nil || errs.CodeOf(err) != errs.CodeConflict {
		t.Fatalf("install over another service's script must conflict: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != body {
		t.Errorf("refused install changed the script:\n--- got ---\n%s\n--- want ---\n%s", after, body)
	}
}
