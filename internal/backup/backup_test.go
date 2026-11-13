package backup_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"repairflow/internal/app"
	"repairflow/internal/backup"
	"repairflow/internal/db"
	"repairflow/internal/errs"
	"repairflow/internal/files"
	"repairflow/internal/testutil"
)

// ---- helpers -------------------------------------------------------------

type fixture struct {
	home string
	svc  *app.Service
	db   *sql.DB
}

func newWorkspace(t *testing.T) *fixture {
	t.Helper()
	home := filepath.Join(t.TempDir(), "ws")
	home, err := filepath.Abs(home)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := db.Open(context.Background(), db.Path(home))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(context.Background(), handle); err != nil {
		t.Fatal(err)
	}
	svc := app.New(handle).WithStore(files.New(home))
	return &fixture{home: home, svc: svc, db: handle}
}

func (f *fixture) addAttachment(t *testing.T, ticketID, entryID int64, name string, payload []byte) app.Attachment {
	t.Helper()
	a, err := f.svc.AddAttachment(context.Background(), "tester", ticketID, entryID, name, bytes.NewReader(payload), "", "cli")
	if err != nil {
		t.Fatalf("add attachment %q: %v", name, err)
	}
	return a
}

func (f *fixture) populated(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	c, err := f.svc.CreateCustomer(ctx, "tester", app.CustomerInput{Name: "Ada Lövelace 日本語", Phone: "+49 00 1", Notes: "üñîçødé"})
	if err != nil {
		t.Fatal(err)
	}
	tk, err := f.svc.CreateRepair(ctx, "tester", app.RepairInput{CustomerID: c.ID, Device: "Bosch X (съёмник)", Issue: "smoke"})
	if err != nil {
		t.Fatal(err)
	}
	e1, _ := f.svc.AddRepairEntry(ctx, "tester", tk.ID, "work", "entry one")
	e2, _ := f.svc.AddRepairEntry(ctx, "tester", tk.ID, "test", "entry two")
	if _, err := f.svc.AddRepairPart(ctx, "tester", tk.ID, app.PartInput{Description: "Part ü", QuantityMilli: 1500, UnitPriceCents: 999}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.CreateEstimate(ctx, "tester", tk.ID, app.EstimateInput{DiscountKind: "percent", DiscountValue: 10}); err != nil {
		t.Fatal(err)
	}

	// Unicode, spaces, unusual display names; binary payloads incl. NULs.
	f.addAttachment(t, tk.ID, e1.ID, "IMG 2026 ünïcode (1).jpg", []byte{0xff, 0xd8, 0xff, 0x00, 0x01, 0xfe, 0xff})
	f.addAttachment(t, tk.ID, e2.ID, "firmware dump v2.bin", append([]byte{0x7f, 'E', 'L', 'F', 0x00}, bytes.Repeat([]byte{0x00, 0xff}, 512)...))
	// One attachment linked to BOTH entries.
	a3 := f.addAttachment(t, tk.ID, 0, "schematic.pdf", []byte("%PDF-1.7 fake\x00binary"))
	if _, err := f.svc.LinkEntry(ctx, "tester", a3.ID, e1.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.LinkEntry(ctx, "tester", a3.ID, e2.ID); err != nil {
		t.Fatal(err)
	}
}

func treeHash(t *testing.T, root string) string {
	t.Helper()
	h := sha256.New()
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		sum, _, err := hashOf(path)
		if err != nil {
			return err
		}
		h.Write([]byte(rel + "|" + sum + "\n"))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func hashOf(path string) (string, int64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", 0, err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), int64(len(data)), nil
}

func restoredWorkspace(t *testing.T, archive string) *fixture {
	t.Helper()
	dest := filepath.Join(t.TempDir(), "restored-ws")
	if _, err := backup.Restore(context.Background(), archive, dest); err != nil {
		t.Fatalf("restore: %v", err)
	}
	handle, err := db.Open(context.Background(), db.Path(dest))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { testutil.Close(t, handle) })
	return &fixture{home: dest, svc: app.New(handle).WithStore(files.New(dest)), db: handle}
}

// ---- the required verification matrix ------------------------------------

// Scenario 1: backup and restore of an empty workspace.
func TestEmptyWorkspaceRoundtrip(t *testing.T) {
	src := newWorkspace(t)
	archive := filepath.Join(t.TempDir(), "empty.rfbak")
	res, err := backup.Create(context.Background(), src.home, archive, "tester")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if res.Manifest.Attachments != nil {
		t.Fatalf("empty workspace should have no attachments: %+v", res.Manifest)
	}
	dst := restoredWorkspace(t, archive)
	if err := db.IntegrityCheck(context.Background(), dst.db); err != nil {
		t.Fatalf("restored db integrity: %v", err)
	}
}

// Scenarios 2, 4, 5: populated demo workspace; multi-entry links; byte-identical
// attachment roundtrips verified by SHA-256.
func TestPopulatedWorkspaceRoundtrip(t *testing.T) {
	src := newWorkspace(t)
	src.populated(t)

	originals := map[string][]byte{}
	atts, _ := src.svc.ListRepairAttachments(context.Background(), 1, true)
	for _, a := range atts {
		rc, _, err := src.svc.OpenBytes(context.Background(), a.ID)
		if err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		if _, err := buf.ReadFrom(rc); err != nil {
			t.Fatal(err)
		}
		testutil.Close(t, rc)
		originals[a.SHA256] = buf.Bytes()
	}

	archive := filepath.Join(t.TempDir(), "demo.rfbak")
	res, err := backup.Create(context.Background(), src.home, archive, "tester")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if len(res.Manifest.Attachments) != 3 {
		t.Fatalf("manifest attachments: %+v", res.Manifest.Attachments)
	}

	dst := restoredWorkspace(t, archive)
	restored, err := dst.svc.ListRepairAttachments(context.Background(), 1, true)
	if err != nil || len(restored) != 3 {
		t.Fatalf("restored attachments: %v %v", restored, err)
	}
	for _, a := range restored {
		rc, _, err := dst.svc.OpenBytes(context.Background(), a.ID)
		if err != nil {
			t.Fatalf("bytes for %s: %v", a.DisplayName, err)
		}
		var buf bytes.Buffer
		if _, err := buf.ReadFrom(rc); err != nil {
			t.Fatal(err)
		}
		testutil.Close(t, rc)
		sum := sha256.Sum256(buf.Bytes())
		if hex.EncodeToString(sum[:]) != a.SHA256 {
			t.Fatalf("attachment %q not byte-identical after restore", a.DisplayName)
		}
		if !bytes.Equal(originals[a.SHA256], buf.Bytes()) {
			t.Fatalf("attachment %q differs from source bytes", a.DisplayName)
		}
	}

	// Scenario 4: entry links survive; one attachment linked to two entries.
	for _, a := range restored {
		if a.DisplayName == "schematic.pdf" && len(a.EntryIDs) != 2 {
			t.Fatalf("multi-entry link lost: %+v", a)
		}
	}

	// Scenario 10: integrity and foreign-key checks after restore.
	if err := db.IntegrityCheck(context.Background(), dst.db); err != nil {
		t.Fatalf("restored integrity: %v", err)
	}

	// Scenario 11: doctor-equivalent checks healthy after restoration.
	missing, orphans, err := dst.svc.ReconcileAttachments(context.Background())
	if err != nil || len(missing) != 0 || len(orphans) != 0 {
		t.Fatalf("reconcile after restore: %v %v %v", missing, orphans, err)
	}
}

// Scenario 3 is covered by TestPopulatedWorkspaceRoundtrip (Unicode, spaces,
// unusual names and binary content are in the fixture).

// Scenario 6: corrupt or truncated archives are rejected.
func TestCorruptAndTruncatedArchivesRejected(t *testing.T) {
	src := newWorkspace(t)
	src.populated(t)
	good := filepath.Join(t.TempDir(), "good.rfbak")
	if _, err := backup.Create(context.Background(), src.home, good, "tester"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(good)

	// Truncated archive.
	trunc := filepath.Join(t.TempDir(), "trunc.rfbak")
	testutil.Must(t, os.WriteFile(trunc, data[:len(data)/2], 0o600))
	if _, err := backup.Verify(context.Background(), trunc); err == nil {
		t.Fatal("truncated archive must be refused")
	}
	if _, err := backup.Restore(context.Background(), trunc, filepath.Join(t.TempDir(), "ws")); err == nil {
		t.Fatal("restore of truncated archive must be refused")
	}

	// Corrupted payload byte (gzip stream damage).
	corrupt := filepath.Join(t.TempDir(), "corrupt.rfbak")
	bad := append([]byte(nil), data...)
	bad[len(bad)/2] ^= 0xff
	testutil.Must(t, os.WriteFile(corrupt, bad, 0o600))
	if _, err := backup.Verify(context.Background(), corrupt); err == nil {
		t.Fatal("corrupt archive must be refused")
	}

	// Well-formed but tampered: rebuild the archive with modified payload
	// bytes while keeping the original manifest hashes (forged content).
	// This must fail entry hash verification.
	tampered := filepath.Join(t.TempDir(), "tampered.rfbak")
	rebuildTampered(t, good, tampered)
	if _, err := backup.Verify(context.Background(), tampered); err == nil {
		t.Fatal("tampered archive must be refused")
	}
	if _, err := backup.Restore(context.Background(), tampered, filepath.Join(t.TempDir(), "ws2")); err == nil {
		t.Fatal("restore of tampered archive must be refused")
	}
}

// rebuildTampered copies archive entries but substitutes modified bytes for
// one payload entry, keeping the original manifest (forged content attack).
func rebuildTampered(t *testing.T, srcPath, dstPath string) {
	t.Helper()
	manifest, err := backup.Verify(context.Background(), srcPath)
	if err != nil {
		t.Fatal(err)
	}
	out, _ := os.Create(dstPath)
	defer testutil.Close(t, out)
	gz := gzip.NewWriter(out)
	defer testutil.Close(t, gz)
	tw := tar.NewWriter(gz)
	defer testutil.Close(t, tw)

	// Re-encode the manifest exactly as verified.
	mjson, _ := json.MarshalIndent(manifest, "", "  ")
	testutil.Must(t, tw.WriteHeader(&tar.Header{Name: "manifest.json", Mode: 0o600, Size: int64(len(mjson))}))
	if _, err := tw.Write(mjson); err != nil {
		t.Fatal(err)
	}

	// Stream the original payload entries, flipping bytes in the database one.
	f, _ := os.Open(srcPath)
	defer testutil.Close(t, f)
	gr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer testutil.Close(t, gr)
	tr := tar.NewReader(gr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if hdr.Name == "manifest.json" {
			continue
		}
		body, _ := io.ReadAll(tr)
		if hdr.Name == "data/repairflow.db" {
			body[100] ^= 0xff // forged bytes
		}
		testutil.Must(t, tw.WriteHeader(&tar.Header{Name: hdr.Name, Mode: 0o600, Size: int64(len(body))}))
		if _, err := tw.Write(body); err != nil {
			t.Fatal(err)
		}
	}
}

// Scenario 7: a missing attachment aborts the backup (never a silent gap).
func TestMissingAttachmentAbortsBackup(t *testing.T) {
	src := newWorkspace(t)
	src.populated(t)
	atts, _ := src.svc.ListRepairAttachments(context.Background(), 1, true)
	name, _ := src.svc.AttachmentStorageName(context.Background(), atts[0].ID)
	testutil.Must(t, os.Remove(filepath.Join(src.home, "files", "tickets", "1", name)))

	archive := filepath.Join(t.TempDir(), "gap.rfbak")
	_, err := backup.Create(context.Background(), src.home, archive, "tester")
	if err == nil {
		t.Fatal("backup with missing attachment must fail")
	}
	if _, statErr := os.Stat(archive); statErr == nil {
		t.Fatal("no archive may be published on failure")
	}
	if _, statErr := os.Stat(archive + ".part"); statErr == nil {
		t.Fatal("partial archive must be cleaned up")
	}
}

// Extra consistency: bytes changed outside the application abort the backup.
func TestModifiedAttachmentAbortsBackup(t *testing.T) {
	src := newWorkspace(t)
	src.populated(t)
	atts, _ := src.svc.ListRepairAttachments(context.Background(), 1, true)
	name, _ := src.svc.AttachmentStorageName(context.Background(), atts[0].ID)
	path := filepath.Join(src.home, "files", "tickets", "1", name)
	testutil.Must(t, os.WriteFile(path, []byte("tampered content"), 0o600))

	_, err := backup.Create(context.Background(), src.home, filepath.Join(t.TempDir(), "t.rfbak"), "tester")
	if err == nil || !strings.Contains(err.Error(), "changed outside the application") {
		t.Fatalf("tampered bytes must abort backup, got: %v", err)
	}
}

// Scenario 8: restore refuses an existing nonempty workspace.
func TestRestoreRefusesPopulatedWorkspace(t *testing.T) {
	src := newWorkspace(t)
	src.populated(t)
	archive := filepath.Join(t.TempDir(), "a.rfbak")
	if _, err := backup.Create(context.Background(), src.home, archive, "tester"); err != nil {
		t.Fatal(err)
	}
	// Existing non-empty workspace.
	if _, err := backup.Restore(context.Background(), archive, src.home); errs.CodeOf(err) != errs.CodeWorkspaceExists {
		t.Fatalf("must refuse populated workspace, got: %v", err)
	}
	// An initialized-but-empty-looking workspace with a file also refused.
	other := filepath.Join(t.TempDir(), "occupied")
	testutil.Must(t, os.MkdirAll(other, 0o700))
	testutil.Must(t, os.WriteFile(filepath.Join(other, "unrelated.txt"), []byte("x"), 0o600))
	if _, err := backup.Restore(context.Background(), archive, other); errs.CodeOf(err) != errs.CodeWorkspaceExists {
		t.Fatalf("must refuse non-empty directory, got: %v", err)
	}
}

// Scenarios 9 and 12: restore to a different path, source untouched.
func TestRestorePortableAndNonDestructive(t *testing.T) {
	src := newWorkspace(t)
	src.populated(t)
	archive := filepath.Join(t.TempDir(), "portable.rfbak")
	if _, err := backup.Create(context.Background(), src.home, archive, "tester"); err != nil {
		t.Fatal(err)
	}
	before := treeHash(t, src.home)

	dest := filepath.Join(t.TempDir(), "elsewhere", "deep", "new-home")
	if _, err := backup.Restore(context.Background(), archive, dest); err != nil {
		t.Fatalf("restore to different path: %v", err)
	}
	if after := treeHash(t, src.home); after != before {
		t.Fatal("source workspace changed during restore")
	}
	// No hard-coded home paths: the restored workspace works from its new root.
	handle, err := db.Open(context.Background(), db.Path(dest))
	if err != nil {
		t.Fatal(err)
	}
	defer testutil.Close(t, handle)
	svc := app.New(handle).WithStore(files.New(dest))
	if missing, orphans, err := svc.ReconcileAttachments(context.Background()); err != nil || len(missing) != 0 || len(orphans) != 0 {
		t.Fatalf("portable restore reconcile: %v %v %v", missing, orphans, err)
	}
	// The manifest records the source home for information only.
	m, err := backup.Verify(context.Background(), archive)
	if err != nil || m.SourceHome != src.home {
		t.Fatalf("manifest source home: %+v %v", m, err)
	}
}

// Failed restores leave the destination safely removable.
func TestInterruptedRestoreLeavesCleanDestination(t *testing.T) {
	src := newWorkspace(t)
	src.populated(t)
	archive := filepath.Join(t.TempDir(), "a.rfbak")
	if _, err := backup.Create(context.Background(), src.home, archive, "tester"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(archive)
	corrupt := filepath.Join(t.TempDir(), "c.rfbak")
	bad := append([]byte(nil), data...)
	bad[len(bad)*2/3] ^= 0xff
	testutil.Must(t, os.WriteFile(corrupt, bad, 0o600))

	dest := filepath.Join(t.TempDir(), "damaged")
	_, err := backup.Restore(context.Background(), corrupt, dest)
	if err == nil {
		t.Fatal("corrupt restore must fail")
	}
	// Destination holds nothing user-visible (empty or absent).
	entries, statErr := os.ReadDir(dest)
	if statErr == nil && len(entries) > 0 {
		t.Fatalf("failed restore left content behind: %v", entries)
	}
}

// Self-inclusion is refused: backup destination inside the workspace.
func TestBackupRefusesSelfInclusion(t *testing.T) {
	src := newWorkspace(t)
	if _, err := backup.Create(context.Background(), src.home, filepath.Join(src.home, "backups", "self.rfbak"), "tester"); errs.CodeOf(err) != errs.CodeValidation {
		t.Fatalf("self-inclusion must be refused, got: %v", err)
	}
}
