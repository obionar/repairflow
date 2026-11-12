package cli

import (
	"strings"

	"repairflow/internal/errs"
)

// parsedArgs is the result of subcommand argument parsing.
type parsedArgs struct {
	values     map[string]string
	bools      map[string]bool
	positional []string
}

// get returns a value flag ("" when absent).
func (p parsedArgs) get(name string) string { return p.values[name] }

// flag returns whether a boolean flag was set.
func (p parsedArgs) flag(name string) bool { return p.bools[name] }

// parseArgs parses subcommand arguments with a strict, uniform contract:
//
//	--key VALUE   --key=VALUE   --bool   positional...
//
// Unknown flags, missing values and values that look like flags are refused
// with usage_error, so a typo can never be silently ignored and a forgotten
// value can never consume the next flag (audit finding A-04).
func parseArgs(args []string, valueFlags, boolFlags []string) (parsedArgs, error) {
	values := map[string]string{}
	bools := map[string]bool{}
	positional := []string{}

	valueSet := map[string]bool{}
	for _, f := range valueFlags {
		valueSet[f] = true
	}
	boolSet := map[string]bool{}
	for _, f := range boolFlags {
		boolSet[f] = true
	}

	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(arg, "--") {
			positional = append(positional, arg)
			continue
		}
		name, inline, hasInline := strings.Cut(strings.TrimPrefix(arg, "--"), "=")
		switch {
		case boolSet[name]:
			if hasInline {
				return parsedArgs{}, errs.New(errs.CodeUsage, "flag --%s does not take a value", name)
			}
			bools[name] = true
		case valueSet[name]:
			value := inline
			if !hasInline {
				if i+1 >= len(args) {
					return parsedArgs{}, errs.New(errs.CodeUsage, "--%s requires a value", name)
				}
				i++
				value = args[i]
			}
			if value == "" {
				return parsedArgs{}, errs.New(errs.CodeUsage, "--%s requires a non-empty value", name)
			}
			if strings.HasPrefix(value, "-") && value != "-" {
				return parsedArgs{}, errs.New(errs.CodeUsage, "--%s value %q looks like a flag; use --%s=VALUE form if this is intentional", name, value, name)
			}
			if _, dup := values[name]; dup {
				return parsedArgs{}, errs.New(errs.CodeUsage, "--%s given more than once", name)
			}
			values[name] = value
		default:
			allowed := append(append([]string{}, valueFlags...), boolFlags...)
			return parsedArgs{}, errs.New(errs.CodeUsage, "unknown flag --%s (allowed: %s)", name, strings.Join(allowed, ", "))
		}
	}
	return parsedArgs{values: values, bools: bools, positional: positional}, nil
}
