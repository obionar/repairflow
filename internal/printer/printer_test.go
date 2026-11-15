package printer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"repairflow/internal/errs"
)

// fakeP15 puts a stand-in p15 on PATH so the driver contract is tested without
// a printer, Bluetooth or root capabilities.
func fakeP15(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, DriverP15)
	if err := os.WriteFile(script, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
}

func TestStickerArgs(t *testing.T) {
	// p15 adds the '#', so the raw repair number is passed; an unassigned
	// repair keeps the (empty) name argument so the number prints alone.
	cases := []struct {
		name string
		id   int64
		want []string
	}{
		{"assigned", 83, []string{"repair", "83", "Maria Pop"}},
		{"unassigned", 12, []string{"repair", "12", ""}},
		{"padded name", 7, []string{"repair", "7", "Ada Lovelace"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := stickerArgs(tc.id, "  "+tc.want[2]+" ")
			if strings.Join(got, "|") != strings.Join(tc.want, "|") {
				t.Fatalf("stickerArgs = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestStickerRunsDriver(t *testing.T) {
	argFile := filepath.Join(t.TempDir(), "args")
	t.Setenv("ARGS_FILE", argFile)
	fakeP15(t, `printf '%s\n' "$@" > "$ARGS_FILE"`+"\n")

	if err := Sticker(context.Background(), DriverP15, 83, "Maria Pop"); err != nil {
		t.Fatalf("Sticker: %v", err)
	}
	out, err := os.ReadFile(argFile)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Split(strings.TrimSpace(string(out)), "\n"); strings.Join(got, "|") != "repair|83|Maria Pop" {
		t.Fatalf("driver arguments = %q", got)
	}
}

func TestStickerReportsDriverFailure(t *testing.T) {
	fakeP15(t, "echo 'p15: no printer found' >&2\nexit 1\n")

	err := Sticker(context.Background(), DriverP15, 83, "Maria Pop")
	if errs.CodeOf(err) != errs.CodeConflict {
		t.Fatalf("code = %s, want conflict (err %v)", errs.CodeOf(err), err)
	}
	if !strings.Contains(err.Error(), "no printer found") {
		t.Fatalf("error must carry the driver's reason, got %q", err.Error())
	}
}

func TestUnknownDriverRefused(t *testing.T) {
	if err := Sticker(context.Background(), "hp2000", 1, "x"); errs.CodeOf(err) != errs.CodeValidation {
		t.Fatalf("code = %s, want validation", errs.CodeOf(err))
	}
	if Known("") || !Known(DriverP15) {
		t.Fatal("Known must accept only the configured drivers")
	}
}

func TestFailureTextIsBoundedAndSanitised(t *testing.T) {
	long := strings.Repeat("x", 400)
	got := failureText([]byte("progress\n\x1b[31mfailed: "+long+"\n\n"), nil)
	if strings.ContainsRune(got, 0x1b) {
		t.Fatalf("control character survived: %q", got)
	}
	if len([]rune(got)) > maxMessage {
		t.Fatalf("message is not bounded: %d runes", len([]rune(got)))
	}
}

// A cancelled request context must not kill a print: the label may already be
// feeding, and a second sticker would duplicate it.
func TestStickerIgnoresRequestCancellation(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "printed")
	t.Setenv("MARKER", marker)
	fakeP15(t, "sleep 0.3\nprintf done > \"$MARKER\"\n")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Sticker(ctx, DriverP15, 83, "Maria Pop"); err != nil {
		t.Fatalf("Sticker with cancelled request context: %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("driver did not finish the print: %v", err)
	}
}
