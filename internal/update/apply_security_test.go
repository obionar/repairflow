package update

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"repairflow/internal/errs"
)

func versionScript(version, marker string) []byte {
	return []byte("#!/bin/sh\nprintf '%s\\n' '{\"ok\":true,\"command\":\"version\",\"contract\":2,\"data\":{\"version\":\"" +
		version + "\"}}'\n# " + marker + "\n")
}

// The signature authenticates one descriptor. Replacing the artifact pathname
// after that descriptor is opened must not change what gets installed.
func TestApplyRejectsPublishedTargetSwapBeforeHealth(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "repairflow")
	old := versionScript("1.0.0", "old")
	if err := os.WriteFile(target, old, 0o755); err != nil {
		t.Fatal(err)
	}
	artifact := filepath.Join(dir, "artifact")
	expected := versionScript("1.1.0", "expected")
	if err := os.WriteFile(artifact, expected, 0o755); err != nil {
		t.Fatal(err)
	}
	malicious := filepath.Join(dir, "malicious")
	marker := filepath.Join(dir, "malicious-ran")
	maliciousScript := []byte("#!/bin/sh\ntouch " + marker + "\nprintf '%s\\n' '{\"ok\":true,\"command\":\"version\",\"contract\":2,\"data\":{\"version\":\"1.1.0\"}}'\n")
	if err := os.WriteFile(malicious, maliciousScript, 0o755); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(expected)
	manifest := Manifest{
		FormatVersion: FormatVersion,
		Channel:       "stable",
		Version:       "1.1.0",
		CreatedAt:     "2026-11-10T00:00:00Z",
		Artifacts: []Artifact{{
			OS: runtime.GOOS, Arch: runtime.GOARCH, File: filepath.Base(artifact),
			SHA256: hex.EncodeToString(sum[:]), SizeBytes: int64(len(expected)),
		}},
	}
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sig := []byte(hex.EncodeToString(ed25519.Sign(priv, manifestBytes)))

	_, err = Apply(context.Background(), ApplyOptions{
		TargetPath: target, ArtifactPath: artifact,
		ManifestBytes: manifestBytes, Signature: sig, PublicKey: pub,
		CurrentVersion: "1.0.0", HealthTimeout: 5 * time.Second,
		afterPublishedVerify: func() {
			if err := os.Rename(malicious, target); err != nil {
				t.Errorf("replace published target: %v", err)
			}
		},
	})
	if err == nil {
		t.Fatal("a target swapped after verification must not be reported as success")
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("unverified replacement target was executed")
	}
	installed, readErr := os.ReadFile(target)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !bytes.Equal(installed, old) || bytes.Contains(installed, []byte("malicious")) {
		t.Fatalf("failed update did not restore the previous target:\n%s", installed)
	}
}

func TestApplyClassifiesMidCopyTruncationAsValidation(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "repairflow")
	if err := os.WriteFile(target, versionScript("1.0.0", "old"), 0o755); err != nil {
		t.Fatal(err)
	}
	artifact := filepath.Join(dir, "artifact")
	expected := versionScript("1.1.0", "expected")
	if err := os.WriteFile(artifact, expected, 0o755); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(expected)
	manifestBytes, err := json.Marshal(Manifest{
		FormatVersion: FormatVersion, Channel: "stable", Version: "1.1.0",
		CreatedAt: "2026-11-10T00:00:00Z",
		Artifacts: []Artifact{{
			OS: runtime.GOOS, Arch: runtime.GOARCH, File: filepath.Base(artifact),
			SHA256: hex.EncodeToString(sum[:]), SizeBytes: int64(len(expected)),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sig := []byte(hex.EncodeToString(ed25519.Sign(priv, manifestBytes)))

	_, err = Apply(context.Background(), ApplyOptions{
		TargetPath: target, ArtifactPath: artifact,
		ManifestBytes: manifestBytes, Signature: sig, PublicKey: pub,
		CurrentVersion: "1.0.0", HealthTimeout: 5 * time.Second,
		afterArtifactOpen: func() {
			if err := os.Truncate(artifact, int64(len(expected)-1)); err != nil {
				t.Errorf("truncate artifact: %v", err)
			}
		},
	})
	if errs.CodeOf(err) != errs.CodeValidation {
		t.Fatalf("short source must be reported as validation_error, got %v", err)
	}
	out, _ := os.ReadFile(target)
	if !bytes.Equal(out, versionScript("1.0.0", "old")) {
		t.Fatalf("refused truncated update changed target:\n%s", out)
	}
}

func TestApplyInstallsDescriptorBytesAfterSourcePathSwap(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "repairflow")
	if err := os.WriteFile(target, versionScript("1.0.0", "old"), 0o755); err != nil {
		t.Fatal(err)
	}
	artifact := filepath.Join(dir, "artifact")
	expected := versionScript("1.1.0", "expected")
	if err := os.WriteFile(artifact, expected, 0o755); err != nil {
		t.Fatal(err)
	}
	malicious := filepath.Join(dir, "malicious")
	if err := os.WriteFile(malicious, versionScript("1.1.0", "malicious"), 0o755); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(expected)
	manifest := Manifest{
		FormatVersion: FormatVersion,
		Channel:       "stable",
		Version:       "1.1.0",
		CreatedAt:     "2026-11-10T00:00:00Z",
		Artifacts: []Artifact{{
			OS: runtime.GOOS, Arch: runtime.GOARCH, File: filepath.Base(artifact),
			SHA256: hex.EncodeToString(sum[:]), SizeBytes: int64(len(expected)),
		}},
	}
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sig := []byte(hex.EncodeToString(ed25519.Sign(priv, manifestBytes)))

	res, err := Apply(context.Background(), ApplyOptions{
		TargetPath: target, ArtifactPath: artifact,
		ManifestBytes: manifestBytes, Signature: sig, PublicKey: pub,
		CurrentVersion: "1.0.0", HealthTimeout: 5 * time.Second,
		afterArtifactOpen: func() {
			if err := os.Rename(malicious, artifact); err != nil {
				t.Errorf("replace artifact path: %v", err)
			}
		},
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if res.ToVersion != "1.1.0" {
		t.Fatalf("result: %+v", res)
	}
	installed, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(installed, expected) || bytes.Contains(installed, []byte("malicious")) {
		t.Fatalf("installed bytes are not the authenticated descriptor bytes:\n%s", installed)
	}
}
