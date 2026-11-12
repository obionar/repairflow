package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGenerateSigningKeysRefusesOverwriteAndPreservesExistingFiles(t *testing.T) {
	out := filepath.Join(t.TempDir(), "keys")
	first, err := generateSigningKeys(out)
	if err != nil {
		t.Fatal(err)
	}
	pubBefore, err := os.ReadFile(first.PublicKeyFile)
	if err != nil {
		t.Fatal(err)
	}
	privBefore, err := os.ReadFile(first.PrivateKeyFile)
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(first.PrivateKeyFile); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("private key mode = %v, want 0600 (%v)", info.Mode(), err)
	}

	// Reproduce the audit case: a second run must not overwrite the key, and
	// it must not silently accept or tighten an existing loose private file.
	if err := os.Chmod(first.PrivateKeyFile, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := generateSigningKeys(out); err == nil {
		t.Fatal("second keygen unexpectedly overwrote the existing keypair")
	}
	pubAfter, err := os.ReadFile(first.PublicKeyFile)
	if err != nil {
		t.Fatal(err)
	}
	privAfter, err := os.ReadFile(first.PrivateKeyFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(pubAfter) != string(pubBefore) || string(privAfter) != string(privBefore) {
		t.Fatal("second keygen changed the existing key material")
	}
	if info, err := os.Stat(first.PrivateKeyFile); err != nil || info.Mode().Perm() != 0o644 {
		t.Fatalf("existing mode was changed: %v (%v)", info.Mode(), err)
	}
}

func TestGenerateSigningKeysRefusesSymlinkAndRemovesPartialPair(t *testing.T) {
	out := filepath.Join(t.TempDir(), "keys")
	if err := os.MkdirAll(out, 0o700); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(victim, []byte("existing private material\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	privatePath := filepath.Join(out, "release-signing.key")
	if err := os.Symlink(victim, privatePath); err != nil {
		t.Fatal(err)
	}
	if _, err := generateSigningKeys(out); err == nil {
		t.Fatal("keygen followed an existing private-key symlink")
	}
	body, err := os.ReadFile(victim)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "existing private material\n" {
		t.Fatal("symlink target was overwritten")
	}
	if _, err := os.Stat(filepath.Join(out, "release-signing.pub")); !os.IsNotExist(err) {
		t.Fatalf("failed keygen left a partial public key: %v", err)
	}
}

func TestGenerateSigningKeysRefusesLooseOutputDirectory(t *testing.T) {
	out := filepath.Join(t.TempDir(), "keys")
	if err := os.MkdirAll(out, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(out, 0o777); err != nil {
		t.Fatal(err)
	}
	if _, err := generateSigningKeys(out); err == nil {
		t.Fatal("keygen accepted a group/world-accessible output directory")
	}
	for _, name := range []string{"release-signing.pub", "release-signing.key"} {
		if _, err := os.Stat(filepath.Join(out, name)); !os.IsNotExist(err) {
			t.Fatalf("loose-directory refusal left %s: %v", name, err)
		}
	}
}
