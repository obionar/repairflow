package cli

import (
	"fmt"
	"os"
	"runtime/debug"
	"strings"

	"repairflow/internal/logging"
)

// failDebug reports whether verbose diagnostics are enabled.
func failDebug(getenv Env) bool {
	return logging.DebugEnabled(false, getenv(logging.EnvDebug))
}

// stackDebug returns process context and a goroutine stack trace for
// internal-error diagnostics (only shown when REPAIRFLOW_DEBUG is set).
func stackDebug() string {
	var b strings.Builder
	fmt.Fprintf(&b, "debug trace (REPAIRFLOW_DEBUG):\n  pid=%d cwd=", os.Getpid())
	if cwd, err := os.Getwd(); err == nil {
		b.WriteString(cwd)
	} else {
		b.WriteString("?")
	}
	b.WriteString("\n")
	b.Write(debug.Stack())
	return b.String()
}
