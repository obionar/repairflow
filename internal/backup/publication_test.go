package backup

import (
	"os"
	"path/filepath"
	"testing"

	"repairflow/internal/errs"
)

func TestLinkFallbackRefusesExistingDestination(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source")
	dest := filepath.Join(dir, "backup.rfbak")
	if err := os.WriteFile(source, []byte("new backup"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := publishByLink(source, dest); errs.CodeOf(err) != errs.CodeConflict {
		t.Fatalf("existing destination: %v", err)
	}
	body, err := os.ReadFile(dest)
	if err != nil || string(body) != "existing" {
		t.Fatalf("existing destination changed: %q, %v", body, err)
	}
	published := filepath.Join(dir, "published.rfbak")
	if err := publishByLink(source, published); err != nil {
		t.Fatalf("link fallback failed: %v", err)
	}
	body, err = os.ReadFile(published)
	if err != nil || string(body) != "new backup" {
		t.Fatalf("published backup: %q, %v", body, err)
	}
}

func TestRestorePublishRefusesLateFileAndRollsBack(t *testing.T) {
	root := t.TempDir()
	staging := filepath.Join(root, "staging")
	dest := filepath.Join(root, "dest")
	for _, dir := range []string{filepath.Join(staging, "data"), dest} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(staging, dbEntryName), []byte("database"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging, cfgEntryName), []byte("new config"), 0o600); err != nil {
		t.Fatal(err)
	}
	// This file arrives after Restore's initial empty-directory check.
	if err := os.WriteFile(filepath.Join(dest, cfgEntryName), []byte("foreign config"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := &Manifest{Config: &FileEntry{Name: cfgEntryName}}
	if err := publish(staging, dest, manifest); errs.CodeOf(err) != errs.CodeConflict {
		t.Fatalf("late destination file: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(dest, cfgEntryName))
	if err != nil || string(body) != "foreign config" {
		t.Fatalf("late destination file changed: %q, %v", body, err)
	}
	if _, err := os.Stat(filepath.Join(dest, dbEntryName)); !os.IsNotExist(err) {
		t.Fatalf("database was not rolled back: %v", err)
	}
}
