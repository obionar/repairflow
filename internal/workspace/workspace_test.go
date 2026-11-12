package workspace

import (
	"os"
	"path/filepath"
	"testing"

	"repairflow/internal/config"
	"repairflow/internal/errs"
)

func TestInitCreatesOwnerOnlyLayout(t *testing.T) {
	home := filepath.Join(t.TempDir(), "ws")
	res, err := Init(home)
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	if res.AlreadyInitialized {
		t.Fatal("first init must not report already initialized")
	}

	info, err := os.Stat(home)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Errorf("workspace perms = %04o, want 0700", info.Mode().Perm())
	}
	for _, dir := range layoutDirs {
		st, err := os.Stat(filepath.Join(home, dir))
		if err != nil {
			t.Fatalf("layout dir %s missing: %v", dir, err)
		}
		if st.Mode().Perm() != 0o700 {
			t.Errorf("dir %s perms = %04o, want 0700", dir, st.Mode().Perm())
		}
	}
	cfgInfo, err := os.Stat(res.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfgInfo.Mode().Perm() != 0o600 {
		t.Errorf("config perms = %04o, want 0600", cfgInfo.Mode().Perm())
	}
	if _, err := config.Load(res.ConfigPath); err != nil {
		t.Fatalf("written config must load: %v", err)
	}
}

func TestInitIsIdempotentAndNeverRewritesConfig(t *testing.T) {
	home := filepath.Join(t.TempDir(), "ws")
	if _, err := Init(home); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(home, config.ConfigName)
	custom := []byte("config_version = 1\n[app]\nenvironment = \"production\"\n")
	if err := os.WriteFile(cfgPath, custom, 0o600); err != nil {
		t.Fatal(err)
	}

	res, err := Init(home)
	if err != nil {
		t.Fatalf("second init must succeed idempotently: %v", err)
	}
	if !res.AlreadyInitialized {
		t.Fatal("second init must report already initialized")
	}
	body, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != string(custom) {
		t.Fatal("init must never rewrite an existing config file")
	}
}

func TestInitRefusals(t *testing.T) {
	base := t.TempDir()

	// Populated directory without config: refuse, leave untouched.
	populated := filepath.Join(base, "populated")
	if err := os.Mkdir(populated, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(populated, "unrelated.db"), []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Init(populated)
	if err == nil {
		t.Fatal("expected refusal over populated directory")
	}
	if errs.CodeOf(err) != errs.CodeWorkspaceExists {
		t.Fatalf("expected workspace_exists, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(populated, "data")); statErr == nil {
		t.Fatal("refused init must not create layout directories")
	}

	// Malformed config: refuse.
	broken := filepath.Join(base, "broken")
	if err := os.Mkdir(broken, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(broken, config.ConfigName), []byte("not toml at all\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Init(broken); err == nil {
		t.Fatal("expected refusal of malformed config")
	} else if errs.CodeOf(err) != errs.CodeConfigInvalid {
		t.Fatalf("expected config_invalid, got %v", err)
	}

	// Plain file instead of a directory: refuse.
	file := filepath.Join(base, "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Init(file); err == nil {
		t.Fatal("expected refusal of file path")
	} else if errs.CodeOf(err) != errs.CodeUnsafePath {
		t.Fatalf("expected unsafe_path, got %v", err)
	}
}

func TestInitHealsMissingLayoutDirs(t *testing.T) {
	home := filepath.Join(t.TempDir(), "ws")
	if _, err := Init(home); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(home, "cache")); err != nil {
		t.Fatal(err)
	}

	res, err := Init(home)
	if err != nil {
		t.Fatalf("init must heal missing layout dirs: %v", err)
	}
	if !res.AlreadyInitialized {
		t.Fatal("still an initialized workspace")
	}
	if len(res.CreatedDirs) != 1 || res.CreatedDirs[0] != "cache" {
		t.Fatalf("healed dirs: %v", res.CreatedDirs)
	}
	if _, err := os.Stat(filepath.Join(home, "cache")); err != nil {
		t.Fatalf("cache not restored: %v", err)
	}
}

func TestInitRefusesNonDirectoryLayoutPath(t *testing.T) {
	home := filepath.Join(t.TempDir(), "ws")
	if _, err := Init(home); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(home, "cache")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "cache"), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Init(home); errs.CodeOf(err) != errs.CodeUnsafePath {
		t.Fatalf("file at cache path must be refused: %v", err)
	}
}

func TestCheckRequiresInitializedWorkspace(t *testing.T) {
	base := t.TempDir()

	if _, err := Check(filepath.Join(base, "missing")); err == nil {
		t.Fatal("expected missing workspace to fail")
	} else if errs.CodeOf(err) != errs.CodeNotInitialized {
		t.Fatalf("expected workspace_not_initialized, got %v", err)
	}

	home := filepath.Join(base, "ws")
	if _, err := Init(home); err != nil {
		t.Fatal(err)
	}
	status, err := Check(home)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if !status.Initialized || status.Config.ConfigVersion != config.CurrentConfigVersion {
		t.Fatalf("unexpected status: %+v", status)
	}
}
