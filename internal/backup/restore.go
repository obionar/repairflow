package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"repairflow/internal/config"
	"repairflow/internal/db"
	"repairflow/internal/errs"
)

// Verify checks every payload's size/hash and database/config usability using
// the same rules as Restore. Only the database and config are temporarily extracted.
func Verify(ctx context.Context, archivePath string) (*Manifest, error) {
	tmp, err := os.MkdirTemp("", "repairflow-verify-*")
	if err != nil {
		return nil, errs.Wrap(errs.CodeInternal, err, "cannot create verification directory")
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	return readAndVerify(ctx, archivePath, tmp, false)
}

// Restore validates the archive and publishes it into destHome, which must
// not exist or must be an empty directory. Everything is staged under
// destHome/run/restore-*.part and only renamed into place after full
// verification, so an interrupted restore leaves a removable staging
// directory instead of a half-published workspace.
func Restore(ctx context.Context, archivePath, destHome string) (*Manifest, error) {
	if err := prepareDest(destHome); err != nil {
		return nil, err
	}
	created, err := ensureDest(destHome)
	if err != nil {
		return nil, err
	}
	cleanup := func() {
		_ = os.RemoveAll(filepath.Join(destHome, "run"))
		if created {
			// Only remove the root if nothing was published into it.
			if entries, err := os.ReadDir(destHome); err == nil && len(entries) == 0 {
				_ = os.Remove(destHome)
			}
		}
	}

	staging := filepath.Join(destHome, "run", "restore-staging.part")
	if err := os.MkdirAll(staging, 0o700); err != nil {
		cleanup()
		return nil, errs.Wrap(errs.CodeInternal, err, "cannot create restore staging")
	}

	manifest, err := readAndVerify(ctx, archivePath, staging, true)
	if err != nil {
		cleanup()
		return nil, err
	}

	// Publish: move staged content into the final layout.
	if err := publish(staging, destHome, manifest); err != nil {
		// Staging is preserved on publish failure for manual recovery.
		return nil, errs.Wrap(errs.CodeInternal, err,
			"restore publish failed; staged data kept at %s for recovery", staging)
	}
	_ = os.RemoveAll(staging)
	return manifest, nil
}

// prepareDest validates the restore destination policy: never overwrite an
// existing populated workspace (data-safety rule).
func prepareDest(destHome string) error {
	if strings.TrimSpace(destHome) == "" {
		return errs.New(errs.CodeValidation, "restore destination must not be empty")
	}
	if !filepath.IsAbs(destHome) {
		return errs.New(errs.CodeValidation, "restore destination must be an absolute path")
	}
	if err := config.AssertSafeWorkspaceRoot(filepath.Clean(destHome)); err != nil {
		return err
	}
	if info, err := os.Stat(destHome); err == nil {
		if !info.IsDir() {
			return errs.New(errs.CodeUnsafePath, "restore destination %q is not a directory", destHome)
		}
		entries, err := os.ReadDir(destHome)
		if err != nil {
			return errs.Wrap(errs.CodeInternal, err, "cannot inspect restore destination")
		}
		if len(entries) > 0 {
			return errs.New(errs.CodeWorkspaceExists,
				"refusing to restore into populated %q; restore targets must be empty or new", destHome)
		}
	} else if !os.IsNotExist(err) {
		return errs.Wrap(errs.CodeInternal, err, "cannot inspect restore destination")
	}
	return nil
}

// ensureDest creates the destination root and reports whether it did.
func ensureDest(destHome string) (bool, error) {
	if _, err := os.Stat(destHome); err == nil {
		return false, nil
	}
	if err := os.MkdirAll(destHome, 0o700); err != nil {
		return false, errs.Wrap(errs.CodeInternal, err, "cannot create restore destination")
	}
	return true, nil
}

// readAndVerify shares the archive and usability checks for verify and restore.
// Verify discards attachment bytes after hashing; restore stages them for publication.
func readAndVerify(ctx context.Context, archivePath, staging string, extract bool) (*Manifest, error) {
	r, err := openArchive(archivePath)
	if err != nil {
		return nil, err
	}
	defer r.close()

	manifest, err := r.readManifest()
	if err != nil {
		return nil, err
	}
	if err := checkManifest(manifest); err != nil {
		return nil, err
	}

	if err := r.verifyEntries(manifest, staging, extract); err != nil {
		return nil, err
	}
	if err := validateDatabaseFile(ctx, filepath.Join(staging, filepath.FromSlash(dbEntryName)), manifest); err != nil {
		return nil, err
	}
	if manifest.Config != nil {
		if _, err := config.Load(filepath.Join(staging, cfgEntryName)); err != nil {
			return nil, errs.Wrap(errs.CodeValidation, err, "archive configuration is not usable")
		}
	}
	return manifest, nil
}

func validateDatabaseFile(ctx context.Context, dbPath string, manifest *Manifest) error {
	handle, err := sql.Open(db.DriverName, dbPath)
	if err != nil {
		return errs.Wrap(errs.CodeValidation, err, "database cannot be opened")
	}
	defer func() { _ = handle.Close() }()
	return validateDatabaseHandle(ctx, handle, manifest)
}

// validateDatabaseHandle checks SQLite integrity, schema support and the
// attachment rows against the manifest. It does not read attachment bytes.
func validateDatabaseHandle(ctx context.Context, handle *sql.DB, manifest *Manifest) error {
	if err := db.IntegrityCheck(ctx, handle); err != nil {
		return errs.Wrap(errs.CodeValidation, err, "database failed integrity checks")
	}
	version, err := db.CurrentVersion(ctx, handle)
	if err != nil {
		return err
	}
	if version < 1 || version > db.LatestVersion() {
		return errs.New(errs.CodeConfigVersion,
			"database schema version %d is not supported by this build (1..%d)", version, db.LatestVersion())
	}

	if version != manifest.SchemaVersion {
		return errs.New(errs.CodeValidation, "database schema version %d does not match the manifest (%d)", version, manifest.SchemaVersion)
	}

	rows, err := handle.QueryContext(ctx,
		`SELECT repair_id, storage_name, size_bytes, sha256 FROM attachments WHERE state != 'purged'`)
	if err != nil {
		return errs.Wrap(errs.CodeValidation, err, "cannot read restored attachment records")
	}
	defer func() { _ = rows.Close() }()
	refs := map[string]FileEntry{}
	for rows.Next() {
		var repair int64
		var name string
		var entry FileEntry
		if err := rows.Scan(&repair, &name, &entry.SizeBytes, &entry.SHA256); err != nil {
			return errs.Wrap(errs.CodeValidation, err, "cannot read restored attachment record")
		}
		refs[fmt.Sprintf("%d/%s", repair, name)] = entry
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(refs) != len(manifest.Attachments) {
		return errs.New(errs.CodeValidation,
			"restored attachment records (%d) do not match the manifest (%d)", len(refs), len(manifest.Attachments))
	}
	for _, a := range manifest.Attachments {
		key := fmt.Sprintf("%d/%s", a.TicketID, a.StorageName)
		if ref, ok := refs[key]; !ok || ref.SHA256 != a.SHA256 || ref.SizeBytes != a.SizeBytes {
			return errs.New(errs.CodeValidation, "manifest attachment %s does not match the database", key)
		}
	}
	return nil
}

// publish moves verified staged content into the final workspace layout.
func publish(staging, destHome string, manifest *Manifest) error {
	dirs := []string{"data", "files", "backups", "logs", "cache", "run"}
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(destHome, d), 0o700); err != nil {
			return err
		}
	}
	if err := moveFile(filepath.Join(staging, filepath.FromSlash(dbEntryName)), filepath.Join(destHome, "data", "repairflow.db")); err != nil {
		return err
	}
	if manifest.Config != nil {
		if err := moveFile(filepath.Join(staging, filepath.FromSlash(cfgEntryName)), filepath.Join(destHome, "config.toml")); err != nil {
			return err
		}
	}
	if _, err := os.Stat(filepath.Join(staging, "files", "tickets")); err == nil {
		if err := os.Rename(filepath.Join(staging, "files", "tickets"), filepath.Join(destHome, "files", "tickets")); err != nil {
			return err
		}
	}
	return nil
}

