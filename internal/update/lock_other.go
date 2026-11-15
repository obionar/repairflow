//go:build !linux

package update

// Verified update execution is Linux-only; Apply refuses before reaching this
// helper on other platforms. Keeping the build surface explicit avoids a
// second partial implementation.
func lockInstallTarget(string) (func(), error) { return func() {}, nil }
