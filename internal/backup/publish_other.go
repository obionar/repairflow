//go:build !linux

package backup

// publishNoReplace uses an atomic hard link on platforms without renameat2.
// The project ships Linux binaries; this keeps the package buildable for test
// hosts and future ports without weakening the no-overwrite rule.
func publishNoReplace(temp, dest string) error {
	return publishByLink(temp, dest)
}
