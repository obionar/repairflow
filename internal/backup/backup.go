// Package backup implements portable workspace backup and restore
// Backup and restore preserve the complete workspace and verify every file.
//
// Consistency model: the SQLite database is snapshotted with `VACUUM INTO`
// (SQLite's supported consistent-copy mechanism — never a raw WAL file copy),
// then every attachment recorded in that snapshot is hash-verified against the
// live file before archiving. The archive therefore contains one coherent
// state: DB snapshot + byte-verified originals, or a clear failure. A backup
// is written to `<dest>.part` and only renamed to its final name after a full
// verification pass — a partial backup is never presented as a successful one.
//
// Archive layout (gzip tar, portable relative paths):
//
//	manifest.json                 integrity + compatibility metadata
//	data/repairflow.db            consistent database snapshot
//	config.toml                   workspace configuration (optional)
//	files/tickets/<id>/<opaque>   original attachment bytes
//
// Disposable content (cache/, run/, logs/, backups/) is excluded.
package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"repairflow/internal/app"
	"repairflow/internal/buildinfo"
	"repairflow/internal/config"
	"repairflow/internal/db"
	"repairflow/internal/errs"
	"repairflow/internal/files"
)

// FormatVersion is the backup archive format version.
const FormatVersion = 1

// ArchiveSuffix is the conventional backup file suffix.
const ArchiveSuffix = ".rfbak"

// FileEntry is one archived file with its integrity data.
type FileEntry struct {
	Name      string `json:"name"`
	SizeBytes int64  `json:"size_bytes"`
	SHA256    string `json:"sha256"`
}

// AttachmentEntry records one original attachment file from the snapshot.
type AttachmentEntry struct {
	TicketID    int64  `json:"ticket_id"`
	StorageName string `json:"storage_name"`
	SizeBytes   int64  `json:"size_bytes"`
	SHA256      string `json:"sha256"`
}

// Manifest is the archive's integrity and compatibility record.
type Manifest struct {
	FormatVersion int               `json:"format_version"`
	AppVersion    string            `json:"app_version"`
	SchemaVersion int               `json:"schema_version"`
	CreatedAt     string            `json:"created_at"`
	CreatedBy     string            `json:"created_by"`
	SourceHome    string            `json:"source_home"` // informational only; restore is path-independent
	Database      FileEntry         `json:"database"`
	Config        *FileEntry        `json:"config,omitempty"`
	Attachments   []AttachmentEntry `json:"attachments"`
}

var sha64Re = regexp.MustCompile(`^[a-f0-9]{64}$`)

const (
	manifestName = "manifest.json"
	dbEntryName  = "data/repairflow.db"
	cfgEntryName = "config.toml"
)

// Result reports a completed backup.
type Result struct {
	Archive   string   `json:"archive"`
	SizeBytes int64    `json:"size_bytes"`
	SHA256    string   `json:"sha256"` // of the archive itself
	Manifest  Manifest `json:"manifest"`
}

