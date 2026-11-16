package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"repairflow/internal/buildinfo"
)

// TestUpdateVerifyChecksSignedChecksumList covers the release checksum
// signature: it must accept an authentic detached signature and refuse a
// tampered file. The key is generated here, never a real release key.
func TestUpdateVerifyChecksSignedChecksumList(t *testing.T) {
	keys, err := generateSigningKeys(filepath.Join(t.TempDir(), "keys"))
	if err != nil {
		t.Fatal(err)
	}
	pub, err := os.ReadFile(keys.PublicKeyFile)
	if err != nil {
		t.Fatal(err)
	}
	old := buildinfo.PublicKey
	buildinfo.PublicKey = strings.TrimSpace(string(pub))
	t.Cleanup(func() { buildinfo.PublicKey = old })

	sums := filepath.Join(t.TempDir(), "SHA256SUMS")
	if err := os.WriteFile(sums, []byte("0123456789abcdef  repairflow_Linux_arm64.tar.gz\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sig := sums + ".sig"
	if _, err := signManifest(keys.PrivateKeyFile, sums, sig); err != nil {
		t.Fatal(err)
	}

	if got := runEnv(t, nil, "update", "verify", "--file", sums, "--signature", sig); got.code != 0 {
		t.Fatalf("authentic checksum list refused: exit %d: %s", got.code, got.stderr)
	}

	if err := os.WriteFile(sums, []byte("fedcba9876543210  repairflow_Linux_arm64.tar.gz\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := runEnv(t, nil, "update", "verify", "--file", sums, "--signature", sig)
	if got.code != 2 || !strings.Contains(got.stderr, "signature") {
		t.Fatalf("tampered checksum list: exit %d, stderr %q; want exit 2 and a signature error", got.code, got.stderr)
	}
}
