package config

import (
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"

	"repairflow/internal/errs"
)

func TestResolveHomePrecedence(t *testing.T) {
	base := t.TempDir()
	flagPath := filepath.Join(base, "flag-home")
	envPath := filepath.Join(base, "env-home")

	got, err := ResolveHome(flagPath, envPath, "")
	if err != nil {
		t.Fatalf("flag resolve: %v", err)
	}
	if got != flagPath {
		t.Fatalf("flag must win: got %q want %q", got, flagPath)
	}

	got, err = ResolveHome("", envPath, "")
	if err != nil {
		t.Fatalf("env resolve: %v", err)
	}
	if got != envPath {
		t.Fatalf("env must win over default: got %q want %q", got, envPath)
	}

	got, err = ResolveHome("", "", "")
	if err != nil {
		t.Fatalf("default resolve: %v", err)
	}
	resolvedHome, err := filepath.EvalSymlinks(mustUserHome(t))
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(resolvedHome, DefaultHomeName)
	if got != want {
		t.Fatalf("default home: got %q want %q", got, want)
	}
}

func TestResolveHomeRejectsRelative(t *testing.T) {
	if _, err := ResolveHome("relative/path", "", ""); err == nil {
		t.Fatal("expected relative path to be refused")
	} else if errs.CodeOf(err) != errs.CodeValidation {
		t.Fatalf("expected validation_error, got %v", err)
	}
}

func TestResolveHomeRejectsTildeUser(t *testing.T) {
	if _, err := ResolveHome("~someone/else", "", ""); err == nil {
		t.Fatal("expected ~user form to be refused")
	}
}

func TestResolveHomeExpandsTilde(t *testing.T) {
	got, err := ResolveHome("~/repairflow-test", "", "")
	if err != nil {
		t.Fatalf("tilde expand: %v", err)
	}
	want := filepath.Join(mustUserHome(t), "repairflow-test")
	if got != want {
		t.Fatalf("tilde expansion: got %q want %q", got, want)
	}
}

func TestResolveHomeRefusedRoots(t *testing.T) {
	base := t.TempDir()
	if _, err := ResolveHome(filepath.Join(base, "inside"), "", base); err == nil {
		t.Fatal("expected refused root to be enforced")
	} else if errs.CodeOf(err) != errs.CodeUnsafePath {
		t.Fatalf("expected unsafe_path, got %v", err)
	}
	// A sibling outside the refused root must still resolve.
	sibling := filepath.Join(filepath.Dir(base), "sibling-workspace")
	if _, err := ResolveHome(sibling, "", base); err != nil {
		t.Fatalf("sibling path must be allowed: %v", err)
	}
}

func TestAssertSafeWorkspaceRoot(t *testing.T) {
	base := t.TempDir()

	// Missing path is fine; creation happens elsewhere.
	if err := AssertSafeWorkspaceRoot(filepath.Join(base, "missing")); err != nil {
		t.Fatalf("missing path must be acceptable: %v", err)
	}

	// Plain file is refused.
	file := filepath.Join(base, "afile")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := AssertSafeWorkspaceRoot(file); err == nil {
		t.Fatal("expected file path to be refused")
	}

	// Hard-linked file is refused with unsafe_path.
	linked := filepath.Join(base, "linked")
	if err := os.Link(file, linked); err != nil {
		t.Fatal(err)
	}
	if err := AssertSafeWorkspaceRoot(linked); err == nil {
		t.Fatal("expected hard-linked file to be refused")
	} else if errs.CodeOf(err) != errs.CodeUnsafePath {
		t.Fatalf("expected unsafe_path, got %v", err)
	}

	// World-writable directory is refused.
	loose := filepath.Join(base, "loose")
	if err := os.Mkdir(loose, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(loose, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := AssertSafeWorkspaceRoot(loose); err == nil {
		t.Fatal("expected world-writable directory to be refused")
	} else if errs.CodeOf(err) != errs.CodePermission {
		t.Fatalf("expected permission_refused, got %v", err)
	}

	// Owner-only directory is fine.
	good := filepath.Join(base, "good")
	if err := os.Mkdir(good, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := AssertSafeWorkspaceRoot(good); err != nil {
		t.Fatalf("owner-only directory must be accepted: %v", err)
	}

	// Symlink as the workspace root is refused.
	link := filepath.Join(base, "link")
	if err := os.Symlink(good, link); err != nil {
		t.Fatal(err)
	}
	if err := AssertSafeWorkspaceRoot(link); err == nil {
		t.Fatal("expected symlinked root to be refused")
	}
}

func mustUserHome(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	return home
}

func TestEscalationHomeUsesInvokingUser(t *testing.T) {
	lookup := func(name string) (*user.User, error) {
		return &user.User{Username: name, HomeDir: "/home/" + name}, nil
	}
	getenv := func(key string) string {
		if key == "DOAS_USER" {
			return "tester"
		}
		return ""
	}
	home, handled, err := escalationHome(0, getenv, lookup)
	if err != nil || !handled || home != "/home/tester" {
		t.Fatalf("escalated home = %q handled=%v err=%v", home, handled, err)
	}
}

func TestEscalationHomeUnknownUserRefuses(t *testing.T) {
	lookup := func(string) (*user.User, error) { return nil, os.ErrNotExist }
	_, handled, err := escalationHome(0, func(k string) string { return map[string]string{"SUDO_USER": "ghost"}[k] }, lookup)
	if !handled || err == nil || !strings.Contains(err.Error(), "pass --home") {
		t.Fatalf("want a --home refusal, got handled=%v err=%v", handled, err)
	}
}

func TestEscalationHomeNotRoot(t *testing.T) {
	_, handled, err := escalationHome(1000, func(string) string { return "ian" }, nil)
	if handled || err != nil {
		t.Fatalf("a non-root process must use its own home: handled=%v err=%v", handled, err)
	}
}

func TestEscalationHomeRootWithoutMarkers(t *testing.T) {
	_, handled, err := escalationHome(0, func(string) string { return "" }, nil)
	if handled || err != nil {
		t.Fatalf("a plain root shell keeps /root: handled=%v err=%v", handled, err)
	}
}
