// Package errs defines the stable error codes shared by every adapter
// (CLI today; web and MCP later). Machine-readable interfaces expose the
// code string; humans see the message. Exit-code mapping lives here so the
// CLI contract cannot drift from the error taxonomy.
//
// Typed errors are shared across
// DB/domain/CLI/web). The code set grows with the domain; existing code
// strings are a contract and must not change meaning.
package errs

import (
	"errors"
	"fmt"
)

// Code is a stable, machine-readable error identifier.
type Code string

// Stable error codes. See docs/CLI-CONTRACT.md for the contract.
const (
	CodeUsage           Code = "usage_error"                // bad flags/arguments (exit 2)
	CodeValidation      Code = "validation_error"           // invalid input values (exit 2)
	CodeUnsafePath      Code = "unsafe_path"                // refused path/workspace location (exit 2)
	CodePermission      Code = "permission_refused"         // unsafe ownership or permissions (exit 2)
	CodeWorkspaceExists Code = "workspace_exists"           // refusing to init over existing data (exit 3)
	CodeNotInitialized  Code = "workspace_not_initialized"  // workspace/config missing (exit 3)
	CodeConfigInvalid   Code = "config_invalid"             // config file malformed or invalid (exit 3)
	CodeConfigVersion   Code = "config_version_unsupported" // config from a newer/unknown format (exit 3)
	CodeConflict        Code = "conflict"                   // concurrent/stale state conflict (exit 3)
	CodeNotFound        Code = "not_found"                  // referenced record does not exist (exit 4)
	CodeInternal        Code = "internal_error"             // unexpected failure (exit 1)
)

// Error is an expected, classified failure. Unexpected bugs should return
// plain errors; the CLI reports those as internal_error.
type Error struct {
	Code    Code
	Message string
	Cause   error
}

// New builds a classified error with a formatted message.
func New(code Code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// Wrap attaches a cause to a classified error.
func Wrap(code Code, cause error, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...), Cause: cause}
}

func (e *Error) Error() string {
	if e.Cause != nil {
		return e.Message + ": " + e.Cause.Error()
	}
	return e.Message
}

// Unwrap exposes the cause to errors.Is/As.
func (e *Error) Unwrap() error { return e.Cause }

// CodeOf extracts the stable code from an error, defaulting to internal_error.
// Wrapped errors are unwrapped, so fmt.Errorf("%w") at call sites cannot break
// the machine contract.
func CodeOf(err error) Code {
	if err == nil {
		return ""
	}
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return CodeInternal
}

// ExitCode maps an error to the process exit code documented in
// docs/CLI-CONTRACT.md: 0 success, 1 internal, 2 usage/validation/permission,
// 3 state conflict, 4 not found.
func ExitCode(err error) int {
	switch CodeOf(err) {
	case CodeUsage, CodeValidation, CodeUnsafePath, CodePermission:
		return 2
	case CodeWorkspaceExists, CodeNotInitialized, CodeConfigInvalid, CodeConfigVersion, CodeConflict:
		return 3
	case CodeNotFound:
		return 4
	default:
		return 1
	}
}
