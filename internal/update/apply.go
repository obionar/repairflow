package update

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"repairflow/internal/errs"
)

// ApplyResult reports a completed (or rolled-back) update.
type ApplyResult struct {
	FromVersion string `json:"from_version"`
	ToVersion   string `json:"to_version"`
	TargetPath  string `json:"target_path"`
	BackupPath  string `json:"backup_path"`
	RestartNote string `json:"restart_note"`
}

// ApplyOptions parameterizes the safe binary replacement.
type ApplyOptions struct {
	TargetPath     string // the binary to replace (usually os.Executable())
	ArtifactPath   string // the new binary from the release
	ManifestBytes  []byte
	Signature      []byte
	PublicKey      ed25519.PublicKey
	CurrentVersion string
	HealthTimeout  time.Duration

	// Test-only seams. Unexported so external callers cannot install hooks;
	// they let the package prove path-swap behavior deterministically.
	afterArtifactOpen    func()
	afterPublishedVerify func()
}

// Apply verifies and installs a local release over the target binary.
// Protocol: verify first, stage to <target>.new, preserve the
// current binary as <target>.prev, publish by rename, then health-check the
// new binary (`version --json` must report the manifest version). A failed
// health check restores .prev. The running process is NOT replaced in
// memory — the caller restarts the service through its supervisor or manually.
//
// Crash windows: a leftover .new is stale staging (safe to delete);
// a leftover .prev with a healthy target is the preserved previous binary;
// if the target is missing but .prev exists, restore it manually (documented
// in docs/USAGE.md).
func Apply(ctx context.Context, opts ApplyOptions) (*ApplyResult, error) {
	if runtime.GOOS != "linux" {
		return nil, errs.New(errs.CodeValidation,
			"verified update execution is supported on Linux only; this build cannot apply releases")
	}
	if opts.HealthTimeout <= 0 {
		opts.HealthTimeout = 10 * time.Second
	}
	m, err := VerifyManifest(opts.ManifestBytes, opts.Signature, opts.PublicKey)
	if err != nil {
		return nil, err
	}
	artifact, err := m.matchArtifact(runtimeGOOS, runtimeGOARCH)
	if err != nil {
		return nil, err
	}

	// Version policy: no same-version re-apply, no downgrades.
	switch cmp := CompareVersions(m.Version, opts.CurrentVersion); {
	case cmp == 0:
		return nil, errs.New(errs.CodeConflict, "version %s is already installed", m.Version)
	case cmp < 0:
		return nil, errs.New(errs.CodeValidation,
			"refusing downgrade %s -> %s; recovery uses the preserved previous binary", opts.CurrentVersion, m.Version)
	}

	// The replacement protocol preserves one previous binary by renaming the
	// target. Refuse a symlink/device here so that contract is not silently
	// changed to a link that may dangle later.
	targetInfo, err := os.Lstat(opts.TargetPath)
	if err != nil {
		return nil, errs.Wrap(errs.CodeValidation, err, "cannot inspect target binary")
	}
	if !targetInfo.Mode().IsRegular() {
		return nil, errs.New(errs.CodeValidation, "target %q is not a regular file", opts.TargetPath)
	}

	staging := opts.TargetPath + ".new"
	backup := opts.TargetPath + ".prev"
	_ = os.Remove(staging) // clear stale staging from an interrupted update

	// Authenticate exactly one stream. stageArtifact opens the source once
	// without following symlinks or blocking on FIFOs, requires a regular
	// file, copies it with a size bound, and hashes those staged bytes. The
	// staged file is hashed again from disk before publication.
	stagedHash, stagedSize, err := stageArtifact(opts.ArtifactPath, staging, artifact.SizeBytes, opts.afterArtifactOpen)
	if err != nil {
		return nil, err
	}
	if stagedHash != artifact.SHA256 || stagedSize != artifact.SizeBytes {
		_ = os.Remove(staging)
		return nil, errs.New(errs.CodeValidation,
			"artifact %s/%s fails staged verification (manifest %s/%d, staged %s/%d)",
			artifact.OS, artifact.Arch, artifact.SHA256, artifact.SizeBytes, stagedHash, stagedSize)
	}
	stagedHash, stagedSize, err = hashArtifactFile(staging)
	if err != nil {
		_ = os.Remove(staging)
		return nil, err
	}
	if stagedHash != artifact.SHA256 || stagedSize != artifact.SizeBytes {
		_ = os.Remove(staging)
		return nil, errs.New(errs.CodeValidation,
			"artifact %s/%s fails staged readback verification (manifest %s/%d, staged %s/%d)",
			artifact.OS, artifact.Arch, artifact.SHA256, artifact.SizeBytes, stagedHash, stagedSize)
	}

	// Preserve the current binary, publish the verified staged bytes.
	_ = os.Remove(backup) // keep exactly one previous binary
	if err := os.Rename(opts.TargetPath, backup); err != nil {
		_ = os.Remove(staging)
		return nil, errs.Wrap(errs.CodeInternal, err, "cannot preserve the current binary")
	}
	if err := syncDir(filepath.Dir(opts.TargetPath)); err != nil {
		if rerr := os.Rename(backup, opts.TargetPath); rerr != nil {
			return nil, errs.New(errs.CodeInternal,
				"cannot sync binary directory (%v) AND rollback failed; target may be missing; restore %s manually; verified staging retained at %s",
				err, backup, staging)
		}
		_ = os.Remove(staging)
		return nil, err
	}
	if err := os.Rename(staging, opts.TargetPath); err != nil {
		_ = os.Rename(backup, opts.TargetPath) // undo immediately
		return nil, errs.Wrap(errs.CodeInternal, err, "cannot publish the new binary")
	}
	if err := syncDir(filepath.Dir(opts.TargetPath)); err != nil {
		if rerr := os.Rename(backup, opts.TargetPath); rerr != nil {
			return nil, errs.New(errs.CodeInternal,
				"cannot sync binary directory after publish (%v) AND rollback failed; verified binary may be installed at %s; restore %s manually",
				err, opts.TargetPath, backup)
		}
		return nil, err
	}

	// Read the published target through one descriptor. The same descriptor is
	// executed by the health check, so a path swap cannot substitute the bytes
	// that are both verified and run.
	targetFile, publishedHash, publishedSize, err := openArtifactForExec(opts.TargetPath, artifact.SizeBytes)
	if err != nil {
		if rerr := os.Rename(backup, opts.TargetPath); rerr != nil {
			return nil, errs.New(errs.CodeInternal,
				"published artifact failed verification (%v) AND rollback failed; restore %s manually", err, backup)
		}
		return nil, err
	}
	if publishedHash != artifact.SHA256 || publishedSize != artifact.SizeBytes {
		_ = targetFile.Close()
		if rerr := os.Rename(backup, opts.TargetPath); rerr != nil {
			return nil, errs.New(errs.CodeInternal,
				"published artifact failed verification AND rollback failed; restore %s manually", backup)
		}
		return nil, errs.New(errs.CodeValidation,
			"published artifact %s/%s fails verification (manifest %s/%d, installed %s/%d)",
			artifact.OS, artifact.Arch, artifact.SHA256, artifact.SizeBytes, publishedHash, publishedSize)
	}
	if opts.afterPublishedVerify != nil {
		opts.afterPublishedVerify()
	}

	// Health check: the new binary must identify itself as the manifest
	// version; otherwise roll back. Linux executes the verified
	// descriptor as /proc/self/fd/3, not the replaceable pathname.
	if err := healthCheckFD(ctx, targetFile, m.Version, opts.HealthTimeout); err != nil {
		_ = targetFile.Close()
		if rerr := os.Rename(backup, opts.TargetPath); rerr != nil {
			return nil, errs.Wrap(errs.CodeInternal, rerr,
				"update failed health check (%v) AND rollback failed; restore %s manually", err, backup)
		}
		_ = os.Remove(staging)
		return nil, errs.Wrap(errs.CodeInternal, err,
			"update failed health check; previous binary restored from %s", backup)
	}
	if err := targetFile.Close(); err != nil {
		if rerr := os.Rename(backup, opts.TargetPath); rerr != nil {
			return nil, errs.Wrap(errs.CodeInternal, rerr,
				"cannot close verified target (%v) AND rollback failed; restore %s manually", err, backup)
		}
		return nil, errs.Wrap(errs.CodeInternal, err, "cannot close verified target")
	}
	// Final pathname readback catches a target replacement during the health
	// check; such a swap is rolled back rather than reported as success.
	finalHash, finalSize, err := hashArtifactFile(opts.TargetPath)
	if err != nil || finalHash != artifact.SHA256 || finalSize != artifact.SizeBytes {
		if rerr := os.Rename(backup, opts.TargetPath); rerr != nil {
			return nil, errs.New(errs.CodeInternal,
				"target changed after verification (%v) AND rollback failed; restore %s manually", err, backup)
		}
		if err != nil {
			return nil, err
		}
		return nil, errs.New(errs.CodeValidation,
			"target changed after verification (manifest %s/%d, final %s/%d)",
			artifact.SHA256, artifact.SizeBytes, finalHash, finalSize)
	}
	if err := syncDir(filepath.Dir(opts.TargetPath)); err != nil {
		return nil, errs.New(errs.CodeInternal,
			"verified version %s is installed but the binary directory sync failed (%v); restart will run the new version",
			m.Version, err)
	}

	return &ApplyResult{
		FromVersion: opts.CurrentVersion, ToVersion: m.Version,
		TargetPath: opts.TargetPath, BackupPath: backup,
		RestartNote: "binary replaced on disk; restart the service/process to run the new version",
	}, nil
}

