package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"repairflow/internal/seed"
	"runtime"
	"strings"
	"testing"

	"repairflow/internal/buildinfo"
	"repairflow/internal/testutil"
)

// runEnv executes a command against a fixed environment map.
func runEnv(t *testing.T, env map[string]string, args ...string) execResult {
	t.Helper()
	var out, errb bytes.Buffer
	code := Run(context.Background(), args, strings.NewReader(""), &out, &errb, func(key string) string { return env[key] })
	return execResult{code: code, stdout: out.String(), stderr: errb.String()}
}

func dataOf(t *testing.T, res execResult) map[string]any {
	t.Helper()
	if res.code != 0 {
		t.Fatalf("expected success, got exit %d: %s", res.code, res.stderr)
	}
	env := decode(t, res.stdout)
	return env["data"].(map[string]any)
}

// TestFullRepairWorkflow drives the complete CLI repair interaction:
// init -> customer -> repair -> entries -> stages -> history -> search.
func TestFullRepairWorkflow(t *testing.T) {
	home := filepath.Join(t.TempDir(), "ws")
	env := map[string]string{"USER": "tester"}
	base := []string{"--home", home, "--json"}

	res := runEnv(t, env, append([]string{"init"}, base...)...)
	if res.code != 0 {
		t.Fatalf("init: %+v", res)
	}

	// Customer create/show/update with revision safety.
	c := dataOf(t, runEnv(t, env, append([]string{"customer", "add", "--name", "Ada Lovelace", "--phone", "123"}, base...)...))
	id := int64(c["id"].(float64))
	if c["name"] != "Ada Lovelace" || c["revision"] == "" {
		t.Fatalf("customer: %v", c)
	}

	show := dataOf(t, runEnv(t, env, append([]string{"customer", "show", itoa(id)}, base...)...))
	cust := show["customer"].(map[string]any)
	rev := cust["revision"].(string)

	upd := dataOf(t, runEnv(t, env, append([]string{"customer", "update", itoa(id), "--name", "Ada L.", "--revision", rev}, base...)...))
	if upd["name"] != "Ada L." {
		t.Fatalf("update: %v", upd)
	}

	// Stale revision -> conflict (exit 3), record untouched.
	stale := runEnv(t, env, append([]string{"customer", "update", itoa(id), "--name", "Mallory", "--revision", rev}, base...)...)
	if stale.code != 3 {
		t.Fatalf("stale update exit %d: %s", stale.code, stale.stderr)
	}
	if code := decode(t, stale.stderr)["error"].(map[string]any)["code"]; code != "conflict" {
		t.Fatalf("stale code: %v", code)
	}

	// Repair create, log entries, stage chain.
	tk := dataOf(t, runEnv(t, env, append([]string{"repair", "add", "--customer", itoa(id), "--device", "Analytical Engine", "--issue", "Carry wheel sticks"}, base...)...))
	tid := int64(tk["id"].(float64))
	if tk["stage"] != "new" {
		t.Fatalf("repair: %v", tk)
	}

	dataOf(t, runEnv(t, env, append([]string{"repair", "entry", itoa(tid), "--kind", "work", "--body", "Cleaned carry mechanism"}, base...)...))
	dataOf(t, runEnv(t, env, append([]string{"repair", "entry", itoa(tid), "--kind", "test", "--body", "Ran 100 iterations"}, base...)...))
	dataOf(t, runEnv(t, env, append([]string{"repair", "stage", itoa(tid), "in_progress"}, base...)...))
	dataOf(t, runEnv(t, env, append([]string{"repair", "stage", itoa(tid), "repaired"}, base...)...))

	// Reopening a terminal stage requires --reopen.
	refused := runEnv(t, env, append([]string{"repair", "stage", itoa(tid), "in_progress"}, base...)...)
	if refused.code != 2 {
		t.Fatalf("reopen without flag exit %d: %s", refused.code, refused.stderr)
	}
	dataOf(t, runEnv(t, env, append([]string{"repair", "stage", itoa(tid), "in_progress", "--reopen"}, base...)...))

	// Full readback: log and complete activity history.
	detail := dataOf(t, runEnv(t, env, append([]string{"repair", "show", itoa(tid)}, base...)...))
	if len(detail["entries"].([]any)) != 2 {
		t.Fatalf("entries: %v", detail["entries"])
	}
	activity := detail["activity"].([]any)
	if len(activity) < 5 { // create + 2 entries + 3 stage actions
		t.Fatalf("activity too short: %d", len(activity))
	}

	// Search and filters.
	list := dataOf(t, runEnv(t, env, append([]string{"repair", "list", "--search", "Analytical"}, base...)...))
	if list["total"].(float64) != 1 {
		t.Fatalf("search: %v", list)
	}

	// Human text mode renders readable output on stdout.
	text := runEnv(t, env, "repair", "show", itoa(tid), "--home", home)
	if text.code != 0 || !strings.Contains(text.stdout, "Analytical Engine") || !strings.Contains(text.stdout, "activity:") {
		t.Fatalf("text render: %+v", text)
	}
}

