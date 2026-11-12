package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"repairflow/internal/config"
	"repairflow/internal/errs"
)

// Cleanup removes disposable workspace content using the retention
// definition:
//
//   - run/: interrupted-upload staging files (*.part) older than one hour.
//     Fresh staging files belong to in-flight operations and are kept.
//   - cache/: regenerable content, removable at any time.
//
// User data is NEVER touched: data/ (database), files/ (attachment originals),
// config.toml, backups/ and logs/ are out of scope by definition. Anything
// unrecognized is left in place and reported.

// CleanupResult reports what cleanup removed.
type CleanupResult struct {
	StagingRemoved []string `json:"staging_removed"`
	CacheRemoved   []string `json:"cache_removed"`
	Skipped        []string `json:"skipped,omitempty"`
}

// StagingRetention is how long a staged upload file is kept before cleanup
// may remove it (protects in-flight operations).
const StagingRetention = time.Hour

// Cleanup removes disposable files from home. It is safe to run at any time.
func Cleanup(home string) (*CleanupResult, error) {
	if err := config.AssertSafeWorkspaceRoot(home); err != nil {
		return nil, err
	}
	res := &CleanupResult{StagingRemoved: []string{}, CacheRemoved: []string{}, Skipped: []string{}}
	cutoff := time.Now().Add(-StagingRetention)

	runDir := filepath.Join(home, "run")
	entries, err := os.ReadDir(runDir)
	if err != nil && !os.IsNotExist(err) {
		return nil, errs.Wrap(errs.CodeInternal, err, "cannot read run directory")
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		full := filepath.Join(runDir, name)
		if name == "purge.lock" {
			continue // purge/recovery serialization lock; never diagnostic noise
		}
		if !strings.HasSuffix(name, ".part") {
			res.Skipped = append(res.Skipped, filepath.Join("run", name))
			continue
		}
		info, err := e.Info()
		if err != nil || info.ModTime().After(cutoff) {
			res.Skipped = append(res.Skipped, filepath.Join("run", name))
			continue
		}
		if err := os.Remove(full); err != nil {
			return nil, errs.Wrap(errs.CodeInternal, err, "cannot remove staging file %q", full)
		}
		res.StagingRemoved = append(res.StagingRemoved, filepath.Join("run", name))
	}

	cacheDir := filepath.Join(home, "cache")
	entries, err = os.ReadDir(cacheDir)
	if err != nil && !os.IsNotExist(err) {
		return nil, errs.Wrap(errs.CodeInternal, err, "cannot read cache directory")
	}
	for _, e := range entries {
		full := filepath.Join(cacheDir, e.Name())
		if err := os.RemoveAll(full); err != nil {
			return nil, errs.Wrap(errs.CodeInternal, err, "cannot remove cache entry %q", full)
		}
		res.CacheRemoved = append(res.CacheRemoved, filepath.Join("cache", e.Name()))
	}
	return res, nil
}
