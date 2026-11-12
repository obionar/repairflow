package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"repairflow/internal/errs"
)

func TestParseTomlValid(t *testing.T) {
	doc, err := parseToml([]byte(`
# leading comment
config_version = 1

[app]
environment = "development"   # trailing comment

[web]
host = "127.0.0.1"
port = 8091
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if v := doc[""]["config_version"]; v.kind != kindInt || v.num != 1 {
		t.Fatalf("config_version: %+v", v)
	}
	if v := doc["app"]["environment"]; v.kind != kindString || v.str != "development" {
		t.Fatalf("environment: %+v", v)
	}
	if v := doc["web"]["port"]; v.kind != kindInt || v.num != 8091 {
		t.Fatalf("port: %+v", v)
	}
}

func TestParseTomlRejects(t *testing.T) {
	cases := map[string]string{
		"duplicate key":     "a = 1\na = 2\n",
		"duplicate section": "[s]\n[s]\n",
		"missing value":     "a =\n",
		"unsupported value": "a = 1.5\n",
		"array value":       "a = [1]\n",
		"inline table":      "a = {b = 1}\n",
		"bad escape":        `a = "x\q"` + "\n",
		"unterminated":      "a = \"x\n",
		"dotted key":        "a.b = 1\n",
		"nested section":    "[a.b]\n",
		"bare garbage":      "just text\n",
	}
	for name, input := range cases {
		if _, err := parseToml([]byte(input)); err == nil {
			t.Errorf("%s: expected refusal", name)
		} else if errs.CodeOf(err) != errs.CodeConfigInvalid {
			t.Errorf("%s: expected config_invalid, got %v", name, err)
		}
	}
}

func TestLoadRoundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ConfigName)
	if err := os.WriteFile(path, Render(Default()), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg != Default() {
		t.Fatalf("roundtrip mismatch: %+v", cfg)
	}
}

func TestRemoteBindingRequiresExplicitAllow(t *testing.T) {
	path := filepath.Join(t.TempDir(), ConfigName)
	body := "config_version = 1\n[web]\nhost = \"0.0.0.0\"\nport = 8091\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); errs.CodeOf(err) != errs.CodeConfigInvalid {
		t.Fatalf("remote bind without allow_remote must be refused: %v", err)
	}
	body += "allow_remote = true\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil || !cfg.Web.AllowRemote {
		t.Fatalf("explicit remote bind: %+v %v", cfg, err)
	}
}

func TestAuthenticationDisableIsLoopbackOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), ConfigName)
	body := "config_version = 1\n[web]\nhost = \"127.0.0.1\"\nport = 8091\nauth_required = false\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil || cfg.Web.AuthRequired {
		t.Fatalf("loopback test config: %+v %v", cfg, err)
	}
	body = strings.Replace(body, "127.0.0.1", "0.0.0.0", 1)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); errs.CodeOf(err) != errs.CodeConfigInvalid {
		t.Fatalf("non-loopback auth bypass must be refused: %v", err)
	}
}

// TestInsecureLANExplicitOptIn pins the safety rule: an unauthenticated
// workspace may face the LAN only when web.allow_insecure_lan says so, and the
// generated config always records the key.
func TestInsecureLANExplicitOptIn(t *testing.T) {
	base := t.TempDir()
	write := func(name, body string) string {
		path := filepath.Join(base, name)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	body := func(extra string) string {
		return `config_version = 1

[app]
environment = "development"

[web]
host = "0.0.0.0"
port = 8091
auth_required = false
allow_remote = true
` + extra + `
[ui]
language = "en"
`
	}
	if _, err := Load(write("refused.toml", body(""))); err == nil || errs.CodeOf(err) != errs.CodeConfigInvalid {
		t.Fatalf("an unauthenticated LAN bind must be refused, got %v", err)
	}
	cfg, err := Load(write("allowed.toml", body("allow_insecure_lan = true\n")))
	if err != nil {
		t.Fatalf("explicit opt-in must load: %v", err)
	}
	if !cfg.Web.AllowInsecureLAN || cfg.Web.AuthRequired || cfg.Web.Host != "0.0.0.0" {
		t.Fatalf("loaded config: %+v", cfg.Web)
	}
	if !strings.Contains(string(Render(cfg)), "allow_insecure_lan = true") {
		t.Fatal("generated config must record allow_insecure_lan")
	}
	if !strings.Contains(string(Render(Default())), "allow_insecure_lan = false") {
		t.Fatal("the default config must state the key as false")
	}
}

func TestLoadMissingIsNotInitialized(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "absent.toml"))
	if err == nil {
		t.Fatal("expected missing config to fail")
	}
	if errs.CodeOf(err) != errs.CodeNotInitialized {
		t.Fatalf("expected workspace_not_initialized, got %v", err)
	}
}

func TestLoadRejects(t *testing.T) {
	cases := map[string]struct {
		body string
		code errs.Code
	}{
		"unknown key":       {"config_version = 1\nmystery = 2\n", errs.CodeConfigInvalid},
		"unknown section":   {"config_version = 1\n[future]\nx = 1\n", errs.CodeConfigInvalid},
		"future version":    {"config_version = 99\n", errs.CodeConfigVersion},
		"missing version":   {"[app]\nenvironment = \"x\"\n", errs.CodeConfigInvalid},
		"bad port":          {"config_version = 1\n[web]\nport = 0\n", errs.CodeConfigInvalid},
		"wrong type":        {"config_version = 1\n[web]\nport = \"8091\"\n", errs.CodeConfigInvalid},
		"blank environment": {"config_version = 1\n[app]\nenvironment = \"\"\n", errs.CodeConfigInvalid},
	}
	for name, tc := range cases {
		path := filepath.Join(t.TempDir(), ConfigName)
		if err := os.WriteFile(path, []byte(tc.body), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := Load(path)
		if err == nil {
			t.Errorf("%s: expected refusal", name)
			continue
		}
		if errs.CodeOf(err) != tc.code {
			t.Errorf("%s: expected %s, got %v", name, tc.code, err)
		}
		if !strings.Contains(err.Error(), path) {
			t.Errorf("%s: error should name the config file: %v", name, err)
		}
	}
}