// Create writes a verified backup archive of workspace home to destPath.
// The destination must not live inside the workspace (self-inclusion is
// refused) and must not already exist.
func Create(ctx context.Context, home, destPath, actor string) (*Result, error) {
	if err := config.AssertSafeWorkspaceRoot(home); err != nil {
		return nil, err
	}
	destPath, err := validateDest(home, destPath)
	if err != nil {
		return nil, err
	}
	actor = strings.TrimSpace(actor)
	if actor == "" {
		actor = "operator"
	}

	staging, err := os.MkdirTemp(filepath.Dir(destPath), ".rfbackup-staging-*")
	if err != nil {
		return nil, errs.Wrap(errs.CodeInternal, err, "cannot create backup staging directory")
	}
	defer func() { _ = os.RemoveAll(staging) }()

	manifest, snapshotPath, err := snapshotAndInventory(ctx, home, staging, actor)
	if err != nil {
		return nil, err
	}

	partPath := destPath + ".part"
	if err := writeArchive(ctx, home, partPath, staging, snapshotPath, manifest); err != nil {
		_ = os.Remove(partPath)
		return nil, err
	}
	// Full verification pass before publishing the final name.
	if _, err := Verify(ctx, partPath); err != nil {
		_ = os.Remove(partPath)
		return nil, errs.Wrap(errs.CodeInternal, err, "backup self-verification failed; no backup was published")
	}
	if err := os.Chmod(partPath, 0o600); err != nil {
		_ = os.Remove(partPath)
		return nil, errs.Wrap(errs.CodeInternal, err, "cannot set backup permissions")
	}
	if err := os.Rename(partPath, destPath); err != nil {
		_ = os.Remove(partPath)
		return nil, errs.Wrap(errs.CodeInternal, err, "cannot publish backup")
	}

	archiveHash, size, err := hashFile(destPath)
	if err != nil {
		return nil, err
	}
	return &Result{Archive: destPath, SizeBytes: size, SHA256: archiveHash, Manifest: *manifest}, nil
}

// snapshotAndInventory produces the consistent DB snapshot and the verified
// attachment inventory for the manifest. Every attachment recorded in the
// snapshot is hash-checked against its live file here, so the archive can
// never silently mix a DB state with missing or modified originals.
func snapshotAndInventory(ctx context.Context, home, staging, actor string) (*Manifest, string, error) {
	handle, err := db.Open(ctx, db.Path(home))
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = handle.Close() }()
	svc := app.New(handle).WithStore(files.New(home))
	if err := svc.RecoverPendingDeletes(ctx); err != nil {
		return nil, "", err
	}

	snapshotPath := filepath.Join(staging, "snapshot.db")
	if _, err := handle.ExecContext(ctx, "VACUUM INTO "+quoteLiteral(snapshotPath)); err != nil {
		return nil, "", errs.Wrap(errs.CodeInternal, err, "cannot create consistent database snapshot")
	}

	snap, err := sql.Open(db.DriverName, snapshotPath)
	if err != nil {
		return nil, "", errs.Wrap(errs.CodeInternal, err, "cannot open database snapshot")
	}
	defer func() { _ = snap.Close() }()
	if err := db.IntegrityCheck(ctx, snap); err != nil {
		return nil, "", errs.Wrap(errs.CodeInternal, err, "snapshot failed integrity checks")
	}
	schemaVersion, err := db.CurrentVersion(ctx, snap)
	if err != nil {
		return nil, "", err
	}

	// Inventory every attachment whose bytes belong to this DB state.
	rows, err := snap.QueryContext(ctx,
		`SELECT repair_id, storage_name, size_bytes, sha256 FROM attachments WHERE state != 'purged' ORDER BY repair_id, storage_name`)
	if err != nil {
		return nil, "", errs.Wrap(errs.CodeInternal, err, "cannot read attachment inventory")
	}
	var attachments []AttachmentEntry
	for rows.Next() {
		var e AttachmentEntry
		if err := rows.Scan(&e.TicketID, &e.StorageName, &e.SizeBytes, &e.SHA256); err != nil {
			_ = rows.Close()
			return nil, "", errs.Wrap(errs.CodeInternal, err, "cannot read attachment record")
		}
		file := filepath.Join(home, "files", "tickets", strconv.FormatInt(e.TicketID, 10), e.StorageName)
		gotHash, gotSize, err := hashFile(file)
		if err != nil {
			_ = rows.Close()
			return nil, "", errs.Wrap(errs.CodeInternal, err,
				"attachment %d/%s is recorded in the database but its bytes are unavailable", e.TicketID, e.StorageName)
		}
		if gotHash != e.SHA256 || gotSize != e.SizeBytes {
			_ = rows.Close()
			return nil, "", errs.New(errs.CodeConflict,
				"attachment %d/%s changed outside the application (db %s/%d, file %s/%d); refusing inconsistent backup",
				e.TicketID, e.StorageName, e.SHA256, e.SizeBytes, gotHash, gotSize)
		}
		attachments = append(attachments, e)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, "", errs.Wrap(errs.CodeInternal, err, "cannot iterate attachment inventory")
	}
	if err := rows.Close(); err != nil {
		return nil, "", errs.Wrap(errs.CodeInternal, err, "cannot close attachment inventory")
	}

	manifest := &Manifest{
		FormatVersion: FormatVersion,
		AppVersion:    buildinfo.Get().Version,
		SchemaVersion: schemaVersion,
		CreatedAt:     time.Now().UTC().Format(time.RFC3339),
		CreatedBy:     actor,
		SourceHome:    home,
		Attachments:   attachments,
	}
	dbHash, dbSize, err := hashFile(snapshotPath)
	if err != nil {
		return nil, "", err
	}
	manifest.Database = FileEntry{Name: dbEntryName, SizeBytes: dbSize, SHA256: dbHash}

	configPath := filepath.Join(home, config.ConfigName)
	if cfgHash, cfgSize, err := hashFile(configPath); err == nil {
		manifest.Config = &FileEntry{Name: cfgEntryName, SizeBytes: cfgSize, SHA256: cfgHash}
	} else if os.IsNotExist(unwrapPathError(err)) {
		manifest.Config = nil // config is optional in the archive
	} else {
		return nil, "", err
	}
	return manifest, snapshotPath, nil
}