// stageArtifact copies one already-opened regular-file stream to dst with
// executable permissions, bounds the copy at expectedSize+1, hashes the bytes
// that are actually written, and fsyncs the staged file before returning.
func stageArtifact(src, dst string, expectedSize int64, afterOpen func()) (string, int64, error) {
	in, err := openArtifactSource(src)
	if err != nil {
		return "", 0, err
	}
	info, err := in.Stat()
	if err != nil {
		_ = in.Close()
		return "", 0, errs.Wrap(errs.CodeValidation, err, "cannot inspect artifact %q", src)
	}
	if !info.Mode().IsRegular() {
		_ = in.Close()
		return "", 0, errs.New(errs.CodeValidation, "artifact %q is not a regular file", src)
	}
	if info.Size() != expectedSize {
		_ = in.Close()
		return "", 0, errs.New(errs.CodeValidation,
			"artifact %q has size %d, manifest requires %d", src, info.Size(), expectedSize)
	}
	if afterOpen != nil {
		afterOpen()
	}

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o700)
	if err != nil {
		_ = in.Close()
		return "", 0, errs.Wrap(errs.CodeInternal, err, "cannot stage the new binary")
	}
	hash := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(out, hash), io.LimitReader(in, expectedSize+1))
	closeInErr := in.Close()
	if copyErr == nil {
		copyErr = closeInErr
	}
	if copyErr == nil && n != expectedSize {
		copyErr = errs.New(errs.CodeValidation,
			"artifact changed while staging: read %d bytes, manifest requires %d", n, expectedSize)
	}
	if copyErr == nil {
		copyErr = out.Chmod(0o755)
	}
	if copyErr == nil {
		copyErr = out.Sync()
	}
	closeErr := out.Close()
	if copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		_ = os.Remove(dst)
		code := errs.CodeOf(copyErr)
		if code == "" {
			code = errs.CodeInternal
		}
		return "", 0, errs.Wrap(code, copyErr, "cannot stage the new binary")
	}
	if err := syncDir(filepath.Dir(dst)); err != nil {
		_ = os.Remove(dst)
		return "", 0, err
	}
	return hex.EncodeToString(hash.Sum(nil)), n, nil
}