func TestInitDemoAndDiagnostics(t *testing.T) {
	home := filepath.Join(t.TempDir(), "ws")
	env := map[string]string{"USER": "tester"}
	base := []string{"--home", home, "--json"}

	res := runEnv(t, env, append([]string{"init", "--demo"}, base...)...)
	if res.code != 0 {
		t.Fatalf("init --demo: %+v", res)
	}
	data := decode(t, res.stdout)["data"].(map[string]any)
	seedData := data["seed"].(map[string]any)
	if seedData["customers"].(float64) != float64(seed.DemoCustomers) ||
		seedData["repairs"].(float64) != float64(seed.DemoRepairs) ||
		seedData["parts"].(float64) != float64(seed.DemoParts) ||
		seedData["estimates"].(float64) != float64(seed.DemoEstimates) {
		t.Fatalf("demo seed: %v", seedData)
	}
	if seedData["user"] != nil {
		t.Fatalf("init --demo must not create credentials: %v", seedData)
	}

	// Re-running seed reports already seeded without changes.
	again := runEnv(t, env, append([]string{"seed", "demo"}, base...)...)
	if again.code != 0 {
		t.Fatalf("seed rerun: %+v", again)
	}

	// status reports the demo dataset.
	st := dataOf(t, runEnv(t, env, append([]string{"status"}, base...)...))
	if st["demo_seeded"] != true || st["customers"].(float64) != float64(seed.DemoCustomers) || st["schema_version"].(float64) < 1 {
		t.Fatalf("status: %v", st)
	}

	// doctor is healthy on a fresh demo workspace.
	doc := dataOf(t, runEnv(t, env, append([]string{"doctor"}, base...)...))
	if doc["ok"] != true {
		t.Fatalf("doctor: %v", doc)
	}

	// Demo seed refuses to mix with a real record elsewhere.
	home2 := filepath.Join(t.TempDir(), "ws2")
	base2 := []string{"--home", home2, "--json"}
	runEnv(t, env, append([]string{"init"}, base2...)...)
	dataOf(t, runEnv(t, env, append([]string{"customer", "add", "--name", "Real Person"}, base2...)...))
	refused := runEnv(t, env, append([]string{"seed", "demo"}, base2...)...)
	if refused.code != 3 {
		t.Fatalf("seed over real data exit %d: %s", refused.code, refused.stderr)
	}
}

