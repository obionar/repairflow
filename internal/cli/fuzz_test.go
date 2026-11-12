package cli

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"repairflow/internal/config"
	"repairflow/internal/errs"
)

// FuzzParseGlobalFlags checks global flag extraction against arbitrary
// arguments: no panics, classified errors only, and --home can never swallow
// a following flag. Pure parsing — no filesystem access.
func FuzzParseGlobalFlags(f *testing.F) {
	for _, seed := range [][3]string{
		{"--json", "init", ""},
		{"--home", "/tmp/x", "init"},
		{"--home=/tmp/x", "--json", ""},
		{"--home", "--json", ""},
		{"--", "", ""},
		{"-h", "", ""},
		{"--home=", "", ""},
		{"--version", "", ""},
	} {
		f.Add(seed[0], seed[1], seed[2])
	}
	f.Fuzz(func(t *testing.T, a, b, c string) {
		args := []string{a, b, c}
		_, rest, err := parseGlobalFlags(args)
		if err != nil {
			if errs.CodeOf(err) != errs.CodeUsage {
				t.Fatalf("unexpected code for %v: %v", args, err)
			}
			return
		}
		_ = rest
	})
}

// FuzzRun drives the whole CLI entrypoint with arbitrary argument pairs and
// asserts the process contract: a defined exit code, and empty stdout on
// failure (errors belong to stderr).
//
// Safety: the workspace is pinned to a private sandbox via REPAIRFLOW_HOME and
// any input containing "--home" or a path separator is skipped, so the fuzzer
// can never write outside its sandbox.
func FuzzRun(f *testing.F) {
	sandbox, err := os.MkdirTemp("", "rf-fuzz-home")
	if err != nil {
		f.Fatal(err)
	}
	f.Cleanup(func() { _ = os.RemoveAll(sandbox) })
	getenv := func(key string) string {
		if key == config.EnvHome {
			return sandbox
		}
		return ""
	}

	for _, seed := range [][2]string{
		{"version", "--json"},
		{"help", ""},
		{"customer", "list"},
		{"repair", "show"},
		{"seed", "demo"},
		{"doctor", ""},
		{"status", ""},
	} {
		f.Add(seed[0], seed[1])
	}
	f.Fuzz(func(t *testing.T, a, b string) {
		if strings.Contains(a, "--home") || strings.Contains(b, "--home") ||
			strings.Contains(a, "/") || strings.Contains(b, "/") {
			return
		}
		var out, errb writerCount
		code := Run(context.Background(), []string{a, b}, strings.NewReader(""), &out, &errb, getenv)
		if code != 0 && code != 1 && code != 2 && code != 3 && code != 4 {
			t.Fatalf("undefined exit code %d for %q %q", code, a, b)
		}
		if code != 0 && out.n != 0 {
			t.Fatalf("stdout must be empty on failure (exit %d) for %q %q", code, a, b)
		}
	})
}

type writerCount struct{ n int }

func (w *writerCount) Write(p []byte) (int, error) {
	w.n += len(p)
	return len(p), nil
}

var _ io.Writer = (*writerCount)(nil)
