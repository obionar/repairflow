package config

import (
	"strconv"
	"strings"

	"repairflow/internal/errs"
)

// This file implements the strict, minimal TOML subset used by config.toml
// (decision recorded in docs/ARCHITECTURE.md): plain key/value pairs in an
// optional flat [section], with basic strings, integers and booleans.
// Comments, dotted keys, arrays, floats, dates and inline tables are rejected
// with a line-numbered error. Keeping the parser in-tree means the single
// binary has zero third-party dependencies and a fully auditable config path.
// If the config format outgrows this subset, replace it with a reviewed parser.
// replace this file with a vetted TOML library behind the same Load/Render API.

type tomlKind int

const (
	kindString tomlKind = iota
	kindInt
	kindBool
)

type tomlValue struct {
	kind tomlKind
	str  string
	num  int64
	b    bool
}

// tomlDoc maps section name ("" for the top level) to key/value pairs.
type tomlDoc map[string]map[string]tomlValue

func parseToml(data []byte) (tomlDoc, error) {
	doc := tomlDoc{"": {}}
	section := ""
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	for i, raw := range lines {
		lineNo := i + 1
		line := strings.TrimSpace(strings.TrimPrefix(raw, "\ufeff"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			name, err := parseSectionHeader(line, lineNo)
			if err != nil {
				return nil, err
			}
			if _, seen := doc[name]; seen {
				return nil, errs.New(errs.CodeConfigInvalid, "line %d: duplicate section [%s]", lineNo, name)
			}
			doc[name] = map[string]tomlValue{}
			section = name
			continue
		}
		key, rest, ok := strings.Cut(line, "=")
		if !ok {
			return nil, errs.New(errs.CodeConfigInvalid, "line %d: expected \"key = value\", got %q", lineNo, line)
		}
		key = strings.TrimSpace(key)
		if !validBareKey(key) {
			return nil, errs.New(errs.CodeConfigInvalid, "line %d: invalid key %q", lineNo, key)
		}
		if _, exists := doc[section][key]; exists {
			return nil, errs.New(errs.CodeConfigInvalid, "line %d: duplicate key %q", lineNo, key)
		}
		value, err := parseValue(strings.TrimSpace(rest), lineNo)
		if err != nil {
			return nil, err
		}
		doc[section][key] = value
	}
	return doc, nil
}

func parseSectionHeader(line string, lineNo int) (string, error) {
	if !strings.HasSuffix(line, "]") || strings.Count(line, "[") != 1 {
		return "", errs.New(errs.CodeConfigInvalid, "line %d: invalid section header %q", lineNo, line)
	}
	name := strings.TrimSpace(line[1 : len(line)-1])
	if !validBareKey(name) {
		return "", errs.New(errs.CodeConfigInvalid, "line %d: unsupported section name %q (nested or dotted sections are not supported)", lineNo, name)
	}
	return name, nil
}

func validBareKey(key string) bool {
	if key == "" {
		return false
	}
	for _, r := range key {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			continue
		}
		return false
	}
	return true
}

func parseValue(rest string, lineNo int) (tomlValue, error) {
	// Strip a trailing comment separated from the value by whitespace.
	if idx := strings.IndexByte(rest, '#'); idx >= 0 {
		rest = strings.TrimSpace(rest[:idx])
	}
	if rest == "" {
		return tomlValue{}, errs.New(errs.CodeConfigInvalid, "line %d: missing value", lineNo)
	}
	switch {
	case strings.HasPrefix(rest, "\""):
		s, err := parseBasicString(rest, lineNo)
		if err != nil {
			return tomlValue{}, err
		}
		return tomlValue{kind: kindString, str: s}, nil
	case rest == "true":
		return tomlValue{kind: kindBool, b: true}, nil
	case rest == "false":
		return tomlValue{kind: kindBool, b: false}, nil
	case strings.HasPrefix(rest, "[") || strings.HasPrefix(rest, "{"):
		return tomlValue{}, errs.New(errs.CodeConfigInvalid, "line %d: arrays and inline tables are not supported in config.toml", lineNo)
	}
	n, err := strconv.ParseInt(rest, 10, 64)
	if err != nil {
		return tomlValue{}, errs.New(errs.CodeConfigInvalid, "line %d: unsupported value %q (expected a quoted string, integer, true or false)", lineNo, rest)
	}
	return tomlValue{kind: kindInt, num: n}, nil
}

func parseBasicString(rest string, lineNo int) (string, error) {
	if len(rest) < 2 || !strings.HasSuffix(rest, "\"") {
		return "", errs.New(errs.CodeConfigInvalid, "line %d: unterminated string", lineNo)
	}
	body := rest[1 : len(rest)-1]
	var b strings.Builder
	for i := 0; i < len(body); i++ {
		c := body[i]
		switch c {
		case '"':
			return "", errs.New(errs.CodeConfigInvalid, "line %d: unescaped quote inside string", lineNo)
		case '\\':
			if i+1 >= len(body) {
				return "", errs.New(errs.CodeConfigInvalid, "line %d: dangling escape in string", lineNo)
			}
			i++
			switch body[i] {
			case '\\', '"':
				b.WriteByte(body[i])
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'r':
				b.WriteByte('\r')
			default:
				return "", errs.New(errs.CodeConfigInvalid, "line %d: unsupported escape \\%c in string", lineNo, body[i])
			}
		default:
			if c < 0x20 {
				return "", errs.New(errs.CodeConfigInvalid, "line %d: control character in string", lineNo)
			}
			b.WriteByte(c)
		}
	}
	return b.String(), nil
}
