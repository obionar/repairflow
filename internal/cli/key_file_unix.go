//go:build unix

package cli

import (
	"fmt"
	"os"
	"syscall"
)

func verifyOwnedDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("stat key directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("key output is not a real directory: %s", path)
	}
	if got := info.Mode().Perm(); got&0o022 != 0 {
		return fmt.Errorf("key directory %s is group/world-writable (mode %04o)", path, got)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Getuid() {
		return fmt.Errorf("key directory %s is not owned by the current user", path)
	}
	return nil
}

func verifyOwnedRegularFile(path string, want os.FileMode) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("stat key file: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("key path is not a regular file: %s", path)
	}
	if got := info.Mode().Perm(); got != want {
		return fmt.Errorf("key file %s has mode %04o, want %04o", path, got, want)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Getuid() {
		return fmt.Errorf("key file %s is not owned by the current user", path)
	}
	return nil
}