func moveFile(src, dst string) error {
	if err := os.Chmod(src, 0o600); err != nil {
		return err
	}
	if err := os.Rename(src, dst); err != nil {
		return errs.Wrap(errs.CodeInternal, err, "cannot publish %s", dst)
	}
	return nil
}

// ---- archive reading helpers ---------------------------------------------

type archiveReader struct {
	f  *os.File
	gz *gzip.Reader
	tr *tar.Reader
}

func openArchive(path string) (*archiveReader, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, errs.Wrap(errs.CodeNotFound, err, "backup archive %q not found", path)
		}
		return nil, errs.Wrap(errs.CodeInternal, err, "cannot open backup archive")
	}
	gz, err := gzip.NewReader(f)
	if err != nil {
		_ = f.Close()
		return nil, errs.Wrap(errs.CodeValidation, err, "backup archive %q is not a valid gzip archive", path)
	}
	return &archiveReader{f: f, gz: gz, tr: tar.NewReader(gz)}, nil
}

func (r *archiveReader) close() {
	_ = r.gz.Close()
	_ = r.f.Close()
}

// readManifest requires manifest.json to be the FIRST archive entry.
func (r *archiveReader) readManifest() (*Manifest, error) {
	hdr, err := r.tr.Next()
	if err != nil {
		return nil, errs.Wrap(errs.CodeValidation, err, "archive is empty, corrupt or truncated")
	}
	if hdr.Typeflag != tar.TypeReg {
		return nil, errs.New(errs.CodeValidation, "archive manifest is not a regular file")
	}
	if hdr.Name != manifestName {
		return nil, errs.New(errs.CodeValidation, "archive does not start with %s", manifestName)
	}
	return decodeManifest(r.tr)
}

