// Package files owns original attachment bytes in the workspace. Storage
// rules for original bytes, hashes, opaque names and recoverable deletion:
//
//   - bytes live under <workspace>/files/tickets/<ticket-id>/<opaque-name>;
//     user-supplied filenames are display metadata ONLY and never touch the
//     filesystem;
//   - uploads stream through a staging file in <workspace>/run/ with the size
//     cap enforced during the copy (never fully buffered), then get SHA-256
//     hashed and published by rename;
//   - a failure at any step removes the staged file — a failed upload never
//     leaves visible bytes;
//   - exports are byte-identical and hash-verified.
package files

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"repairflow/internal/errs"
)

// MaxAttachmentBytes is the default per-file cap (256 MB). Configurable
// per call; the CLI passes this default.
const MaxAttachmentBytes = 256 << 20

var storageNameRe = regexp.MustCompile(`^[a-f0-9]{32}$`)

// Store resolves attachment paths inside one workspace.
type Store struct {
	home string
}

// New builds a store for the workspace at home.
func New(home string) *Store { return &Store{home: home} }

// Stored describes one published file.
type Stored struct {
	StorageName string `json:"-"`
	SizeBytes   int64  `json:"size_bytes"`
	SHA256      string `json:"sha256"`
	MediaType   string `json:"media_type"`
}

// Add streams src into the repair's storage tree and publishes it. The copy
// is size-capped and hashed on the way through; the source filename never
// influences the storage path.
func (s *Store) Add(ticketID int64, src io.Reader, maxBytes int64) (Stored, error) {
	if ticketID <= 0 {
		return Stored{}, errs.New(errs.CodeValidation, "ticket id must be positive")
	}
	if maxBytes <= 0 {
		maxBytes = MaxAttachmentBytes
	}

	if err := os.MkdirAll(s.ticketDir(ticketID), 0o700); err != nil {
		return Stored{}, errs.Wrap(errs.CodeInternal, err, "cannot create attachment directory")
	}
	if err := os.MkdirAll(s.runDir(), 0o700); err != nil {
		return Stored{}, errs.Wrap(errs.CodeInternal, err, "cannot create staging directory")
	}
	staging, err := os.CreateTemp(s.runDir(), "upload-*.part")
	if err != nil {
		return Stored{}, errs.Wrap(errs.CodeInternal, err, "cannot create staging file")
	}
	stagingPath := staging.Name()
	defer func() {
		_ = staging.Close()
		_ = os.Remove(stagingPath) // no-op after successful rename
	}()

	hash := sha256.New()
	limited := io.LimitReader(src, maxBytes+1)
	n, err := io.Copy(io.MultiWriter(staging, hash), limited)
	if err != nil {
		return Stored{}, errs.Wrap(errs.CodeInternal, err, "cannot stream upload")
	}
	if n > maxBytes {
		return Stored{}, errs.New(errs.CodeValidation, "file exceeds the %d byte limit", maxBytes)
	}
	if n == 0 {
		return Stored{}, errs.New(errs.CodeValidation, "refusing empty file")
	}
	if err := staging.Sync(); err != nil {
		return Stored{}, errs.Wrap(errs.CodeInternal, err, "cannot flush staging file")
	}
	if err := staging.Close(); err != nil {
		return Stored{}, errs.Wrap(errs.CodeInternal, err, "cannot close staging file")
	}

	// Sniff the media type from the staged head; unknown types stay generic
	// binary and are download-only.
	head, err := readHead(stagingPath, 512)
	if err != nil {
		return Stored{}, errs.Wrap(errs.CodeInternal, err, "cannot inspect staged attachment")
	}
	mediaType := http.DetectContentType(head)

	name, err := randomStorageName()
	if err != nil {
		return Stored{}, err
	}
	target, err := s.path(ticketID, name)
	if err != nil {
		return Stored{}, err
	}
	if err := os.Rename(stagingPath, target); err != nil {
		return Stored{}, errs.Wrap(errs.CodeInternal, err, "cannot publish attachment file")
	}
	if err := os.Chmod(target, 0o600); err != nil {
		_ = os.Remove(target)
		return Stored{}, errs.Wrap(errs.CodeInternal, err, "cannot set attachment permissions")
	}
	return Stored{StorageName: name, SizeBytes: n, SHA256: hex.EncodeToString(hash.Sum(nil)), MediaType: mediaType}, nil
}

