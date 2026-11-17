//go:build !unix

package workspace

import (
	"context"

	"repairflow/internal/errs"
)

// Lock is currently implemented only on Unix. Windows is not a supported
// runtime target; refusing explicitly is safer than an unprotected migration.
func Lock(context.Context, string, bool) (func(), error) {
	return nil, errs.New(errs.CodeInternal,
		"workspace locking is unavailable on this platform; use a Linux build")
}