// decodeManifest reads one bounded manifest. The writer uses the same bound.
func decodeManifest(r io.Reader) (*Manifest, error) {
	body, err := io.ReadAll(io.LimitReader(r, MaxManifestBytes+1))
	if err != nil {
		return nil, errs.Wrap(errs.CodeValidation, err, "archive manifest is corrupt")
	}
	if len(body) > MaxManifestBytes {
		return nil, errs.New(errs.CodeValidation,
			"archive manifest is larger than the %d-byte limit", MaxManifestBytes)
	}
	var m Manifest
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, errs.Wrap(errs.CodeValidation, err, "archive manifest is corrupt")
	}
	return &m, nil
}

func checkManifest(m *Manifest) error {
	if m.FormatVersion != FormatVersion {
		return errs.New(errs.CodeConfigVersion, "backup format version %d is not supported (expected %d)", m.FormatVersion, FormatVersion)
	}
	if m.SchemaVersion < 1 {
		return errs.New(errs.CodeValidation, "backup manifest has no valid schema version")
	}
	if m.Database.Name != dbEntryName || m.Database.SizeBytes <= 0 || !sha64Re.MatchString(m.Database.SHA256) {
		return errs.New(errs.CodeValidation, "backup manifest database entry is invalid")
	}
	if m.Config != nil && (m.Config.Name != cfgEntryName || m.Config.SizeBytes <= 0 || !sha64Re.MatchString(m.Config.SHA256)) {
		return errs.New(errs.CodeValidation, "backup manifest config entry is invalid")
	}
	for _, a := range m.Attachments {
		name := fmt.Sprintf("files/tickets/%d/%s", a.TicketID, a.StorageName)
		if !attachmentEntryRe.MatchString(name) || !sha64Re.MatchString(a.SHA256) || a.SizeBytes <= 0 {
			return errs.New(errs.CodeValidation, "backup manifest attachment entry is invalid")
		}
	}
	return nil
}

