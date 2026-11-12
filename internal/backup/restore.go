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
	"strconv"
	"strings"

	"repairflow/internal/config"
	"repairflow/internal/db"
	"repairflow/internal/errs"
)

// Verify reads an archive end-to-end and validates structure, manifest and
// every file hash. Corrupt, truncated or tampered archives are refused with
// explicit errors; nothing is extracted.
func Verify(ctx context.Context, archivePath string) (*Manifest, error) {
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
	if err := r.verifyEntries(manifest); err != nil {
		return nil, err
	}
	return manifest, nil
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

	manifest, err := extractAndVerify(ctx, archivePath, staging)
	if err != nil {
		cleanup()
		return nil, err
	}
	if err := validateRestoredDB(ctx, staging, manifest); err != nil {
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

// extractAndVerify streams the archive into staging, verifying every entry
// name and hash against the manifest and rejecting anything unexpected.
func extractAndVerify(ctx context.Context, archivePath, staging string) (*Manifest, error) {
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

	expect := expectedEntries(manifest)
	seen := map[string]bool{}
	for {
		hdr, err := r.tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, errs.Wrap(errs.CodeValidation, err, "archive is corrupt or truncated")
		}
		if hdr.Typeflag != tar.TypeReg {
			return nil, errs.New(errs.CodeValidation, "archive entry %q is not a regular file", hdr.Name)
		}
		wantHash, ok := expect[hdr.Name]
		if !ok {
			return nil, errs.New(errs.CodeValidation, "unexpected archive entry %q", hdr.Name)
		}
		if seen[hdr.Name] {
			return nil, errs.New(errs.CodeValidation, "duplicate archive entry %q", hdr.Name)
		}
		seen[hdr.Name] = true

		dst := filepath.Join(staging, filepath.FromSlash(hdr.Name))
		if !strings.HasPrefix(dst, staging+string(filepath.Separator)) {
			return nil, errs.New(errs.CodeValidation, "archive entry %q escapes the restore root", hdr.Name)
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return nil, errs.Wrap(errs.CodeInternal, err, "cannot create restore directory")
		}
		hash, size, err := writeVerified(dst, r.tr, hdr.Size)
		if err != nil {
			return nil, err
		}
		if hash != wantHash {
			return nil, errs.New(errs.CodeValidation,
				"archive entry %q fails integrity verification (manifest %s, got %s)", hdr.Name, wantHash, hash)
		}
		if _, err := io.Copy(io.Discard, r.tr); err != nil {
			return nil, errs.Wrap(errs.CodeValidation, err, "archive is corrupt or truncated")
		}
		_ = size
	}
	for name := range expect {
		if !seen[name] {
			return nil, errs.New(errs.CodeValidation, "archive entry %q is missing", name)
		}
	}
	return manifest, nil
}

// validateRestoredDB checks the restored database snapshot's integrity,
// schema compatibility and attachment references before publishing.
func validateRestoredDB(ctx context.Context, staging string, manifest *Manifest) error {
	dbPath := filepath.Join(staging, filepath.FromSlash(dbEntryName))
	handle, err := sql.Open(db.DriverName, dbPath)
	if err != nil {
		return errs.Wrap(errs.CodeValidation, err, "restored database cannot be opened")
	}
	defer func() { _ = handle.Close() }()
	if err := db.IntegrityCheck(ctx, handle); err != nil {
		return errs.Wrap(errs.CodeValidation, err, "restored database failed integrity checks")
	}
	version, err := db.CurrentVersion(ctx, handle)
	if err != nil {
		return err
	}
	if version < 1 || version > db.LatestVersion() {
		return errs.New(errs.CodeConfigVersion,
			"restored database schema version %d is not supported by this build (1..%d)", version, db.LatestVersion())
	}

	// Attachment references and bytes must agree in the restored tree.
	rows, err := handle.QueryContext(ctx,
		`SELECT repair_id, storage_name, sha256 FROM attachments WHERE state != 'purged'`)
	if err != nil {
		return errs.Wrap(errs.CodeValidation, err, "cannot read restored attachment records")
	}
	defer func() { _ = rows.Close() }()
	type ref struct {
		ticket int64
		name   string
		hash   string
	}
	var refs []ref
	for rows.Next() {
		var r ref
		if err := rows.Scan(&r.ticket, &r.name, &r.hash); err != nil {
			return errs.Wrap(errs.CodeValidation, err, "cannot read restored attachment record")
		}
		refs = append(refs, r)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(refs) != len(manifest.Attachments) {
		return errs.New(errs.CodeValidation,
			"restored attachment records (%d) do not match the manifest (%d)", len(refs), len(manifest.Attachments))
	}
	for _, r := range refs {
		file := filepath.Join(staging, "files", "tickets", strconv.FormatInt(r.ticket, 10), r.name)
		hash, _, err := hashFile(file)
		if err != nil {
			return errs.Wrap(errs.CodeValidation, err, "restored attachment %d/%s has no bytes", r.ticket, r.name)
		}
		if hash != r.hash {
			return errs.New(errs.CodeValidation, "restored attachment %d/%s fails hash verification", r.ticket, r.name)
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
	if hdr.Name != manifestName {
		return nil, errs.New(errs.CodeValidation, "archive does not start with %s", manifestName)
	}
	var m Manifest
	if err := json.NewDecoder(io.LimitReader(r.tr, 4<<20)).Decode(&m); err != nil {
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
	if m.Database.Name != dbEntryName || !sha64Re.MatchString(m.Database.SHA256) {
		return errs.New(errs.CodeValidation, "backup manifest database entry is invalid")
	}
	if m.Config != nil && (m.Config.Name != cfgEntryName || !sha64Re.MatchString(m.Config.SHA256)) {
		return errs.New(errs.CodeValidation, "backup manifest config entry is invalid")
	}
	for _, a := range m.Attachments {
		if !sha64Re.MatchString(a.SHA256) || a.SizeBytes <= 0 {
			return errs.New(errs.CodeValidation, "backup manifest attachment entry is invalid")
		}
	}
	return nil
}

// expectedEntries maps every archive entry name (besides the manifest) to its
// manifest hash. Anything outside this map is rejected.
func expectedEntries(m *Manifest) map[string]string {
	out := map[string]string{m.Database.Name: m.Database.SHA256}
	if m.Config != nil {
		out[m.Config.Name] = m.Config.SHA256
	}
	for _, a := range m.Attachments {
		name := fmt.Sprintf("files/tickets/%d/%s", a.TicketID, a.StorageName)
		out[name] = a.SHA256
	}
	return out
}

// verifyEntries streams all payload entries and checks hashes (Verify).
func (r *archiveReader) verifyEntries(m *Manifest) error {
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
		want, ok := expect[hdr.Name]
		if !ok {
			return errs.New(errs.CodeValidation, "unexpected archive entry %q", hdr.Name)
		}
		if seen[hdr.Name] {
			return errs.New(errs.CodeValidation, "duplicate archive entry %q", hdr.Name)
		}
		seen[hdr.Name] = true
		h := sha256.New()
		n, err := io.Copy(h, io.LimitReader(r.tr, hdr.Size+1))
		if err != nil {
			return errs.Wrap(errs.CodeValidation, err, "archive is corrupt or truncated")
		}
		if n != hdr.Size {
			return errs.New(errs.CodeValidation, "archive entry %q is truncated", hdr.Name)
		}
		if got := hex.EncodeToString(h.Sum(nil)); got != want {
			return errs.New(errs.CodeValidation,
				"archive entry %q fails integrity verification (manifest %s, got %s)", hdr.Name, want, got)
		}
	}
	for name := range expect {
		if !seen[name] {
			return errs.New(errs.CodeValidation, "archive entry %q is missing", name)
		}
	}
	return nil
}

// writeVerified extracts one entry to dst while hashing it.
func writeVerified(dst string, tr *tar.Reader, declaredSize int64) (string, int64, error) {
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", 0, errs.Wrap(errs.CodeInternal, err, "cannot create restore file")
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(tr, declaredSize+1))
	if err != nil {
		_ = f.Close()
		return "", 0, errs.Wrap(errs.CodeValidation, err, "archive is corrupt or truncated")
	}
	if n != declaredSize {
		_ = f.Close()
		return "", 0, errs.New(errs.CodeValidation, "archive entry is truncated")
	}
	if err := f.Close(); err != nil {
		return "", 0, errs.Wrap(errs.CodeInternal, err, "cannot close restore file")
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}
