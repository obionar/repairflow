//go:build !unix

package config

import "os"

// fileLinks is unavailable off Unix; hard-link aliasing checks are skipped.
func fileLinks(info os.FileInfo) (uint64, bool) { return 0, false }

// assertOwned is unavailable off Unix; ownership checks are skipped.
func assertOwned(path string, info os.FileInfo) error { return nil }