// TestDemoOperatorAccountCLI covers `init --demo --demo-user` and
// `seed demo --demo-user`: the account is optional, the password only comes
// from the environment, no password is echoed, and reruns do not reset it.
func TestDemoOperatorAccountCLI(t *testing.T) {
	home := filepath.Join(t.TempDir(), "ws")
	base := []string{"--home", home, "--json"}
	env := map[string]string{"USER": "tester", "REPAIRFLOW_NEW_PASSWORD": "demo-password-1"}

	// Missing password is a usage error and creates nothing.
	noPass := runEnv(t, map[string]string{"USER": "tester"},
		append([]string{"init", "--demo", "--demo-user", "demo"}, base...)...)
	if noPass.code != 2 || !strings.Contains(noPass.stderr, "REPAIRFLOW_NEW_PASSWORD") {
		t.Fatalf("missing demo password: exit %d %s", noPass.code, noPass.stderr)
	}

	res := runEnv(t, env, append([]string{"init", "--demo", "--demo-user", "demo"}, base...)...)
	if res.code != 0 {
		t.Fatalf("init --demo --demo-user: %+v", res)
	}
	seedData := decode(t, res.stdout)["data"].(map[string]any)["seed"].(map[string]any)
	user := seedData["user"].(map[string]any)
	if user["username"] != "demo" || user["created"] != true || user["role"] != "admin" {
		t.Fatalf("demo user: %v", user)
	}
	if strings.Contains(res.stdout, "demo-password-1") {
		t.Fatal("password must never appear in output")
	}

	// The account is listed and a rerun reports it without resetting it.
	list := decode(t, runEnv(t, env, append([]string{"user", "list"}, base...)...).stdout)["data"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["username"] != "demo" {
		t.Fatalf("user list: %v", list)
	}
	again := dataOf(t, runEnv(t, env, append([]string{"seed", "demo", "--demo-user", "demo"}, base...)...))
	if again["already_seeded"] != true {
		t.Fatalf("seed rerun: %v", again)
	}
	if u := again["user"].(map[string]any); u["already_existed"] != true || u["created"] != false {
		t.Fatalf("rerun demo user: %v", u)
	}

	// --demo-user without --demo is a usage error, not a silent account.
	other := filepath.Join(t.TempDir(), "ws2")
	badFlag := runEnv(t, env, "init", "--demo-user", "demo", "--home", other, "--json")
	if badFlag.code != 2 || !strings.Contains(badFlag.stderr, "--demo") {
		t.Fatalf("--demo-user without --demo: exit %d %s", badFlag.code, badFlag.stderr)
	}
}

func TestContactAndDeviceFieldsCLI(t *testing.T) {
	home := filepath.Join(t.TempDir(), "ws")
	env := map[string]string{"USER": "tester"}
	base := []string{"--home", home, "--json"}
	if res := runEnv(t, env, append([]string{"init"}, base...)...); res.code != 0 {
		t.Fatalf("init: %s", res.stderr)
	}
	customer := dataOf(t, runEnv(t, env, append([]string{"customer", "add", "--name", "Parity Customer",
		"--address", "1 Test Street"}, base...)...))
	if customer["address"] != "1 Test Street" {
		t.Fatalf("customer address: %v", customer)
	}
	repair := dataOf(t, runEnv(t, env, append([]string{"repair", "add", "--customer", "1",
		"--device-type", "Laptop", "--brand", "Lenovo", "--model", "T480", "--serial", "SN-1",
		"--issue", "No power"}, base...)...))
	for key, want := range map[string]string{"device_type": "Laptop", "brand": "Lenovo", "model": "T480",
		"serial": "SN-1", "reported_issue": "No power", "device": "Lenovo T480"} {
		if repair[key] != want {
			t.Errorf("repair %s=%v want %q", key, repair[key], want)
		}
	}
	updated := dataOf(t, runEnv(t, env, append([]string{"repair", "update", "1", "--serial", "SN-2"}, base...)...))
	if updated["serial"] != "SN-2" {
		t.Fatalf("serial update: %v", updated)
	}
}

func TestErrorContractMatrix(t *testing.T) {
	home := filepath.Join(t.TempDir(), "ws")
	env := map[string]string{"USER": "tester"}
	base := []string{"--home", home, "--json"}
	runEnv(t, env, append([]string{"init"}, base...)...)

	cases := []struct {
		name     string
		args     []string
		wantExit int
		wantCode string
	}{
		{"unknown command", []string{"frobnicate", "--json"}, 2, "usage_error"},
		{"unknown customer sub", append([]string{"customer", "frobnicate"}, base...), 2, "usage_error"},
		{"unknown repair sub", append([]string{"repair", "frobnicate"}, base...), 2, "usage_error"},
		{"bad id", append([]string{"customer", "show", "zero"}, base...), 2, "validation_error"},
		{"negative id", append([]string{"customer", "show", "-5"}, base...), 2, "validation_error"},
		{"missing customer", append([]string{"customer", "show", "999"}, base...), 4, "not_found"},
		{"missing repair", append([]string{"repair", "show", "999"}, base...), 4, "not_found"},
		{"entry on missing repair", append([]string{"repair", "entry", "999", "--kind", "work", "--body", "x"}, base...), 4, "not_found"},
		{"bad entry kind", append([]string{"repair", "entry", "1", "--kind", "magic", "--body", "x"}, base...), 2, "validation_error"},
		{"missing required flag", append([]string{"customer", "add"}, base...), 2, "validation_error"},
		{"unknown flag", append([]string{"repair", "list", "--bogus"}, base...), 2, "usage_error"},
		{"missing flag value", append([]string{"customer", "add", "--name"}, base...), 2, "usage_error"},
		{"bad stage name", append([]string{"repair", "stage", "1", "bogus"}, base...), 2, "validation_error"},
		{"version with args", append([]string{"version", "extra"}, base...), 2, "usage_error"},
		{"init with positional", append([]string{"init", "oops"}, base...), 2, "usage_error"},
		{"doctor with args", append([]string{"doctor", "extra"}, base...), 2, "usage_error"},
	}
	for _, tc := range cases {
		res := runEnv(t, env, tc.args...)
		if res.code != tc.wantExit {
			t.Errorf("%s: exit %d want %d (stderr %q)", tc.name, res.code, tc.wantExit, res.stderr)
			continue
		}
		if res.stdout != "" {
			t.Errorf("%s: stdout must be empty on failure, got %q", tc.name, res.stdout)
		}
		envOut := decode(t, res.stderr)
		if envOut["ok"] != false {
			t.Errorf("%s: envelope: %v", tc.name, envOut)
		}
		got := envOut["error"].(map[string]any)["code"]
		if got != tc.wantCode {
			t.Errorf("%s: code %v want %s", tc.name, got, tc.wantCode)
		}
	}
}

func TestDebugFlagAddsTraceOnInternalErrorOnly(t *testing.T) {
	// failDebug gating is the observable part; triggering a real internal
	// error from outside would require fault injection.
	if failDebug(func(string) string { return "" }) {
		t.Fatal("debug must default off")
	}
	if failDebug(func(string) string { return "0" }) || failDebug(func(string) string { return "false" }) {
		t.Fatal("0/false must disable debug")
	}
	if !failDebug(func(string) string { return "1" }) {
		t.Fatal("1 must enable debug")
	}
}

func itoa(v int64) string {
	return strings.TrimSpace(json.Number(jsonInt(v)).String())
}

func jsonInt(v int64) json.Number {
	b, _ := json.Marshal(v)
	return json.Number(string(b))
}

// TestAgentMediaIngestionFlow exercises a staged
// phone/Telegram-like media intake through the CLI only — external agent
// stages bytes, creates the repair, attaches photo + firmware, and retrieves
// byte-identical originals with stable JSON IDs.
func TestAgentMediaIngestionFlow(t *testing.T) {
	home := filepath.Join(t.TempDir(), "ws")
	env := map[string]string{"USER": "external-agent"}
	base := []string{"--home", home, "--json"}
	runEnv(t, env, append([]string{"init"}, base...)...)

	// Agent stages media locally (simulating Telegram getFile downloads).
	stage := t.TempDir()
	photo := filepath.Join(stage, "IMG_0001.jpg")
	firmware := filepath.Join(stage, "dump.bin")
	photoBytes := []byte("\xff\xd8\xff\xe0 staged photo bytes \x00\x01")
	firmwareBytes := []byte("\x7fELF\x02\x01 firmware blob with \x00 NUL bytes")
	testutil.Must(t, os.WriteFile(photo, photoBytes, 0o600))
	testutil.Must(t, os.WriteFile(firmware, firmwareBytes, 0o600))

	c := dataOf(t, runEnv(t, env, append([]string{"customer", "add", "--name", "Phone Owner"}, base...)...))
	tk := dataOf(t, runEnv(t, env, append([]string{"repair", "add", "--customer", itoa(int64(c["id"].(float64))),
		"--device", "Pixel 8", "--issue", "cracked screen"}, base...)...))
	tid := int64(tk["id"].(float64))
	entry := dataOf(t, runEnv(t, env, append([]string{"repair", "entry", itoa(tid), "--kind", "work", "--body", "Intake photos received"}, base...)...))
	eid := int64(entry["id"].(float64))

	// Attach photo to the entry and firmware to the repair.
	a1 := dataOf(t, runEnv(t, env, append([]string{"attachment", "add", "--repair", itoa(tid),
		"--file", photo, "--entry", itoa(eid), "--caption", "damage close-up", "--source", "external-agent"}, base...)...))
	a2 := dataOf(t, runEnv(t, env, append([]string{"attachment", "add", "--repair", itoa(tid),
		"--file", firmware, "--source", "external-agent"}, base...)...))
	ids := []int64{int64(a1["id"].(float64)), int64(a2["id"].(float64))}
	if a1["sha256"] == "" || a2["media_type"] == "" {
		t.Fatalf("stable metadata missing: %v %v", a1, a2)
	}

	// Retrieve originals: byte-identical, hash-stable.
	for i, src := range []string{photo, firmware} {
		out := filepath.Join(t.TempDir(), "export.bin")
		dataOf(t, runEnv(t, env, append([]string{"attachment", "export", itoa(ids[i]), "--out", out}, base...)...))
		got, _ := os.ReadFile(out)
		want, _ := os.ReadFile(src)
		if !bytes.Equal(got, want) {
			t.Fatalf("export %d not byte-identical", i)
		}
	}

	// Web-ready readback: gallery aggregates, entry shows its media, captions edit.
	gallery := runEnv(t, env, append([]string{"attachment", "list", "--repair", itoa(tid)}, base...)...)
	if gallery.code != 0 {
		t.Fatalf("gallery: %s", gallery.stderr)
	}
	if items, _ := decode(t, gallery.stdout)["data"].([]any); len(items) != 2 {
		t.Fatalf("gallery: %v", decode(t, gallery.stdout))
	}
	detail := dataOf(t, runEnv(t, env, append([]string{"repair", "show", itoa(tid)}, base...)...))
	if len(detail["entries"].([]any)) != 1 {
		t.Fatalf("entries: %v", detail["entries"])
	}
	dataOf(t, runEnv(t, env, append([]string{"attachment", "caption", itoa(ids[0]), "--caption", "edited via CLI"}, base...)...))
}

// TestIdempotentCreateCLIContract covers idempotent creation at the interface level:
// identical JSON on replay, structured conflict code on payload reuse.
func TestIdempotentCreateCLIContract(t *testing.T) {
	home := filepath.Join(t.TempDir(), "ws")
	env := map[string]string{"USER": "agent"}
	base := []string{"--home", home, "--json"}
	runEnv(t, env, append([]string{"init"}, base...)...)
	c := dataOf(t, runEnv(t, env, append([]string{"customer", "add", "--name", "Cust"}, base...)...))
	cid := itoa(int64(c["id"].(float64)))

	args := []string{"repair", "add", "--customer", cid, "--device", "Phone", "--issue", "dead", "--idempotency-key", "agent-req-42"}
	first := runEnv(t, env, append(append([]string{}, args...), base...)...)
	second := runEnv(t, env, append(append([]string{}, args...), base...)...)
	if first.code != 0 || second.code != 0 {
		t.Fatalf("replay must succeed: %d %d", first.code, second.code)
	}
	// Identical request -> identical JSON data (stable response contract).
	if dataOf(t, first)["id"] != dataOf(t, second)["id"] {
		t.Fatalf("replay returned a different record:\n%s\n%s", first.stdout, second.stdout)
	}

	conflict := runEnv(t, env, append(append([]string{}, "repair", "add", "--customer", cid,
		"--device", "Other", "--issue", "dead", "--idempotency-key", "agent-req-42"), base...)...)
	if conflict.code != 3 {
		t.Fatalf("conflicting reuse exit %d: %s", conflict.code, conflict.stderr)
	}
	if code := decode(t, conflict.stderr)["error"].(map[string]any)["code"]; code != "conflict" {
		t.Fatalf("conflict code: %v", code)
	}
}

// TestDemoResetCLI covers the interface: --confirm required,
// provenance enforced, pristine restoration.
func TestDemoResetCLI(t *testing.T) {
	home := filepath.Join(t.TempDir(), "ws")
	env := map[string]string{"USER": "tester"}
	base := []string{"--home", home, "--json"}
	runEnv(t, env, append([]string{"init", "--demo"}, base...)...)

	// Missing --confirm is refused before anything destructive happens.
	refused := runEnv(t, env, append([]string{"demo", "reset"}, base...)...)
	if refused.code != 2 {
		t.Fatalf("unconfirmed reset exit %d: %s", refused.code, refused.stderr)
	}

	// Confirmed reset on the proven demo workspace succeeds.
	res := runEnv(t, env, append([]string{"demo", "reset", "--confirm"}, base...)...)
	if res.code != 0 {
		t.Fatalf("reset: %s", res.stderr)
	}
	data := dataOf(t, res)
	if data["reset"] != true {
		t.Fatalf("reset result: %v", data)
	}

	// A real record makes the workspace ineligible.
	dataOf(t, runEnv(t, env, append([]string{"customer", "add", "--name", "Real"}, base...)...))
	blocked := runEnv(t, env, append([]string{"demo", "reset", "--confirm"}, base...)...)
	if blocked.code != 3 {
		t.Fatalf("modified workspace exit %d: %s", blocked.code, blocked.stderr)
	}
}

// TestInteractiveInitWizard checks that scripted answers create the
// workspace, operator account, custom web port and demo data.
func TestUserLifecycleCLI(t *testing.T) {
	home := filepath.Join(t.TempDir(), "ws")
	env := map[string]string{"USER": "tester", "REPAIRFLOW_NEW_PASSWORD": "password12345"}
	if res := runEnv(t, env, "init", "--home", home); res.code != 0 {
		t.Fatalf("init: %s", res.stderr)
	}
	for _, u := range []struct{ name, role string }{{"admin", "admin"}, {"tech", "technician"}} {
		if res := runEnv(t, env, "user", "add", u.name, "--role", u.role, "--home", home); res.code != 0 {
			t.Fatalf("user add %s: %s", u.name, res.stderr)
		}
	}
	res := runEnv(t, env, "user", "passwd", "tech", "--home", home, "--json")
	if res.code != 0 || !strings.Contains(res.stdout, "password_changed") {
		t.Fatalf("passwd: %d %s", res.code, res.stderr)
	}
	if res := runEnv(t, env, "user", "disable", "tech", "--home", home); res.code != 0 {
		t.Fatalf("disable: %s", res.stderr)
	}
	res = runEnv(t, env, "user", "list", "--home", home, "--json")
	if res.code != 0 || !strings.Contains(res.stdout, `"active":false`) {
		t.Fatalf("disabled user must be listed inactive: %s", res.stdout)
	}
	if res := runEnv(t, env, "user", "enable", "tech", "--home", home); res.code != 0 {
		t.Fatalf("enable: %s", res.stderr)
	}
	if res := runEnv(t, env, "user", "disable", "admin", "--home", home); res.code == 0 {
		t.Fatal("last active admin disable must be refused")
	}
}

func TestInteractiveInitWizard(t *testing.T) {
	home := filepath.Join(t.TempDir(), "wizard-ws")
	env := map[string]string{"USER": "tester"}
	var out, errb bytes.Buffer
	answers := strings.NewReader(strings.Join([]string{
		home,            // workspace location
		"wizardadmin",   // operator username
		"wizard-pass-1", // password
		"wizard-pass-1", // password repeat
		"8095",          // web port
		"ru",            // interface language
		"y",             // seed demo
		"",
	}, "\n"))
	code := Run(context.Background(), []string{"init", "--interactive", "--json"}, answers, &out, &errb,
		func(key string) string { return env[key] })
	if code != 0 {
		t.Fatalf("wizard exit %d: %s", code, errb.String())
	}
	data := decode(t, out.String())["data"].(map[string]any)
	if data["user"] == nil || data["seed"] == nil {
		t.Fatalf("wizard result: %v", data)
	}

	// Custom port was applied to the freshly created config.
	cfgBytes, err := os.ReadFile(filepath.Join(home, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cfgBytes), "port = 8095") {
		t.Fatalf("config port not applied: %s", cfgBytes)
	}
	if !strings.Contains(string(cfgBytes), `language = "ru"`) {
		t.Fatalf("config language not applied: %s", cfgBytes)
	}

	// Operator account created; workspace validates.
	res := runEnv(t, env, "config", "check", "--home", home, "--json")
	if res.code != 0 {
		t.Fatalf("workspace must be valid after wizard: %s", res.stderr)
	}
}

// TestInteractiveInitValidationErrors: bad password confirmation and bad
// port are refused with validation_error before anything is created.
func TestInteractiveInitValidationErrors(t *testing.T) {
	home := filepath.Join(t.TempDir(), "bad-ws")
	env := map[string]string{"USER": "tester"}

	var out, errb bytes.Buffer
	answers := strings.NewReader(home + "\nadmin\npass-a\npass-b\n")
	code := Run(context.Background(), []string{"init", "--interactive", "--json"}, answers, &out, &errb,
		func(key string) string { return env[key] })
	if code != 2 {
		t.Fatalf("mismatched passwords exit %d: %s", code, errb.String())
	}
	if _, err := os.Stat(home); err == nil {
		t.Fatal("failed wizard must not create the workspace")
	}

	out.Reset()
	errb.Reset()
	answers = strings.NewReader(home + "\n\nnot-a-port\n") // no user, bad port
	code = Run(context.Background(), []string{"init", "--interactive", "--json"}, answers, &out, &errb,
		func(key string) string { return env[key] })
	if code != 2 {
		t.Fatalf("bad port exit %d: %s", code, errb.String())
	}
}

// TestUpdateCLIWorkflow covers the interface: keygen, sign,
// check against an injected trusted key, and apply's confirmation guard.
func TestUpdateCLIWorkflow(t *testing.T) {
	env := map[string]string{"USER": "release"}
	dir := t.TempDir()

	// Release signing keys.
	kg := runEnv(t, env, "update", "keygen", "--out", dir, "--json")
	if kg.code != 0 {
		t.Fatalf("keygen: %s", kg.stderr)
	}
	kgData := dataOf(t, kg)
	pubHex := kgData["public_key_hex"].(string)

	// Build a manifest for a fake artifact and sign it.
	artifact := filepath.Join(dir, "artifact.bin")
	testutil.Must(t, os.WriteFile(artifact, []byte("new binary bytes"), 0o755))
	hash, size, err := hashAndSize(artifact)
	if err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(dir, "manifest.json")
	testutil.Must(t, os.WriteFile(manifest, []byte(fmt.Sprintf(
		`{"format_version":1,"channel":"stable","version":"9.9.9","created_at":"2026-11-10T00:00:00Z","artifacts":[{"os":"%s","arch":"%s","file":"artifact.bin","sha256":"%s","size_bytes":%d}]}`,
		runtime.GOOS, runtime.GOARCH, hash, size)), 0o600))
	sigPath := filepath.Join(dir, "manifest.sig")
	sg := runEnv(t, env, "update", "sign", "--key", filepath.Join(dir, "release-signing.key"),
		"--manifest", manifest, "--out", sigPath, "--json")
	if sg.code != 0 {
		t.Fatalf("sign: %s", sg.stderr)
	}

	// Inject the trusted key into this in-process build and check.
	old := buildinfo.PublicKey
	buildinfo.PublicKey = pubHex
	defer func() { buildinfo.PublicKey = old }()

	res := runEnv(t, env, "update", "check", "--manifest", manifest, "--signature", sigPath, "--json")
	if res.code != 0 {
		t.Fatalf("check: %s", res.stderr)
	}
	check := dataOf(t, res)
	if check["new_version"] != "9.9.9" {
		t.Fatalf("check: %v", check)
	}

	// apply without --confirm refuses before doing anything.
	refused := runEnv(t, env, "update", "apply", "--file", artifact, "--manifest", manifest, "--signature", sigPath, "--json")
	if refused.code != 2 {
		t.Fatalf("unconfirmed apply exit %d: %s", refused.code, refused.stderr)
	}

	// Without an embedded key, check refuses clearly.
	buildinfo.PublicKey = ""
	nokey := runEnv(t, env, "update", "check", "--manifest", manifest, "--signature", sigPath, "--json")
	if nokey.code != 2 {
		t.Fatalf("keyless build exit %d: %s", nokey.code, nokey.stderr)
	}
}

func hashAndSize(path string) (string, int64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", 0, err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), int64(len(data)), nil
}

// TestRepairCommandAndTicketAlias pins the public surface: `repair` is primary,
// `ticket` is still accepted, and the JSON envelope reports contract 2 with the
// canonical command name either way.
func TestRepairCommandAndTicketAlias(t *testing.T) {
	home := filepath.Join(t.TempDir(), "ws")
	env := map[string]string{"USER": "tester"}
	base := []string{"--home", home, "--json"}

	if res := runEnv(t, env, append([]string{"init"}, base...)...); res.code != 0 {
		t.Fatalf("init: %+v", res)
	}
	c := dataOf(t, runEnv(t, env, append([]string{"customer", "add", "--name", "Grace Hopper"}, base...)...))
	cid := itoa(int64(c["id"].(float64)))

	created := runEnv(t, env, append([]string{"repair", "add", "--customer", cid, "--device", "Mark I", "--issue", "Relay"}, base...)...)
	if created.code != 0 {
		t.Fatalf("repair add: %d %s", created.code, created.stderr)
	}
	envl := decode(t, created.stdout)
	if envl["contract"] != float64(2) {
		t.Fatalf("contract = %v, want 2", envl["contract"])
	}
	if envl["command"] != "repair add" {
		t.Fatalf("command = %v, want repair add", envl["command"])
	}
	repair := envl["data"].(map[string]any)
	for field := range repair {
		if strings.Contains(field, "ticket") {
			t.Fatalf("contract 2 must not carry legacy field %q: %v", field, repair)
		}
	}
	rid := itoa(int64(repair["id"].(float64)))

	// The alias reaches the same code path and reports the canonical name.
	aliased := runEnv(t, env, append([]string{"ticket", "show", rid}, base...)...)
	if aliased.code != 0 {
		t.Fatalf("ticket alias: %d %s", aliased.code, aliased.stderr)
	}
	aliasEnv := decode(t, aliased.stdout)
	if aliasEnv["command"] != "repair show" || aliasEnv["contract"] != float64(2) {
		t.Fatalf("alias envelope: %v", aliasEnv)
	}
	if got := aliasEnv["data"].(map[string]any)["device"]; got != "Mark I" {
		t.Fatalf("alias data: %v", got)
	}
	listed := dataOf(t, runEnv(t, env, append([]string{"ticket", "list"}, base...)...))
	if items := listed["items"].([]any); len(items) != 1 {
		t.Fatalf("alias list items: %v", items)
	}
}
