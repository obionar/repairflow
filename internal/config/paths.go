package config

import (
	"os"
	"os/user"
	"path/filepath"
	"strings"

	"repairflow/internal/errs"
)

// Environment variables recognised for workspace selection and protection.
const (
	// EnvHome selects the workspace directory (overridden by --home).
	EnvHome = "REPAIRFLOW_HOME"
	// EnvRefusedRoots is an os.PathListSeparator-separated list of directory
	// roots that must never contain a RepairFlow workspace. It exists so an
	// operator or agent can protect unrelated production data (for example the
	// legacy Python RepairFlow installation) from accidental initialization.
	EnvRefusedRoots = "REPAIRFLOW_REFUSED_ROOTS"
)

// DefaultHomeName is the workspace directory name under the user's home.
// Candidate default workspace; keep user data in a per-user location.
const DefaultHomeName = ".repairflow"

// ResolveHome resolves the workspace root with the documented precedence:
// explicit flag, then REPAIRFLOW_HOME, then $HOME/.repairflow. refusedRoots is
// the raw os.PathListSeparator-separated refusal list (normally from
// REPAIRFLOW_REFUSED_ROOTS).
//
// The result is a cleaned absolute path with symlinks resolved on the
// existing prefix. Relative paths, "~user" forms, and paths inside a refused
// root are rejected; no directory is created here.
func ResolveHome(flagValue, envValue, refusedRoots string) (string, error) {
	raw := strings.TrimSpace(flagValue)
	if raw == "" {
		raw = strings.TrimSpace(envValue)
	}
	if raw == "" {
		home, err := defaultHomeDir()
		if err != nil {
			return "", err
		}
		raw = filepath.Join(home, DefaultHomeName)
	}

	expanded, err := expandHome(raw)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(expanded) {
		return "", errs.New(errs.CodeValidation, "workspace path must be absolute (got %q)", raw)
	}
	clean := filepath.Clean(expanded)

	resolved, err := resolveExistingPrefix(clean)
	if err != nil {
		return "", errs.Wrap(errs.CodeUnsafePath, err, "cannot resolve workspace path %q", clean)
	}
	if err := checkRefusedRoots(resolved, refusedRoots); err != nil {
		return "", err
	}
	return resolved, nil
}

// defaultHomeDir is the home directory used when neither --home nor
// REPAIRFLOW_HOME selects a workspace. Under sudo/doas the process is root and
// HOME points at root's own directory, so the invoking user's home is used
// instead: a unit written for /root/.repairflow points at a workspace that does
// not exist and the service fails to start.
func defaultHomeDir() (string, error) {
	home, handled, err := escalationHome(os.Geteuid(), os.Getenv, user.Lookup)
	if err != nil {
		return "", err
	}
	if handled {
		return home, nil
	}
	dir, err := os.UserHomeDir()
	if err != nil {
		return "", errs.Wrap(errs.CodeValidation, err, "cannot determine the user home directory for the default workspace")
	}
	return dir, nil
}

// escalationHome returns the invoking user's home when this process is root
// only because of sudo/doas. handled=false means "not escalated": use the
// process home.
func escalationHome(euid int, getenv func(string) string, lookup func(string) (*user.User, error)) (string, bool, error) {
	if euid != 0 {
		return "", false, nil
	}
	for _, key := range []string{"SUDO_USER", "DOAS_USER"} {
		name := strings.TrimSpace(getenv(key))
		if name == "" || name == "root" {
			continue
		}
		u, lookupErr := lookup(name)
		if lookupErr != nil || u.HomeDir == "" {
			return "", true, errs.New(errs.CodeValidation,
				"running as root via %s but the home directory of %q is unknown; pass --home explicitly", key, name)
		}
		return u.HomeDir, true, nil
	}
	return "", false, nil
}

// AssertSafeWorkspaceRoot validates an existing or to-be-created workspace
// root. It refuses files, unsafe ownership/permissions, and hard-linked
// files that could alias unrelated data. A missing path is acceptable here;
// creation is the workspace package's job.
func AssertSafeWorkspaceRoot(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return errs.Wrap(errs.CodeUnsafePath, err, "cannot inspect workspace path %q", path)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errs.New(errs.CodeUnsafePath, "refusing workspace path %q: symbolic links are not accepted as the workspace root", path)
	}
	if !info.IsDir() {
		if nlink, ok := fileLinks(info); ok && nlink > 1 {
			return errs.New(errs.CodeUnsafePath, "refusing %q: the file has %d hard links, so it may alias unrelated data", path, nlink)
		}
		return errs.New(errs.CodeUnsafePath, "refusing workspace path %q: not a directory", path)
	}
	if info.Mode().Perm()&0o022 != 0 {
		return errs.New(errs.CodePermission, "refusing workspace %q: directory is group- or world-writable (mode %04o)", path, info.Mode().Perm())
	}
	return assertOwned(path, info)
}

// expandHome expands a leading "~" or "~/..." form using the current user's
// home directory. "~user" is deliberately unsupported.
func expandHome(raw string) (string, error) {
	if raw == "~" || strings.HasPrefix(raw, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", errs.Wrap(errs.CodeValidation, err, "cannot expand ~ in workspace path %q", raw)
		}
		if raw == "~" {
			return home, nil
		}
		return filepath.Join(home, strings.TrimPrefix(raw, "~/")), nil
	}
	if strings.HasPrefix(raw, "~") {
		return "", errs.New(errs.CodeValidation, "refusing workspace path %q: ~user forms are not supported, use an absolute path", raw)
	}
	return raw, nil
}

// resolveExistingPrefix resolves symlinks on the longest existing prefix of
// path and re-appends the missing remainder, so decisions are made about the
// real on-disk location.
func resolveExistingPrefix(path string) (string, error) {
	remainder := ""
	current := path
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			if remainder == "" {
				return resolved, nil
			}
			return filepath.Join(resolved, remainder), nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			// Nothing exists above; keep the cleaned path as-is.
			return path, nil
		}
		if remainder == "" {
			remainder = filepath.Base(current)
		} else {
			remainder = filepath.Join(filepath.Base(current), remainder)
		}
		current = parent
	}
}

// checkRefusedRoots rejects any path equal to or inside a refused root.
func checkRefusedRoots(path, rawRoots string) error {
	for _, raw := range strings.Split(rawRoots, string(os.PathListSeparator)) {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		expanded, err := expandHome(raw)
		if err != nil {
			return err
		}
		if !filepath.IsAbs(expanded) {
			return errs.New(errs.CodeValidation, "refused root %q must be an absolute path", raw)
		}
		root, err := resolveExistingPrefix(filepath.Clean(expanded))
		if err != nil {
			return errs.Wrap(errs.CodeUnsafePath, err, "cannot resolve refused root %q", raw)
		}
		if path == root || strings.HasPrefix(path, root+string(filepath.Separator)) {
			return errs.New(errs.CodeUnsafePath, "refusing workspace %q: inside refused root %q (set by %s)", path, root, EnvRefusedRoots)
		}
	}
	return nil
}
