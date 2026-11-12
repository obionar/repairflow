package errs

import (
	"fmt"
	"testing"
)

// Regression tests for audit findings B-01: error-code extraction must see
// through wrapped errors so the machine contract cannot silently degrade to
// internal_error.

func TestCodeOfSeesThroughWrapping(t *testing.T) {
	base := New(CodeConflict, "stale write")
	if CodeOf(base) != CodeConflict {
		t.Fatalf("direct: %v", CodeOf(base))
	}
	wrapped := fmt.Errorf("context: %w", base)
	if CodeOf(wrapped) != CodeConflict {
		t.Fatalf("wrapped code must survive, got %v", CodeOf(wrapped))
	}
	if ExitCode(wrapped) != 3 {
		t.Fatalf("wrapped exit code: %d", ExitCode(wrapped))
	}
	if CodeOf(fmt.Errorf("plain")) != CodeInternal {
		t.Fatal("plain errors must map to internal_error")
	}
}

func TestExitCodeTable(t *testing.T) {
	cases := map[Code]int{
		CodeUsage: 2, CodeValidation: 2, CodeUnsafePath: 2, CodePermission: 2,
		CodeWorkspaceExists: 3, CodeNotInitialized: 3, CodeConfigInvalid: 3,
		CodeConfigVersion: 3, CodeConflict: 3,
		CodeNotFound: 4,
		CodeInternal: 1,
	}
	for code, want := range cases {
		if got := ExitCode(New(code, "x")); got != want {
			t.Errorf("%s: exit %d want %d", code, got, want)
		}
	}
	if ExitCode(nil) != 1 {
		t.Error("nil error must map to exit 1")
	}
}

func TestWrapKeepsCause(t *testing.T) {
	cause := fmt.Errorf("disk on fire")
	err := Wrap(CodeInternal, cause, "cannot write %s", "db")
	if err.Unwrap() != cause {
		t.Fatal("cause must be unwrap-able")
	}
	if err.Error() != "cannot write db: disk on fire" {
		t.Fatalf("message: %q", err.Error())
	}
}
