package update_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"repairflow/internal/errs"
	"repairflow/internal/update"
)

// ---- fixtures ------------------------------------------------------------

func writeScript(t *testing.T, dir, name, version string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	body := "#!/bin/sh\necho '{\"ok\":true,\"command\":\"version\",\"contract\":1,\"data\":{\"version\":\"" + version + "\"}}'\n"
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

type release struct {
	manifest []byte
	sig      []byte
	pub      ed25519.PublicKey
	artifact string
}

func makeRelease(t *testing.T, dir, version, goos, goarch string, artifactVersion string) release {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	artifact := writeScript(t, dir, "artifact-"+version, artifactVersion)
	hash, size, err := update.HashFile(artifact)
	if err != nil {
		t.Fatal(err)
	}
	m := update.Manifest{
		FormatVersion: update.FormatVersion, Channel: "stable",
		Version: version, CreatedAt: "2026-11-10T00:00:00Z",
		Artifacts: []update.Artifact{{
			OS: goos, Arch: goarch, File: filepath.Base(artifact), SHA256: hash, SizeBytes: size,
		}},
	}
	manifestBytes, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	sig := ed25519.Sign(priv, manifestBytes)
	return release{manifest: manifestBytes, sig: []byte(hex.EncodeToString(sig)), pub: pub, artifact: artifact}
}

func applyOpts(r release, target, current string) update.ApplyOptions {
	return update.ApplyOptions{
		TargetPath: target, ArtifactPath: r.artifact,
		ManifestBytes: r.manifest, Signature: r.sig, PublicKey: r.pub,
		CurrentVersion: current, HealthTimeout: 5 * time.Second,
	}
}

// ---- happy path -----------------------------------------------------------

func TestApplyHappyPath(t *testing.T) {
	dir := t.TempDir()
	target := writeScript(t, dir, "repairflow", "1.0.0")
	previous, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	r := makeRelease(t, dir, "1.1.0", runtime_GOOS, runtime_GOARCH, "1.1.0")

	res, err := update.Apply(context.Background(), applyOpts(r, target, "1.0.0"))
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if res.ToVersion != "1.1.0" {
		t.Fatalf("result: %+v", res)
	}
	// New binary active, previous preserved byte-for-byte.
	out, _ := execOutput(t, target)
	if !contains(out, "1.1.0") {
		t.Fatalf("target not updated: %s", out)
	}
	backup, err := os.ReadFile(res.BackupPath)
	if err != nil {
		t.Fatal("previous binary must be preserved")
	}
	if !bytes.Equal(backup, previous) {
		t.Fatal("preserved binary is not byte-identical to the previous target")
	}
	if _, err := os.Stat(target + ".new"); err == nil {
		t.Fatal("staging file must be gone after success")
	}
}

// ---- negative matrix ------------------------------------------------------

func TestApplyRefusals(t *testing.T) {
	dir := t.TempDir()
	target := writeScript(t, dir, "repairflow", "1.0.0")

	// Invalid signature.
	r := makeRelease(t, dir, "1.1.0", runtime_GOOS, runtime_GOARCH, "1.1.0")
	bad := r
	bad.sig = []byte(hex.EncodeToString(make([]byte, ed25519.SignatureSize)))
	if _, err := update.Apply(context.Background(), applyOpts(bad, target, "1.0.0")); errs.CodeOf(err) != errs.CodeValidation {
		t.Fatalf("invalid signature: %v", err)
	}

	// Hash mismatch (artifact bytes differ from the signed manifest).
	tampered := r
	tampered.artifact = writeScript(t, dir, "evil", "9.9.9")
	if _, err := update.Apply(context.Background(), applyOpts(tampered, target, "1.0.0")); errs.CodeOf(err) != errs.CodeValidation {
		t.Fatalf("tampered artifact must be refused as validation error: %v", err)
	}

	// No artifact for this platform.
	foreign := makeRelease(t, dir, "1.2.0", "plan9", "mips", "1.2.0")
	if _, err := update.Apply(context.Background(), applyOpts(foreign, target, "1.0.0")); errs.CodeOf(err) != errs.CodeValidation {
		t.Fatalf("foreign platform: %v", err)
	}

	// Same version and downgrade refused.
	same := makeRelease(t, dir, "1.0.0", runtime_GOOS, runtime_GOARCH, "1.0.0")
	if _, err := update.Apply(context.Background(), applyOpts(same, target, "1.0.0")); errs.CodeOf(err) != errs.CodeConflict {
		t.Fatalf("same version: %v", err)
	}
	down := makeRelease(t, dir, "0.9.0", runtime_GOOS, runtime_GOARCH, "0.9.0")
	if _, err := update.Apply(context.Background(), applyOpts(down, target, "1.0.0")); err == nil {
		t.Fatal("downgrade must be refused")
	}

	// None of the refusals touched the target.
	out, _ := execOutput(t, target)
	if !contains(out, "1.0.0") {
		t.Fatalf("target modified by refused update: %s", out)
	}
}

// An artifact path that is not a regular file is never a valid release
// artifact. Rejecting it before reading prevents FIFO blocking and closes the
// path-reopen class that allowed verification of one stream and installation
// of another.
func TestApplyRejectsSymlinkArtifact(t *testing.T) {
	dir := t.TempDir()
	target := writeScript(t, dir, "repairflow", "1.0.0")
	r := makeRelease(t, dir, "1.1.0", runtime_GOOS, runtime_GOARCH, "1.1.0")
	link := filepath.Join(dir, "artifact-link")
	if err := os.Symlink(r.artifact, link); err != nil {
		t.Fatal(err)
	}
	r.artifact = link

	if _, err := update.Apply(context.Background(), applyOpts(r, target, "1.0.0")); errs.CodeOf(err) != errs.CodeValidation {
		t.Fatalf("symlink artifact must be refused as validation error, got %v", err)
	}
	out, _ := execOutput(t, target)
	if !contains(out, "1.0.0") {
		t.Fatalf("refused symlink update modified target: %s", out)
	}
}

func TestApplyRejectsSymlinkTarget(t *testing.T) {
	dir := t.TempDir()
	realTarget := writeScript(t, dir, "real-repairflow", "1.0.0")
	target := filepath.Join(dir, "repairflow")
	if err := os.Symlink(realTarget, target); err != nil {
		t.Fatal(err)
	}
	r := makeRelease(t, dir, "1.1.0", runtime_GOOS, runtime_GOARCH, "1.1.0")

	if _, err := update.Apply(context.Background(), applyOpts(r, target, "1.0.0")); errs.CodeOf(err) != errs.CodeValidation {
		t.Fatalf("symlink target must be refused as validation error, got %v", err)
	}
	out, _ := execOutput(t, realTarget)
	if !contains(out, "1.0.0") {
		t.Fatalf("refused symlink target update modified real binary: %s", out)
	}
	if info, err := os.Lstat(target); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("refused update changed the target symlink: %v %v", info, err)
	}
}

