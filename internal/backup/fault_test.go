package backup_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"repairflow/internal/backup"
	"repairflow/internal/errs"
)

// Fault: backup destination not writable (simulated disk-full/permission
// failure). Must fail explicitly and leave no partial or final archive.
func TestBackupDestinationUnwritable(t *testing.T) {
	src := newWorkspace(t)
	src.populated(t)
	destDir := filepath.Join(t.TempDir(), "readonly")
	if err := os.MkdirAll(destDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(destDir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(destDir, 0o700)

	dest := filepath.Join(destDir, "backup.rfbak")
	_, err := backup.Create(context.Background(), src.home, dest, "tester")
	if err == nil {
		t.Fatal("unwritable destination must fail the backup")
	}
	if _, statErr := os.Stat(dest); statErr == nil {
		t.Fatal("no archive may exist after a failed backup")
	}
	if _, statErr := os.Stat(dest + ".part"); statErr == nil {
		t.Fatal("no partial archive may remain after a failed backup")
	}
}

// Fault: unreadable attachment bytes (permission change) abort the backup
// with an explicit error instead of producing an incomplete archive.
func TestUnreadableAttachmentAbortsBackup(t *testing.T) {
	src := newWorkspace(t)
	src.populated(t)
	atts, _ := src.svc.ListRepairAttachments(context.Background(), 1, true)
	name, _ := src.svc.AttachmentStorageName(context.Background(), atts[0].ID)
	file := filepath.Join(src.home, "files", "tickets", "1", name)
	if err := os.Chmod(file, 0o000); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(file, 0o600)

	_, err := backup.Create(context.Background(), src.home, filepath.Join(t.TempDir(), "b.rfbak"), "tester")
	if err == nil {
		t.Fatal("unreadable attachment must abort the backup")
	}
}

// Fault: concurrent attachment modifications during listing/backup — the
// service layer must stay consistent (run with -race for full value).
func TestConcurrentAttachmentModifications(t *testing.T) {
	src := newWorkspace(t)
	src.populated(t)
	ctx := context.Background()
	atts, _ := src.svc.ListRepairAttachments(ctx, 1, true)

	var wg sync.WaitGroup
	errsSeen := make(chan error, 32)
	for i := 0; i < 4; i++ {
		wg.Add(3)
		a := atts[i%len(atts)]
		go func() {
			defer wg.Done()
			if _, err := src.svc.SetCaption(ctx, "tester", a.ID, "concurrent caption"); err != nil {
				errsSeen <- err
			}
		}()
		go func() {
			defer wg.Done()
			if _, err := src.svc.Detach(ctx, "tester", a.ID); err != nil {
				// conflicts with concurrent detach are legitimate
				if errs.CodeOf(err) != errs.CodeConflict {
					errsSeen <- err
				}
			}
		}()
		go func() {
			defer wg.Done()
			if _, err := src.svc.ListRepairAttachments(ctx, 1, true); err != nil {
				errsSeen <- err
			}
		}()
	}
	wg.Wait()
	close(errsSeen)
	for err := range errsSeen {
		t.Fatalf("concurrent modification failed: %v", err)
	}

	// A backup during/after concurrent activity must still succeed or fail
	// cleanly — never produce a silently inconsistent archive.
	if _, err := backup.Create(context.Background(), src.home, filepath.Join(t.TempDir(), "c.rfbak"), "tester"); err != nil {
		t.Fatalf("backup after concurrent modifications: %v", err)
	}
}

// The backup never includes disposable content (cache/run/logs/backups).
func TestBackupExcludesDisposableContent(t *testing.T) {
	src := newWorkspace(t)
	src.populated(t)
	os.WriteFile(filepath.Join(src.home, "cache", "thumb.bin"), []byte("regenerable"), 0o600)
	os.WriteFile(filepath.Join(src.home, "run", "upload-old.part"), []byte("stale"), 0o600)
	os.WriteFile(filepath.Join(src.home, "logs", "app.log"), []byte("noise"), 0o600)

	archive := filepath.Join(t.TempDir(), "lean.rfbak")
	res, err := backup.Create(context.Background(), src.home, archive, "tester")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(archive)
	for _, banned := range [][]byte{[]byte("regenerable"), []byte("stale"), []byte("noise")} {
		if bytes.Contains(data, banned) {
			t.Fatalf("backup contains disposable content %q", banned)
		}
	}
	_ = res
}