// writeArchive streams the manifest, snapshot, config and verified attachment
// bytes into a gzip tar at partPath, re-hashing every file on the way in so a
// file changed during archiving aborts the backup.
func writeArchive(ctx context.Context, home, partPath, staging, snapshotPath string, manifest *Manifest) error {
	out, err := os.OpenFile(partPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return errs.Wrap(errs.CodeInternal, err, "cannot create backup archive")
	}
	gz := gzip.NewWriter(out)
	tw := tar.NewWriter(gz)

	fail := func(err error) error {
		_ = tw.Close()
		_ = gz.Close()
		_ = out.Close()
		return err
	}

	manifestJSON, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fail(errs.Wrap(errs.CodeInternal, err, "cannot encode manifest"))
	}
	if err := writeEntry(tw, manifestName, manifestJSON); err != nil {
		return fail(err)
	}
	if err := addFile(tw, dbEntryName, snapshotPath, manifest.Database.SHA256, manifest.Database.SizeBytes); err != nil {
		return fail(err)
	}
	if manifest.Config != nil {
		if err := addFile(tw, cfgEntryName, filepath.Join(home, config.ConfigName), manifest.Config.SHA256, manifest.Config.SizeBytes); err != nil {
			return fail(err)
		}
	}
	for _, a := range manifest.Attachments {
		name := path.Join("files", "tickets", strconv.FormatInt(a.TicketID, 10), a.StorageName)
		src := filepath.Join(home, "files", "tickets", strconv.FormatInt(a.TicketID, 10), a.StorageName)
		if err := addFile(tw, name, src, a.SHA256, a.SizeBytes); err != nil {
			return fail(err)
		}
	}
	if err := tw.Close(); err != nil {
		_ = gz.Close()
		_ = out.Close()
		return errs.Wrap(errs.CodeInternal, err, "cannot finish backup archive")
	}
	if err := gz.Close(); err != nil {
		_ = out.Close()
		return errs.Wrap(errs.CodeInternal, err, "cannot compress backup archive")
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return errs.Wrap(errs.CodeInternal, err, "cannot flush backup archive")
	}
	if err := out.Close(); err != nil {
		return errs.Wrap(errs.CodeInternal, err, "cannot close backup archive")
	}
	return nil
}