// Fault: the new binary fails its health check -> automatic rollback.
func TestApplyHealthFailureRollsBack(t *testing.T) {
	dir := t.TempDir()
	target := writeScript(t, dir, "repairflow", "1.0.0")
	// Artifact reports the WRONG version -> health check must fail.
	r := makeRelease(t, dir, "1.1.0", runtime_GOOS, runtime_GOARCH, "0.0.1-wrong")

	_, err := update.Apply(context.Background(), applyOpts(r, target, "1.0.0"))
	if err == nil {
		t.Fatal("health failure must fail the update")
	}
	// Target restored to the previous, working binary.
	out, execErr := execOutput(t, target)
	if execErr != nil || !contains(out, "1.0.0") {
		t.Fatalf("rollback failed: %q %v", out, execErr)
	}
}

// Fault: stale staging from an interrupted update is cleaned before staging.
func TestApplyClearsStaleStaging(t *testing.T) {
	dir := t.TempDir()
	target := writeScript(t, dir, "repairflow", "1.0.0")
	if err := os.WriteFile(target+".new", []byte("stale bytes from a crashed update"), 0o755); err != nil {
		t.Fatal(err)
	}
	r := makeRelease(t, dir, "1.1.0", runtime_GOOS, runtime_GOARCH, "1.1.0")
	if _, err := update.Apply(context.Background(), applyOpts(r, target, "1.0.0")); err != nil {
		t.Fatalf("stale staging must not block the update: %v", err)
	}
	out, _ := execOutput(t, target)
	if !contains(out, "1.1.0") {
		t.Fatalf("target: %s", out)
	}
}

// keygen -> sign -> verify roundtrip.
func TestKeygenSignVerifyRoundtrip(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	// Sign a complete manifest (an empty artifacts list would be rejected).
	r := makeRelease(t, t.TempDir(), "1.1.0", "linux", "amd64", "1.1.0")
	manifestBytes := r.manifest
	sig := ed25519.Sign(priv, manifestBytes)

	m, err := update.VerifyManifest(manifestBytes, []byte(hex.EncodeToString(sig)), pub)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if m.Version != "1.1.0" {
		t.Fatalf("manifest: %+v", m)
	}
	if _, err := update.VerifyManifest(manifestBytes, []byte(hex.EncodeToString(sig)), r.pub); err == nil {
		t.Fatal("wrong key must fail verification")
	}
}

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.0.0", "1.0.0", 0},
		{"1.0.1", "1.0.0", 1},
		{"1.0.0", "1.0.1", -1},
		{"1.10.0", "1.9.0", 1},
		{"0.9.0", "1.0.0", -1},
		{"1.0", "1.0.0", 0},
		{"0.1.0", "0.1.0-rc9.1", 1},
		{"0.1.0-rc9.1", "0.1.0", -1},
		{"0.1.0-rc10", "0.1.0-rc9", 1},
		{"0.1.0-rc9.2", "0.1.0-rc9.10", -1},
		{"0.1.0-rc9", "0.1.0-rc9.1", -1},
		{"0.1.0+build.7", "0.1.0", 0},
		{"1.0.0-alpha", "1.0.0-beta", -1},
		{"0.1.0-rc9.1", "0.1.0-rc9.1", 0},
		{"1.0.0-alpha", "1.0.0-alpha", 0},
	}
	for _, c := range cases {
		if got := update.CompareVersions(c.a, c.b); got != c.want {
			t.Errorf("CompareVersions(%s,%s)=%d want %d", c.a, c.b, got, c.want)
		}
	}
}

// ---- helpers -------------------------------------------------------------

// runtime_GOOS/GOARCH mirror the test host (update package tests run on it).
var (
	runtime_GOOS   = runtime.GOOS
	runtime_GOARCH = runtime.GOARCH
)

func execOutput(t *testing.T, path string) (string, error) {
	t.Helper()
	out, err := exec.Command(path, "version", "--json").CombinedOutput()
	return string(out), err
}

func contains(haystack, needle string) bool {
	return strings.Contains(haystack, needle)
}
