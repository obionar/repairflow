package files

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"repairflow/internal/errs"
)

func TestAddRoundtripAndHash(t *testing.T) {
	store := New(t.TempDir())
	payload := []byte("firmware dump \x00\x01\x02 with binary bytes")
	stored, err := store.Add(7, bytes.NewReader(payload), 0)
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if stored.SizeBytes != int64(len(payload)) {
		t.Fatalf("size: %+v", stored)
	}
	want := sha256.Sum256(payload)
	if stored.SHA256 != hex.EncodeToString(want[:]) {
		t.Fatalf("hash: %s", stored.SHA256)
	}
	exists, err := store.Exists(7, stored.StorageName)
	if err != nil || !exists {
		t.Fatalf("file must exist after publish: exists=%t err=%v", exists, err)
	}

	// Export is byte-identical and hash-verified; never overwrites.
	dest := filepath.Join(t.TempDir(), "out.bin")
	n, hash, err := store.Export(7, stored.StorageName, dest, stored.SHA256)
	if err != nil || n != int64(len(payload)) || hash != stored.SHA256 {
		t.Fatalf("export: %d %s %v", n, hash, err)
	}
	got, _ := os.ReadFile(dest)
	if !bytes.Equal(got, payload) {
		t.Fatal("exported bytes differ from original")
	}
	if _, _, err := store.Export(7, stored.StorageName, dest, stored.SHA256); errs.CodeOf(err) != errs.CodeConflict {
		t.Fatalf("overwrite must be refused: %v", err)
	}
}

func TestSizeCapAndEmptyRefused(t *testing.T) {
	store := New(t.TempDir())
	big := bytes.NewReader(make([]byte, 1025))
	if _, err := store.Add(1, big, 1024); errs.CodeOf(err) != errs.CodeValidation {
		t.Fatalf("oversize must be refused: %v", err)
	}
	if _, err := store.Add(1, bytes.NewReader(nil), 0); errs.CodeOf(err) != errs.CodeValidation {
		t.Fatalf("empty file must be refused: %v", err)
	}
	// Failed uploads leave no staged or published bytes.
	if names, _ := store.List(1); len(names) != 0 {
		t.Fatalf("failed uploads left files: %v", names)
	}
	entries, _ := os.ReadDir(filepath.Join(t.TempDir(), "run"))
	_ = entries
}

func TestPathSafety(t *testing.T) {
	store := New(t.TempDir())
	stored, err := store.Add(1, bytes.NewReader([]byte("x")), 0)
	if err != nil {
		t.Fatal(err)
	}
	// Invalid storage names return classified errors, never panic or join traversal input.
	for _, bad := range []string{"../escape", "..", "a/b", "", strings.Repeat("f", 31), strings.Repeat("f", 33), strings.Repeat(strings.ToUpper("f"), 32)} {
		if _, err := store.path(1, bad); errs.CodeOf(err) != errs.CodeValidation {
			t.Errorf("name %q must be refused with validation_error, got %v", bad, err)
		}
	}
	if _, err := store.Open(-1, stored.StorageName); errs.CodeOf(err) != errs.CodeValidation {
		t.Fatalf("invalid ticket id must be refused: %v", err)
	}
	// Missing file is a classified not_found, not a raw OS error.
	if _, err := store.Open(1, strings.Repeat("a", 32)); errs.CodeOf(err) != errs.CodeNotFound {
		t.Fatalf("missing file: %v", err)
	}
	// Wrong ticket id cannot reach another ticket's bytes.
	if _, err := store.Open(2, stored.StorageName); errs.CodeOf(err) != errs.CodeNotFound {
		t.Fatalf("cross-ticket access must miss: %v", err)
	}
}

func TestSafeDisplayName(t *testing.T) {
	cases := map[string]string{
		"/etc/passwd":       "passwd",
		`..\..\evil.exe`:    "evil.exe",
		"photo.png":         "photo.png",
		"we\x00ird\x1fname": "weirdname",
		"":                  "",
		".":                 "",
	}
	for in, want := range cases {
		if got := SafeDisplayName(in); got != want {
			t.Errorf("SafeDisplayName(%q) = %q, want %q", in, got, want)
		}
	}
}
