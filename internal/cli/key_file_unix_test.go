//go:build unix

package cli

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestGenerateSigningKeysHonorsModesUnderRestrictiveUmask(t *testing.T) {
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)
	result, err := generateSigningKeys(filepath.Join(t.TempDir(), "keys"))
	if err != nil {
		t.Fatalf("keygen failed under umask 077: %v", err)
	}
	for path, want := range map[string]os.FileMode{
		result.PublicKeyFile: 0o644, result.PrivateKeyFile: 0o600,
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Fatalf("%s mode=%04o, want %04o", path, got, want)
		}
	}
}
