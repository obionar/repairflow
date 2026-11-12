package service_test

import (
	"os"
	"path/filepath"
	"testing"

	"repairflow/internal/errs"
	"repairflow/internal/service"
)

// fakeTool puts a tiny shell command first on PATH for this test only.
func fakeTool(t *testing.T, name, body string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func managedUnit(t *testing.T, dir, name, body string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	unitPath := filepath.Join(dir, name)
	if err := os.WriteFile(unitPath, []byte("# managed-by=repairflow\n"+body), 0o644); err != nil {
		t.Fatal(err)
	}
	return unitPath
}

// TestRemoveRefusesRunningUnit: a unit whose service did not stop must not be
// deleted, or the shop keeps serving a process nothing can stop.
func TestRemoveRefusesRunningUnit(t *testing.T) {
	fakeTool(t, "systemctl", "for a in \"$@\"; do [ \"$a\" = is-active ] && { echo active; exit 0; }; done\necho 'Failed to disable unit: access denied' >&2\nexit 1\n")
	unitDir := t.TempDir()
	unitPath := managedUnit(t, unitDir, service.UnitName, "[Unit]\nDescription=RepairFlow\n")

	_, err := service.Remove(service.Options{Init: service.InitSystemd, UnitDir: unitDir})
	if err == nil || errs.CodeOf(err) != errs.CodeConflict {
		t.Fatalf("running unit must refuse removal, got: %v", err)
	}
	if _, statErr := os.Stat(unitPath); statErr != nil {
		t.Fatal("refused removal must leave the unit file")
	}
}

// TestRemoveStoppedUnitSucceedsWithWarning: a stopped unit is removed even when
// the init system reports a disable failure, and the operator sees why.
func TestRemoveStoppedUnitSucceedsWithWarning(t *testing.T) {
	fakeTool(t, "systemctl", "for a in \"$@\"; do [ \"$a\" = is-active ] && { echo inactive; exit 3; }; done\nexit 1\n")
	unitDir := t.TempDir()
	unitPath := managedUnit(t, unitDir, service.UnitName, "[Unit]\nDescription=RepairFlow\n")

	res, err := service.Remove(service.Options{Init: service.InitSystemd, UnitDir: unitDir})
	if err != nil {
		t.Fatalf("stopped unit removal: %v", err)
	}
	if res.Warning == "" {
		t.Fatal("a failed disable must be reported as a warning")
	}
	if _, statErr := os.Stat(unitPath); statErr == nil {
		t.Fatal("stopped unit must be removed")
	}
}

// TestOpenRCRemoveRefusesStartedService mirrors the systemd guard on Alpine.
func TestOpenRCRemoveRefusesStartedService(t *testing.T) {
	fakeTool(t, "rc-service", "case \"$2\" in status) echo '* status: started'; exit 0;; stop) echo 'failed to stop' >&2; exit 1;; esac\nexit 1\n")
	fakeTool(t, "rc-update", "exit 0\n")
	initDir := t.TempDir()
	scriptPath := managedUnit(t, initDir, service.OpenRCName, "#!/sbin/openrc-run\nname=repairflow\n")

	_, err := service.Remove(service.Options{Init: service.InitOpenRC, InitDir: initDir})
	if err == nil || errs.CodeOf(err) != errs.CodeConflict {
		t.Fatalf("started service must refuse removal, got: %v", err)
	}
	if _, statErr := os.Stat(scriptPath); statErr != nil {
		t.Fatal("refused removal must leave the service script")
	}
}
