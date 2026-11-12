//go:build linux

package update_test

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"repairflow/internal/errs"
	"repairflow/internal/update"
)

func writeFIFO(t *testing.T, path string, payload []byte, delay time.Duration) {
	t.Helper()
	go func() {
		time.Sleep(delay)
		fd, err := syscall.Open(path, syscall.O_RDWR|syscall.O_NONBLOCK, 0)
		if err != nil {
			return
		}
		f := os.NewFile(uintptr(fd), path)
		_, _ = f.Write(payload)
		_ = f.Close()
	}()
}

func TestApplyRejectsFIFOArtifact(t *testing.T) {
	dir := t.TempDir()
	target := writeScript(t, dir, "repairflow", "1.0.0")
	r := makeRelease(t, dir, "1.1.0", runtime_GOOS, runtime_GOARCH, "1.1.0")
	expected, err := os.ReadFile(r.artifact)
	if err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(dir, "artifact.fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	// The vulnerable implementation consumed this stream twice: once to hash
	// the path and once to reopen/copy it. Both writers make that path finish;
	// the fixed implementation rejects the FIFO before reading either stream.
	writeFIFO(t, fifo, expected, 200*time.Millisecond)
	writeFIFO(t, fifo, expected, 500*time.Millisecond)
	r.artifact = fifo

	if _, err := update.Apply(context.Background(), applyOpts(r, target, "1.0.0")); errs.CodeOf(err) != errs.CodeValidation {
		t.Fatalf("FIFO artifact must be refused as validation error, got %v", err)
	}
	out, _ := execOutput(t, target)
	if !contains(out, "1.0.0") {
		t.Fatalf("refused FIFO update modified target: %s", out)
	}
}
