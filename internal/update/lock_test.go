package update

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestApplyWaitsForTargetLock(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "repairflow")
	if err := os.WriteFile(target, versionScript("1.0.0", "old"), 0o755); err != nil {
		t.Fatal(err)
	}
	expected := versionScript("1.1.0", "new")
	artifact := filepath.Join(dir, "artifact")
	if err := os.WriteFile(artifact, expected, 0o755); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(expected)
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	manifestBytes, err := json.Marshal(Manifest{
		FormatVersion: FormatVersion, Channel: "stable", Version: "1.1.0",
		Artifacts: []Artifact{{OS: runtime.GOOS, Arch: runtime.GOARCH, File: "repairflow",
			SHA256: hex.EncodeToString(sum[:]), SizeBytes: int64(len(expected))}},
	})
	if err != nil {
		t.Fatal(err)
	}
	opts := ApplyOptions{
		TargetPath: target, ArtifactPath: artifact,
		ManifestBytes: manifestBytes, Signature: []byte(hex.EncodeToString(ed25519.Sign(priv, manifestBytes))),
		PublicKey: pub, CurrentVersion: "1.0.0", HealthTimeout: 5 * time.Second,
	}

	unlock, err := lockInstallTarget(target)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		result, err := Apply(context.Background(), opts)
		if err == nil && result.FromVersion != "1.0.1" {
			err = fmt.Errorf("installed version was not read after locking: %+v", result)
		}
		done <- err
	}()
	select {
	case err := <-done:
		unlock()
		t.Fatalf("apply completed while the target lock was held: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	if err := os.WriteFile(target, versionScript("1.0.1", "installed while waiting"), 0o755); err != nil {
		unlock()
		t.Fatal(err)
	}
	unlock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("apply after lock release: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("apply did not resume after the target lock was released")
	}
}
