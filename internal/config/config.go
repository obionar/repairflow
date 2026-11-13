package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strings"

	"repairflow/internal/errs"
)

// CurrentConfigVersion is the config format version written by this build.
// app_version, config_version and schema_version stay independent
// (plan §3.2); only config_version lives in this file.
const CurrentConfigVersion = 1

// ConfigName is the workspace configuration file name inside a workspace root.
const ConfigName = "config.toml"

// Config is the effective workspace configuration. Unknown keys are refused
// so a future format is never silently misread by an older binary.
type Config struct {
	ConfigVersion int
	App           App
	Web           Web
	UI            UI
}

// App holds general application settings.
type App struct {
	Environment string
}

// Web holds the embedded web-interface listener settings.
type Web struct {
	Host         string
	Port         int
	AuthRequired bool
	AllowRemote  bool
	// AllowInsecureLAN permits auth_required=false on a non-loopback host. It
	// exists for a shop's own LAN while the interface is being shaped; the
	// server logs a loud warning and this must never ship enabled.
	AllowInsecureLAN bool
}

// UI holds interface defaults. Language is the workspace default; a browser
// cookie can override it per operator.
type UI struct {
	Language string
}

// Default returns the documented default configuration.
func Default() Config {
	return Config{
		ConfigVersion: CurrentConfigVersion,
		App:           App{Environment: "development"},
		Web:           Web{Host: "127.0.0.1", Port: 8091, AuthRequired: true, AllowRemote: false, AllowInsecureLAN: false},
		UI:            UI{Language: "en"},
	}
}

// Load reads and validates a config file. The file must exist; a missing file
// is reported as workspace_not_initialized so callers can distinguish it from
// a malformed one. Load never writes.
func Load(path string) (Config, error) {
	cfg := Default()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Config{}, errs.Wrap(errs.CodeNotInitialized, err, "config %q not found", path)
		}
		return Config{}, errs.Wrap(errs.CodeConfigInvalid, err, "cannot read config %q", path)
	}
	if len(data) > 64*1024 {
		return Config{}, errs.New(errs.CodeConfigInvalid, "config %q is unexpectedly large (%d bytes)", path, len(data))
	}

	doc, err := parseToml(data)
	if err != nil {
		return Config{}, decorate(err, path)
	}
	seen := map[string]bool{}
	for section, pairs := range doc {
		for key, value := range pairs {
			full := key
			if section != "" {
				full = section + "." + key
			}
			if err := cfg.set(full, value); err != nil {
				return Config{}, decorate(err, path)
			}
			seen[full] = true
		}
	}
	if !seen["config_version"] {
		return Config{}, decorate(errs.New(errs.CodeConfigInvalid, "missing required key \"config_version\""), path)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, decorate(err, path)
	}
	return cfg, nil
}

// set applies one known key, refusing anything else.
func (c *Config) set(key string, v tomlValue) error {
	want := func(k tomlKind) error {
		if v.kind != k {
			return errs.New(errs.CodeConfigInvalid, "key %q has the wrong value type", key)
		}
		return nil
	}
	switch key {
	case "config_version":
		if err := want(kindInt); err != nil {
			return err
		}
		if v.num > CurrentConfigVersion {
			return errs.New(errs.CodeConfigVersion, "config_version %d is newer than the supported version %d; upgrade RepairFlow first", v.num, CurrentConfigVersion)
		}
		c.ConfigVersion = int(v.num)
	case "app.environment":
		if err := want(kindString); err != nil {
			return err
		}
		c.App.Environment = v.str
	case "web.host":
		if err := want(kindString); err != nil {
			return err
		}
		c.Web.Host = v.str
	case "web.port":
		if err := want(kindInt); err != nil {
			return err
		}
		c.Web.Port = int(v.num)
	case "web.auth_required":
		if err := want(kindBool); err != nil {
			return err
		}
		c.Web.AuthRequired = v.b
	case "web.allow_remote":
		if err := want(kindBool); err != nil {
			return err
		}
		c.Web.AllowRemote = v.b
	case "web.allow_insecure_lan":
		if err := want(kindBool); err != nil {
			return err
		}
		c.Web.AllowInsecureLAN = v.b
	case "ui.language":
		if err := want(kindString); err != nil {
			return err
		}
		c.UI.Language = v.str
	default:
		return errs.New(errs.CodeConfigInvalid, "unknown key %q (this binary supports config_version %d)", key, CurrentConfigVersion)
	}
	return nil
}