// openArtifactForExec opens one regular-file descriptor, hashes those exact
// bytes, rewinds it for execution, and returns the open descriptor. The caller
// must consume or close it; the pathname is never used for verification.
func openArtifactForExec(path string, expectedSize int64) (*os.File, string, int64, error) {
	f, err := openArtifactSource(path)
	if err != nil {
		return nil, "", 0, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, "", 0, errs.Wrap(errs.CodeValidation, err, "cannot inspect artifact %q", path)
	}
	if !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, "", 0, errs.New(errs.CodeValidation, "artifact %q is not a regular file", path)
	}
	if expectedSize >= 0 && info.Size() != expectedSize {
		_ = f.Close()
		return nil, "", 0, errs.New(errs.CodeValidation,
			"artifact %q has size %d, manifest requires %d", path, info.Size(), expectedSize)
	}
	hash := sha256.New()
	n, copyErr := io.Copy(hash, io.LimitReader(f, info.Size()+1))
	if copyErr == nil {
		_, copyErr = f.Seek(0, io.SeekStart)
	}
	if copyErr != nil {
		_ = f.Close()
		return nil, "", 0, errs.Wrap(errs.CodeValidation, copyErr, "cannot hash artifact %q", path)
	}
	if n != info.Size() {
		_ = f.Close()
		return nil, "", 0, errs.New(errs.CodeValidation,
			"artifact changed while hashing: read %d bytes, stat reported %d", n, info.Size())
	}
	return f, hex.EncodeToString(hash.Sum(nil)), n, nil
}

