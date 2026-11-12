// Package workspace owns the on-disk data workspace layout and lifecycle.
// It is the single place that creates, inspects or refuses workspace
// directories; CLI and future adapters call it instead of touching paths.
package workspace

import (
	"os"
	"path/filepath"
	"sort"

	"repairflow/internal/config"
	"repairflow/internal/errs"
)

// Layout subdirectories (plan §3.1). Relative to the workspace root.
// files/ holds original attachment bytes (Stage 3); data/ holds SQLite (Stage 2).
var layoutDirs = []string{"data", "files", "backups", "logs", "cache", "run"}

const (
	dirPerms  = 0o700 // owner-only workspace directories
	filePerms = 0o600 // owner-only sensitive files
)

// InitResult describes what `repairflow init` did.
type InitResult struct {
	Home               string   `json:"home"`
	ConfigPath         string   `json:"config_path"`
	AlreadyInitialized bool     `json:"already_initialized"`
	CreatedDirs        []string `json:"created_dirs,omitempty"`
}

// Status describes an existing workspace without modifying it.
type Status struct {
	Home        string        `json:"home"`
	ConfigPath  string        `json:"config_path"`
	Initialized bool          `json:"initialized"`
	Config      config.Config `json:"config"`
}

// Init prepares a workspace at home with owner-only permissions.
//
// Behavior contract:
//   - creates the root and layout directories idempotently (0700), healing
//     missing layout directories on an existing workspace;
//   - writes config.toml only when absent (0600), never overwrites it;
//   - reports AlreadyInitialized when a valid config already exists;
//   - refuses a populated directory that has no config, a malformed config,
//     and any path failing the safety checks.
func Init(home string) (*InitResult, error) {
	if err := config.AssertSafeWorkspaceRoot(home); err != nil {
		return nil, err
	}

	result := &InitResult{Home: home, ConfigPath: filepath.Join(home, config.ConfigName)}
	info, statErr := os.Stat(home)
	exists := statErr == nil
	if statErr != nil && !os.IsNotExist(statErr) {
		return nil, errs.Wrap(errs.CodeUnsafePath, statErr, "cannot inspect workspace %q", home)
	}
	if exists {
		if !info.IsDir() {
			return nil, errs.New(errs.CodeUnsafePath, "refusing workspace %q: not a directory", home)
		}
		if _, err := os.Stat(result.ConfigPath); err == nil {
			if _, err := config.Load(result.ConfigPath); err != nil {
				return nil, err
			}
			// Already initialized: heal any missing layout directories below,
			// but never rewrite the config.
			result.AlreadyInitialized = true
		} else if !os.IsNotExist(err) {
			return nil, errs.Wrap(errs.CodeConfigInvalid, err, "cannot inspect %q", result.ConfigPath)
		} else {
			entries, err := os.ReadDir(home)
			if err != nil {
				return nil, errs.Wrap(errs.CodeInternal, err, "cannot read workspace %q", home)
			}
			if len(entries) > 0 {
				return nil, errs.New(errs.CodeWorkspaceExists, "refusing to initialize %q: directory is not empty and has no valid %s", home, config.ConfigName)
			}
		}
	}

	if !exists {
		if err := os.MkdirAll(home, dirPerms); err != nil {
			return nil, errs.Wrap(errs.CodeInternal, err, "cannot create workspace %q", home)
		}
	}
	for _, dir := range layoutDirs {
		full := filepath.Join(home, dir)
		info, err := os.Stat(full)
		if err == nil {
			if !info.IsDir() {
				return nil, errs.New(errs.CodeUnsafePath, "workspace layout path %q is not a directory", full)
			}
			continue
		}
		if !os.IsNotExist(err) {
			return nil, errs.Wrap(errs.CodeUnsafePath, err, "cannot inspect workspace directory %q", full)
		}
		if err := os.Mkdir(full, dirPerms); err != nil && !os.IsExist(err) {
			return nil, errs.Wrap(errs.CodeInternal, err, "cannot create workspace directory %q", full)
		}
		result.CreatedDirs = append(result.CreatedDirs, dir)
	}
	sort.Strings(result.CreatedDirs)

	if err := writeConfigIfAbsent(result.ConfigPath); err != nil {
		return nil, err
	}
	return result, nil
}

// Check validates the workspace and loads its effective configuration
// without modifying anything.
func Check(home string) (*Status, error) {
	if err := config.AssertSafeWorkspaceRoot(home); err != nil {
		return nil, err
	}
	status := &Status{Home: home, ConfigPath: filepath.Join(home, config.ConfigName)}
	cfg, err := config.Load(status.ConfigPath)
	if err != nil {
		return nil, err
	}
	status.Initialized = true
	status.Config = cfg
	return status, nil
}

// writeConfigIfAbsent writes the default config atomically and only when the
// file does not exist yet; existing configuration is never rewritten.
func writeConfigIfAbsent(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return errs.Wrap(errs.CodeInternal, err, "cannot inspect %q", path)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, config.Render(config.Default()), filePerms); err != nil {
		return errs.Wrap(errs.CodeInternal, err, "cannot write %q", tmp)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return errs.Wrap(errs.CodeInternal, err, "cannot publish %q", path)
	}
	return nil
}
