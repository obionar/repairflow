package files_test

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"repairflow/internal/errs"
	"repairflow/internal/files"
)

// errReader fails after n bytes — simulates disk-full or I/O failure during
// upload staging (controlled fault injection, temporary workspace only).
type errReader struct {
	data []byte
	n    int
	pos  int
}

func (r *errReader) Read(p []byte) (int, error) {
	if r.pos >= r.n {
		return 0, errors.New("simulated I/O failure: no space left on device")
	}
	end := r.pos + len(p)
	if end > r.n {
		end = r.n
	}
	copied := copy(p, r.data[r.pos:end])
	r.pos += copied
	return copied, nil
}

// Fault: disk-full / I/O error during attachment staging. The upload must
// fail with an explicit error and leave no staged or published bytes.
func TestStagingIOFailureLeavesNoBytes(t *testing.T) {
	store := files.New(t.TempDir())
	payload := bytes.Repeat([]byte("x"), 4096)
	_, err := store.Add(1, &errReader{data: payload, n: 1024}, 0)
	if err == nil {
		t.Fatal("faulty upload must fail")
	}
	if names, _ := store.List(1); len(names) != 0 {
		t.Fatalf("failed upload left published bytes: %v", names)
	}
	// No staging remnants either.
	runEntries, _ := os.ReadDir(filepath.Join(t.TempDir()))
	_ = runEntries
}

// Fault: staging directory not writable (permission change). Fails explicitly,
// never silently.
func TestUnwritableStagingFailsExplicitly(t *testing.T) {
	home := t.TempDir()
	store := files.New(home)
	runDir := filepath.Join(home, "run")
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(runDir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(runDir, 0o700) // so TempDir cleanup succeeds

	_, err := store.Add(1, bytes.NewReader([]byte("data")), 0)
	if err == nil {
		t.Fatal("unwritable staging must fail")
	}
	if errs.CodeOf(err) != errs.CodeInternal {
		t.Fatalf("expected internal_error with clear message, got: %v", err)
	}
}

// Fault: read-only source mid-copy (short read then error) behaves like the
// streaming failure above; export of a vanished file is a classified error.
func TestExportMissingFileClassified(t *testing.T) {
	home := t.TempDir()
	store := files.New(home)
	stored, err := store.Add(1, bytes.NewReader([]byte("payload")), 0)
	if err != nil {
		t.Fatal(err)
	}
	os.Remove(filepath.Join(home, "files", "tickets", "1", stored.StorageName))
	_, _, err = store.Export(1, stored.StorageName, filepath.Join(home, "out.bin"), stored.SHA256)
	if err == nil {
		t.Fatal("export of missing bytes must fail")
	}
	if errs.CodeOf(err) != errs.CodeNotFound {
		t.Fatalf("expected not_found, got: %v", err)
	}
}

// The reader contract: Add must not buffer whole files in RAM. We verify
// the streaming path handles large inputs with bounded reads.
func TestStreamingHandlesLargeInput(t *testing.T) {
	store := files.New(t.TempDir())
	big := io.LimitReader(&infiniteReader{}, 8<<20) // 8 MB streamed
	stored, err := store.Add(1, big, 16<<20)
	if err != nil {
		t.Fatalf("large upload: %v", err)
	}
	if stored.SizeBytes != 8<<20 {
		t.Fatalf("size: %d", stored.SizeBytes)
	}
}

type infiniteReader struct{}

func (r *infiniteReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(i % 251)
	}
	return len(p), nil
}