// Export copies the stored bytes to destPath and verifies the content hash
// while copying. Existing destinations are refused (never overwrite user
// files). Returns the byte count and hash.
func (s *Store) Export(ticketID int64, storageName, destPath, wantHash string) (int64, string, error) {
	src, err := s.Open(ticketID, storageName)
	if err != nil {
		return 0, "", err
	}
	if strings.TrimSpace(destPath) == "" {
		_ = src.Close()
		return 0, "", errs.New(errs.CodeValidation, "export path must not be empty")
	}
	if _, err := os.Lstat(destPath); err == nil {
		_ = src.Close()
		return 0, "", errs.New(errs.CodeConflict, "refusing to overwrite existing %q", destPath)
	}
	dst, err := os.OpenFile(destPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		_ = src.Close()
		return 0, "", errs.Wrap(errs.CodeInternal, err, "cannot create export file")
	}
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(dst, hash), src)
	closeErr := src.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		_ = dst.Close()
		_ = os.Remove(destPath)
		return 0, "", errs.Wrap(errs.CodeInternal, err, "cannot export attachment")
	}
	if err := dst.Close(); err != nil {
		_ = os.Remove(destPath)
		return 0, "", errs.Wrap(errs.CodeInternal, err, "cannot close export file")
	}
	got := hex.EncodeToString(hash.Sum(nil))
	if wantHash != "" && got != wantHash {
		_ = os.Remove(destPath)
		return 0, "", errs.New(errs.CodeInternal, "export hash mismatch: stored %s, got %s", wantHash, got)
	}
	return n, got, nil
}

// Open returns the stored bytes for reading (path-safe).
func (s *Store) Open(ticketID int64, storageName string) (*os.File, error) {
	path, err := s.path(ticketID, storageName)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, errs.Wrap(errs.CodeNotFound, err, "attachment file %s for ticket %d is missing", storageName, ticketID)
		}
		return nil, errs.Wrap(errs.CodeInternal, err, "cannot open attachment file")
	}
	return f, nil
}

// Delete removes stored bytes (permanent purge). The caller must have applied
// the retention rules; the metadata/audit record survives elsewhere.
func (s *Store) Delete(ticketID int64, storageName string) error {
	path, err := s.path(ticketID, storageName)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return errs.Wrap(errs.CodeInternal, err, "cannot purge attachment file")
	}
	return nil
}

// Exists reports whether the stored bytes are present, without hiding
// invalid paths or unexpected filesystem failures.
func (s *Store) Exists(ticketID int64, storageName string) (bool, error) {
	path, err := s.path(ticketID, storageName)
	if err != nil {
		return false, err
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, errs.Wrap(errs.CodeInternal, err, "cannot inspect attachment file")
	}
	return true, nil
}

// List returns the opaque storage names present for one repair.
func (s *Store) List(ticketID int64) ([]string, error) {
	if ticketID <= 0 {
		return nil, errs.New(errs.CodeValidation, "ticket id must be positive")
	}
	entries, err := os.ReadDir(s.ticketDir(ticketID))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, errs.Wrap(errs.CodeInternal, err, "cannot list attachment directory")
	}
	out := []string{}
	for _, e := range entries {
		if !e.IsDir() && storageNameRe.MatchString(e.Name()) {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

// path validates the opaque name and resolves it under the ticket directory.
// Any attempt to smuggle path separators or traversal is refused.
func (s *Store) path(ticketID int64, storageName string) (string, error) {
	if ticketID <= 0 {
		return "", errs.New(errs.CodeValidation, "ticket id must be positive")
	}
	if !storageNameRe.MatchString(storageName) {
		return "", errs.New(errs.CodeValidation, "invalid attachment storage name")
	}
	return filepath.Join(s.ticketDir(ticketID), storageName), nil
}

func (s *Store) ticketDir(ticketID int64) string {
	return filepath.Join(s.home, "files", "tickets", fmt.Sprintf("%d", ticketID))
}

func (s *Store) runDir() string { return filepath.Join(s.home, "run") }

func randomStorageName() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", errs.Wrap(errs.CodeInternal, err, "cannot generate storage name")
	}
	return hex.EncodeToString(b[:]), nil
}

func readHead(path string, n int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(io.LimitReader(f, n))
}

// SafeDisplayName keeps only the base name and strips control characters so a
// hostile filename can never influence paths or output rendering.
func SafeDisplayName(raw string) string {
	name := filepath.Base(strings.ReplaceAll(raw, "\\", "/"))
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, name)
	name = strings.TrimSpace(name)
	if name == "." || name == "/" {
		return ""
	}
	return name
}
