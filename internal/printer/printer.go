// Package printer prints stickers on a local label printer. It is an adapter
// around an external driver program, not a business-rule authority.
//
// Only the Marklife P15 is supported today, through the p15 CLI
// (github.com/obionar/p15). p15 owns Bluetooth, the label layout and the warm
// daemon, so RepairFlow needs no capabilities and never opens the radio. A
// second printer model is a new driver value, not a new code path here.
package printer

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"repairflow/internal/errs"
)

// DriverP15 is the Marklife P15, printed through the p15 CLI.
const DriverP15 = "p15"

// Drivers lists the label printers an operator can select.
var Drivers = []string{DriverP15}

// LabelPrinterTimeout bounds one print. p15 needs ~10 s for a cold connect and
// holds a 4 s settle after each label, so this is generous on purpose: killing
// p15 mid-print leaves the outcome unknown and the next label may duplicate it.
const LabelPrinterTimeout = 45 * time.Second

// maxMessage bounds the driver's failure text shown to the operator.
const maxMessage = 200

// Known reports whether driver is a selectable label printer.
func Known(driver string) bool {
	for _, d := range Drivers {
		if d == driver {
			return true
		}
	}
	return false
}

// Sticker prints one intake sticker: the repair number as the hero and the
// customer name beside it. An empty name prints the number alone (a repair
// without a customer).
func Sticker(ctx context.Context, driver string, id int64, name string) error {
	return run(ctx, driver, stickerArgs(id, name)...)
}

// TestLabel prints a short label so the operator can confirm the setup.
func TestLabel(ctx context.Context, driver string) error {
	return run(ctx, driver, "text", "RepairFlow test")
}

// stickerArgs builds the p15 job. p15 adds the '#' to a bare repair number, so
// the raw id is passed. The name argument is kept even when it is empty: the
// p15 repair preset renders the number by itself in that case.
func stickerArgs(id int64, name string) []string {
	return []string{"repair", strconv.FormatInt(id, 10), strings.TrimSpace(name)}
}

func run(ctx context.Context, driver string, args ...string) error {
	if !Known(driver) {
		return errs.New(errs.CodeValidation, "unknown label printer %q", driver)
	}
	bin, err := binary()
	if err != nil {
		return err
	}
	// Detach from request cancellation: a closed browser tab or a retried form
	// must not kill p15 halfway through a label that may already be feeding.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), LabelPrinterTimeout)
	defer cancel()
	out, runErr := exec.CommandContext(ctx, bin, args...).CombinedOutput()
	if runErr == nil {
		return nil
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return errs.New(errs.CodeConflict,
			"the label printer did not answer within %s; check the printer before printing again", LabelPrinterTimeout)
	}
	return errs.New(errs.CodeConflict, "label printing failed: %s", failureText(out, runErr))
}

// binary finds p15. A service unit often has a PATH without /usr/local/bin, so
// the documented install locations are checked after PATH.
func binary() (string, error) {
	if path, err := exec.LookPath(DriverP15); err == nil {
		return path, nil
	}
	for _, path := range []string{"/usr/local/bin/" + DriverP15, "/usr/bin/" + DriverP15} {
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
			return path, nil
		}
	}
	return "", errs.New(errs.CodeValidation,
		"the p15 label printer program is not installed for this user; install p15 on the host that runs RepairFlow")
}

// failureText reduces the driver's output to one safe, bounded line: the last
// non-empty line, without control characters.
func failureText(out []byte, runErr error) string {
	lines := strings.Split(string(out), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.Map(func(r rune) rune {
			if r < 0x20 || r == 0x7f {
				return -1
			}
			return r
		}, strings.TrimSpace(lines[i]))
		if line == "" {
			continue
		}
		if runes := []rune(line); len(runes) > maxMessage {
			line = string(runes[:maxMessage])
		}
		return line
	}
	if runErr != nil {
		return runErr.Error()
	}
	return "no output from the label printer"
}