// expectedEntries maps each payload name to its declared size and hash.
func expectedEntries(m *Manifest) map[string]FileEntry {
	out := map[string]FileEntry{m.Database.Name: m.Database}
	if m.Config != nil {
		out[m.Config.Name] = *m.Config
	}
	for _, a := range m.Attachments {
		name := fmt.Sprintf("files/tickets/%d/%s", a.TicketID, a.StorageName)
		out[name] = FileEntry{Name: name, SizeBytes: a.SizeBytes, SHA256: a.SHA256}
	}
	return out
}

// verifyEntries hashes every payload, staging DB/config or all files for restore.
func (r *archiveReader) verifyEntries(m *Manifest, staging string, extract bool) error {
	expect := expectedEntries(m)
	seen := map[string]bool{}
	for {
		hdr, err := r.tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return errs.Wrap(errs.CodeValidation, err, "archive is corrupt or truncated")
		}
		if hdr.Typeflag != tar.TypeReg {
			return errs.New(errs.CodeValidation, "archive entry %q is not a regular file", hdr.Name)
		}
		want, ok := expect[hdr.Name]
		if !ok {
			return errs.New(errs.CodeValidation, "unexpected archive entry %q", hdr.Name)
		}
		if seen[hdr.Name] {
			return errs.New(errs.CodeValidation, "duplicate archive entry %q", hdr.Name)
		}
		seen[hdr.Name] = true
		if hdr.Size != want.SizeBytes {
			return errs.New(errs.CodeValidation, "archive entry %q size %d does not match the manifest (%d)", hdr.Name, hdr.Size, want.SizeBytes)
		}
		var dst *os.File
		if extract || hdr.Name == dbEntryName || hdr.Name == cfgEntryName {
			path := filepath.Join(staging, filepath.FromSlash(hdr.Name))
			if !strings.HasPrefix(path, staging+string(filepath.Separator)) {
				return errs.New(errs.CodeValidation, "archive entry %q escapes the restore root", hdr.Name)
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				return errs.Wrap(errs.CodeInternal, err, "cannot create verification directory")
			}
			dst, err = os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if err != nil {
				return errs.Wrap(errs.CodeInternal, err, "cannot write verification file")
			}
		}
		h := sha256.New()
		var sink io.Writer = h
		if dst != nil {
			sink = io.MultiWriter(h, dst)
		}
		n, copyErr := io.Copy(sink, r.tr)
		var closeErr error
		if dst != nil {
			closeErr = dst.Close()
		}
		if copyErr != nil {
			return errs.Wrap(errs.CodeValidation, copyErr, "archive is corrupt or truncated")
		}
		if closeErr != nil {
			return errs.Wrap(errs.CodeInternal, closeErr, "cannot close verification file")
		}
		if n != hdr.Size {
			return errs.New(errs.CodeValidation, "archive entry %q is truncated", hdr.Name)
		}
		if got := hex.EncodeToString(h.Sum(nil)); got != want.SHA256 {
			return errs.New(errs.CodeValidation,
				"archive entry %q fails integrity verification (manifest %s, got %s)", hdr.Name, want.SHA256, got)
		}
	}
	for name := range expect {
		if !seen[name] {
			return errs.New(errs.CodeValidation, "archive entry %q is missing", name)
		}
	}
	// tar EOF can precede the gzip trailer; Close alone does not check its CRC.
	if _, err := io.Copy(io.Discard, r.gz); err != nil {
		return errs.Wrap(errs.CodeValidation, err, "archive gzip stream is corrupt or truncated")
	}
	return nil
}
