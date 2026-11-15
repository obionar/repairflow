package backup_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"repairflow/internal/backup"
)

// A failed or competing backup must never remove a file it did not create.
func TestBackupDoesNotTouchForeignPartFile(t *testing.T) {
	src := newWorkspace(t)
	dest := filepath.Join(t.TempDir(), "kept.rfbak")
	foreign := []byte("belongs to another operation")
	if err := os.WriteFile(dest+".part", foreign, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := backup.Create(context.Background(), src.home, dest, "tester"); err != nil {
		t.Fatalf("backup: %v", err)
	}
	got, err := os.ReadFile(dest + ".part")
	if err != nil {
		t.Fatalf("foreign .part file disappeared: %v", err)
	}
	if !bytes.Equal(got, foreign) {
		t.Fatalf("foreign .part file changed: %q", got)
	}
	if _, err := os.Stat(dest); err != nil {
		t.Fatalf("backup not published: %v", err)
	}
}

// A destination that appears after validation is never overwritten.
func TestBackupDoesNotOverwriteLateDestination(t *testing.T) {
	src := newWorkspace(t)
	dest := filepath.Join(t.TempDir(), "late.rfbak")
	if err := os.WriteFile(dest, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := backup.Create(context.Background(), src.home, dest, "tester"); err == nil {
		t.Fatal("existing destination must be refused")
	}
	got, err := os.ReadFile(dest)
	if err != nil || string(got) != "existing" {
		t.Fatalf("existing destination changed: %q %v", got, err)
	}
}

func TestVerifyRefusesNonDatabaseSnapshot(t *testing.T) {
	payload := []byte("not a SQLite database")
	hash := sha256.Sum256(payload)
	manifest := backup.Manifest{
		FormatVersion: backup.FormatVersion,
		SchemaVersion: 9,
		Database: backup.FileEntry{
			Name: "data/repairflow.db", SizeBytes: int64(len(payload)), SHA256: hex.EncodeToString(hash[:]),
		},
	}
	archive := filepath.Join(t.TempDir(), "bad-db.rfbak")
	f, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	body, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range []struct {
		name string
		body []byte
	}{{"manifest.json", body}, {"data/repairflow.db", payload}} {
		if err := tw.WriteHeader(&tar.Header{Name: entry.name, Mode: 0o600, Size: int64(len(entry.body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(entry.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := backup.Verify(context.Background(), archive); err == nil {
		t.Fatal("checksum-valid but unusable database must fail verification")
	}
}
