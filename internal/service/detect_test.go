package service

import (
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
)

// TestDetectInit covers the detection order: systemctl, then /sbin/openrc-run,
// then none.
func TestDetectInit(t *testing.T) {
	found := func(string) (string, error) { return "/usr/bin/found", nil }
	missing := func(string) (string, error) { return "", os.ErrNotExist }
	withOpenRC := func(path string) bool { return path == openrcRunPath }
	withoutOpenRC := func(string) bool { return false }

	cases := []struct {
		name   string
		look   func(string) (string, error)
		exists func(string) bool
		want   Init
	}{
		{"systemd wins", found, withOpenRC, InitSystemd},
		{"systemd alone", found, withoutOpenRC, InitSystemd},
		{"openrc without systemd", missing, withOpenRC, InitOpenRC},
		{"neither", missing, withoutOpenRC, InitNone},
	}
	for _, tc := range cases {
		if got := detectInit(tc.look, tc.exists); got != tc.want {
			t.Errorf("%s: detectInit = %q, want %q", tc.name, got, tc.want)
		}
	}

	// The OpenRC probe must be the interpreter, not an arbitrary existing file.
	asked := ""
	detectInit(missing, func(path string) bool { asked = path; return true })
	if asked != openrcRunPath {
		t.Errorf("detection probed %q, want %q", asked, openrcRunPath)
	}
}

func TestOwnerUserMatchesDirectoryOwner(t *testing.T) {
	dir := t.TempDir()
	current, err := user.Current()
	if err != nil {
		t.Skipf("user lookup unavailable: %v", err)
	}
	got := ownerUser(dir)
	if !strings.HasPrefix(got, current.Username+":") {
		t.Fatalf("ownerUser(%q) = %q, want %q:group", dir, got, current.Username)
	}
	if ownerUser(filepath.Join(dir, "missing")) != "" {
		t.Error("a missing directory must yield no command_user")
	}
}

// TestRenderOpenRCSkipsUnknownUser keeps the script valid when the owner cannot
// be resolved (no dangling command_user or --owner argument).
func TestRenderOpenRCSkipsUnknownUser(t *testing.T) {
	script := renderOpenRC(DefaultName, "/bin/repairflow", "/ws", "", "/ws/logs/service.log")
	for _, banned := range []string{"command_user=", "--owner"} {
		if strings.Contains(script, banned) {
			t.Errorf("script with no known user must omit %q:\n%s", banned, script)
		}
	}
	if !strings.Contains(script, `checkpath --file --mode 0600 "${output_log}"`) {
		t.Errorf("start_pre lost its checkpath:\n%s", script)
	}
}

// TestRenderOpenRCEscapesValues keeps quotes, dollars and backticks inert.
func TestRenderOpenRCEscapesValues(t *testing.T) {
	script := renderOpenRC(DefaultName, `/bin/we"ird`, "/ws/$(boom)", "u:g", "/ws/logs/s.log")
	if strings.Contains(script, `"$(`) {
		t.Errorf("command substitution was not escaped:\n%s", script)
	}
	if !strings.Contains(script, `command="/bin/we\"ird"`) {
		t.Errorf("quote not escaped:\n%s", script)
	}
	if !strings.Contains(script, `command_args="serve --home /ws/\$(boom)"`) {
		t.Errorf("dollar not escaped:\n%s", script)
	}
}