// hashArtifactFile re-reads one regular-file stream (never a symlink or FIFO)
// and returns its SHA-256 and exact size.
func hashArtifactFile(path string) (string, int64, error) {
	f, hash, size, err := openArtifactForExec(path, -1)
	if f != nil {
		_ = f.Close()
	}
	return hash, size, err
}

// syncDir makes renames in dir durable. Linux supports directory fsync; the
// updater is a Linux service, so failure is reported rather than ignored.
func syncDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return errs.Wrap(errs.CodeInternal, err, "cannot open binary directory for sync")
	}
	syncErr := f.Sync()
	closeErr := f.Close()
	if syncErr != nil {
		return errs.Wrap(errs.CodeInternal, syncErr, "cannot sync binary directory")
	}
	if closeErr != nil {
		return errs.Wrap(errs.CodeInternal, closeErr, "cannot close binary directory")
	}
	return nil
}

// healthCheckFD executes the exact verified descriptor. On Linux ExtraFiles[0]
// is fd 3 in the child, so /proc/self/fd/3 resolves to the already-open inode
// rather than re-opening a replaceable pathname.
func healthCheckFD(ctx context.Context, verified *os.File, wantVersion string, timeout time.Duration) error {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, "/proc/self/fd/3", "version", "--json")
	cmd.ExtraFiles = []*os.File{verified}
	out, err := cmd.Output()
	if err != nil {
		return errs.Wrap(errs.CodeInternal, err, "health check command failed")
	}
	var envelope struct {
		OK   bool `json:"ok"`
		Data struct {
			Version string `json:"version"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out, &envelope); err != nil || !envelope.OK {
		return errs.New(errs.CodeInternal, "health check produced unreadable output")
	}
	if envelope.Data.Version != wantVersion {
		return errs.New(errs.CodeInternal,
			"health check reports version %q, expected %q", envelope.Data.Version, wantVersion)
	}
	return nil
}

// runtimeGOOS/runtimeGOARCH are indirected for testability (tests simulate
// foreign platforms).
var (
	runtimeGOOS   = runtime.GOOS
	runtimeGOARCH = runtime.GOARCH
)