// addFile copies one file into the archive, verifying it still matches the
// inventory hash captured before archiving started.
func addFile(tw *tar.Writer, entryName, srcPath, wantHash string, wantSize int64) error {
	f, err := os.Open(srcPath)
	if err != nil {
		return errs.Wrap(errs.CodeInternal, err, "cannot read %q for backup", srcPath)
	}
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		_ = f.Close()
		return errs.Wrap(errs.CodeInternal, err, "cannot read %q for backup", srcPath)
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != wantHash || n != wantSize {
		_ = f.Close()
		return errs.New(errs.CodeConflict, "%s changed during backup (expected %s/%d, got %s/%d)", entryName, wantHash, wantSize, got, n)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		_ = f.Close()
		return errs.Wrap(errs.CodeInternal, err, "cannot rewind %q", srcPath)
	}
	if err := tw.WriteHeader(&tar.Header{Name: entryName, Mode: 0o600, Size: n, Format: tar.FormatPAX}); err != nil {
		_ = f.Close()
		return errs.Wrap(errs.CodeInternal, err, "cannot write archive entry %s", entryName)
	}
	if _, err := io.Copy(tw, f); err != nil {
		_ = f.Close()
		return errs.Wrap(errs.CodeInternal, err, "cannot write archive entry %s", entryName)
	}
	if err := f.Close(); err != nil {
		return errs.Wrap(errs.CodeInternal, err, "cannot close %q after archiving", srcPath)
	}
	return nil
}

func writeEntry(tw *tar.Writer, name string, body []byte) error {
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(body)), Format: tar.FormatPAX}); err != nil {
		return errs.Wrap(errs.CodeInternal, err, "cannot write archive entry %s", name)
	}
	if _, err := tw.Write(body); err != nil {
		return errs.Wrap(errs.CodeInternal, err, "cannot write archive entry %s", name)
	}
	return nil
}

// quoteLiteral renders a single-quoted SQLite string literal.
func quoteLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

// unwrapPathError digs to the underlying *os.PathError for IsNotExist checks.
func unwrapPathError(err error) error {
	for {
		u, ok := err.(interface{ Unwrap() error })
		if !ok || u.Unwrap() == nil {
			return err
		}
		err = u.Unwrap()
	}
}

// validateDest normalizes and validates the destination path.
func validateDest(home, destPath string) (string, error) {
	if strings.TrimSpace(destPath) == "" {
		return "", errs.New(errs.CodeValidation, "backup destination must not be empty")
	}
	if !filepath.IsAbs(destPath) {
		return "", errs.New(errs.CodeValidation, "backup destination must be an absolute path")
	}
	destPath = filepath.Clean(destPath)
	homeAbs, err := filepath.Abs(home)
	if err != nil {
		return "", errs.Wrap(errs.CodeInternal, err, "cannot resolve workspace path")
	}
	if destPath == homeAbs || strings.HasPrefix(destPath, homeAbs+string(filepath.Separator)) {
		return "", errs.New(errs.CodeValidation, "refusing backup destination inside the workspace (the archive must never include itself)")
	}
	if info, err := os.Lstat(destPath); err == nil {
		if info.IsDir() {
			return "", errs.New(errs.CodeConflict, "backup destination %q is a directory", destPath)
		}
		return "", errs.New(errs.CodeConflict, "refusing to overwrite existing %q", destPath)
	} else if !os.IsNotExist(err) {
		return "", errs.Wrap(errs.CodeInternal, err, "cannot inspect backup destination")
	}
	if _, err := os.Stat(filepath.Dir(destPath)); err != nil {
		return "", errs.Wrap(errs.CodeValidation, err, "backup destination directory does not exist")
	}
	return destPath, nil
}

// hashFile streams a file's SHA-256 and size.
func hashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, errs.Wrap(errs.CodeInternal, err, "cannot open %q", path)
	}
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		_ = f.Close()
		return "", 0, errs.Wrap(errs.CodeInternal, err, "cannot hash %q", path)
	}
	if err := f.Close(); err != nil {
		return "", 0, errs.Wrap(errs.CodeInternal, err, "cannot close %q", path)
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}