// Validate checks cross-field invariants.
func (c Config) Validate() error {
	if c.ConfigVersion != CurrentConfigVersion {
		return errs.New(errs.CodeConfigInvalid, "config_version must be %d, got %d", CurrentConfigVersion, c.ConfigVersion)
	}
	if strings.TrimSpace(c.App.Environment) == "" || len(c.App.Environment) > 64 {
		return errs.New(errs.CodeConfigInvalid, "app.environment must be 1-64 characters")
	}
	if strings.TrimSpace(c.Web.Host) == "" || len(c.Web.Host) > 255 {
		return errs.New(errs.CodeConfigInvalid, "web.host must be 1-255 characters")
	}
	if c.Web.Port < 1 || c.Web.Port > 65535 {
		return errs.New(errs.CodeConfigInvalid, "web.port must be in 1-65535, got %d", c.Web.Port)
	}
	if !IsLoopbackHost(c.Web.Host) && !c.Web.AllowRemote {
		return errs.New(errs.CodeConfigInvalid,
			"web.host %q is not loopback; set web.allow_remote=true only after reviewing network exposure", c.Web.Host)
	}
	if !c.Web.AuthRequired && !IsLoopbackHost(c.Web.Host) && !c.Web.AllowInsecureLAN {
		return errs.New(errs.CodeConfigInvalid,
			"web.auth_required=false is allowed only for loopback hosts; set web.allow_insecure_lan=true to serve an unauthenticated workspace to the LAN (testing only)")
	}
	if !validLanguage(c.UI.Language) {
		return errs.New(errs.CodeConfigInvalid,
			"ui.language must be 2-16 letters, digits or dashes (got %q)", c.UI.Language)
	}
	return nil
}

// validLanguage accepts a BCP-47-ish primary tag: 2-16 chars of a-z, 0-9 and
// dashes. Whether a catalog exists is checked by the web layer.
func validLanguage(code string) bool {
	if len(code) < 2 || len(code) > 16 {
		return false
	}
	for _, r := range code {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
		default:
			return false
		}
	}
	return true
}

// IsLoopbackHost reports whether a listen address or request host is
// loopback-only. Accepts a bare host, an IPv6 literal in brackets and a
// host:port form, so the config check and the request-host guard share one rule.
func IsLoopbackHost(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.Trim(strings.TrimSpace(host), "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Render renders the config canonically for `repairflow init`. Key order is
// deterministic so rendered files diff cleanly.
func Render(c Config) []byte {
	var b strings.Builder
	b.WriteString("# RepairFlow workspace configuration (config_version 1).\n")
	b.WriteString("# Managed by the operator. repairflow never rewrites this file automatically.\n\n")
	fmt.Fprintf(&b, "config_version = %d\n\n", c.ConfigVersion)
	b.WriteString("[app]\n")
	fmt.Fprintf(&b, "environment = %q\n\n", c.App.Environment)
	b.WriteString("[web]\n")
	fmt.Fprintf(&b, "host = %q\n", c.Web.Host)
	fmt.Fprintf(&b, "port = %d\n", c.Web.Port)
	fmt.Fprintf(&b, "auth_required = %t\n", c.Web.AuthRequired)
	fmt.Fprintf(&b, "allow_remote = %t\n", c.Web.AllowRemote)
	fmt.Fprintf(&b, "allow_insecure_lan = %t\n\n", c.Web.AllowInsecureLAN)
	b.WriteString("[ui]\n")
	fmt.Fprintf(&b, "language = %q\n", c.UI.Language)
	return []byte(b.String())
}

// decorate adds the config path to a parse/validation error, preserving the
// error code through wrapping.
func decorate(err error, path string) error {
	var e *errs.Error
	if errors.As(err, &e) {
		return &errs.Error{Code: e.Code, Message: fmt.Sprintf("%s: %s", path, e.Message), Cause: err}
	}
	return err
}
