// Package logging centralizes RepairFlow's standard-library structured
// diagnostics. Debug is opt-in; normal operation emits only warnings/errors.
package logging

import (
	"io"
	"log/slog"
	"strings"
)

// EnvDebug enables diagnostics when set to a non-empty value other than
// "0" or "false". The explicit CLI flag always takes precedence as an enable.
const EnvDebug = "REPAIRFLOW_DEBUG"

// DebugEnabled resolves the CLI flag and environment using one policy.
func DebugEnabled(flag bool, environment string) bool {
	if flag {
		return true
	}
	value := strings.TrimSpace(environment)
	return value != "" && value != "0" && !strings.EqualFold(value, "false")
}

// New builds a production-safe text logger. Callers attach only known-safe
// fields; request payloads, credentials and tokens must never be logged.
func New(debug bool, output io.Writer) *slog.Logger {
	level := slog.LevelInfo
	if debug {
		level = slog.LevelDebug
	}
	return slog.New(slog.NewTextHandler(output, &slog.HandlerOptions{Level: level}))
}
