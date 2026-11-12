package config

import (
	"testing"

	"repairflow/internal/errs"
)

// FuzzParseToml checks the strict config parser against arbitrary bytes: it
// must never panic, and every failure must carry the config_invalid (or
// config_version_unsupported) code so the CLI contract stays stable.
func FuzzParseToml(f *testing.F) {
	for _, seed := range []string{
		"config_version = 1\n",
		"config_version = 1\n[app]\nenvironment = \"development\"\n",
		"# comment\n[web]\nport = 8091\n",
		"a = \"x\\n\"\n",
		"a = 1.5\n",
		"[a.b]\nx = 1\n",
		"a = \"unterminated\n",
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		doc, err := parseToml(data)
		if err != nil {
			code := errs.CodeOf(err)
			if code != errs.CodeConfigInvalid && code != errs.CodeConfigVersion {
				t.Fatalf("unexpected error code %s for %q: %v", code, data, err)
			}
			return
		}
		// A parsed doc must apply cleanly or fail classified — never panic.
		cfg := Default()
		for section, pairs := range doc {
			for key, value := range pairs {
				full := key
				if section != "" {
					full = section + "." + key
				}
				if err := cfg.set(full, value); err != nil {
					switch errs.CodeOf(err) {
					case errs.CodeConfigInvalid, errs.CodeConfigVersion:
						// classified refusal is fine
					default:
						t.Fatalf("unexpected set error for %q: %v", full, err)
					}
				}
			}
		}
	})
}
