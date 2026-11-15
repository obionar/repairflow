//go:build !linux

package backup

// publishNoReplace uses the portable checked rename on platforms without
// renameat2. The project ships Linux binaries; this keeps the package buildable
// for test hosts and future ports.
func publishNoReplace(temp, dest string) error {
	return publishByRename(temp, dest)
}
