// Package e2e runs browser-level web UI tests with Playwright
// driving the system Chromium against the real repairflow binary.
//
// Test tooling dependency: github.com/mxschmitt/playwright-go (module path of
// playwright-community/playwright-go at its published tags). This lives in its
// own module (tests/e2e) so the runtime module stays dependency-clean.
package e2e

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mxschmitt/playwright-go"
)

var binaryPath string

func TestMain(m *testing.M) {
	if err := playwright.Install(&playwright.RunOptions{SkipInstallBrowsers: true}); err != nil {
		panic("playwright driver install failed: " + err.Error())
	}
	// Build (or reuse) the binary under test from the repository root.
	root, err := filepath.Abs("../..")
	if err != nil {
		panic(err)
	}
	binaryPath = filepath.Join(root, "repairflow")
	// Always rebuild: a stale binary would silently test yesterday's code.
	build := exec.Command("make", "build")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		panic("build failed: " + string(out))
	}
	os.Exit(m.Run())
}

// fixture is one running workspace + server + browser.
type fixture struct {
	t       *testing.T
	home    string
	port    int
	cmd     *exec.Cmd
	pw      *playwright.Playwright
	browser playwright.Browser
	base    string
}

func start(t *testing.T) *fixture {
	t.Helper()
	home := filepath.Join(t.TempDir(), "ws")
	run(t, home, "init")
	// Users: admin, tech, viewer (passwords via env — the CLI contract).
	for _, u := range []struct{ name, role string }{
		{"admin", "admin"}, {"tech", "technician"}, {"viewer", "viewer"},
	} {
		runEnv(t, map[string]string{"REPAIRFLOW_NEW_PASSWORD": "password12345"}, home, "user", "add", u.name, "--role", u.role)
	}
	// No demo data: tests create their own records through the UI so element
	// ids are deterministic (entry-1, part-1, ...).

	port := freePort(t)
	cfgPath := filepath.Join(home, "config.toml")
	cfg, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg = []byte(strings.Replace(string(cfg), "port = 8091", fmt.Sprintf("port = %d", port), 1))
	if err := os.WriteFile(cfgPath, cfg, 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(binaryPath, "serve", "--home", home)
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		t.Fatalf("serve: %v", err)
	}
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	waitHTTP(t, base+"/login")

	pw, err := playwright.Run()
	if err != nil {
		t.Fatalf("playwright run: %v", err)
	}
	browser, err := pw.Chromium.Launch(playwright.BrowserTypeLaunchOptions{
		ExecutablePath: playwright.String("/usr/bin/chromium"),
		Headless:       playwright.Bool(true),
	})
	if err != nil {
		t.Fatalf("chromium launch: %v", err)
	}
	f := &fixture{t: t, home: home, port: port, cmd: cmd, pw: pw, browser: browser, base: base}
	t.Cleanup(func() {
		browser.Close()
		pw.Stop()
		cmd.Process.Kill()
		cmd.Wait()
	})
	return f
}

func (f *fixture) newPage() playwright.Page {
	f.t.Helper()
	page, err := f.browser.NewPage()
	if err != nil {
		f.t.Fatalf("new page: %v", err)
	}
	return page
}

// createTicket walks the UI: one customer plus one ticket (ids 1 and 1).
func (f *fixture) createTicket(page playwright.Page) {
	f.t.Helper()
	must(page.Goto(f.base + "/customers"))
	mustErr(page.Click("#new-customer"))
	mustErr(page.Fill("#name", "Workflow Owner"))
	mustErr(page.Click("#customer-submit"))
	must(page.WaitForSelector("#customer-heading"))
	must(page.Goto(f.base + "/tickets"))
	mustErr(page.Click("#new-ticket"))
	mustErr(page.Fill("#customer_name", "Workflow Owner"))
	mustErr(page.Fill("#device", "Workflow Device"))
	mustErr(page.Fill("#issue", "needs verification"))
	mustErr(page.Click("#ticket-submit"))
	must(page.WaitForSelector("#ticket-heading"))
}

// login performs the real form login and returns the page at the dashboard.
func (f *fixture) login(page playwright.Page, username string) {
	f.t.Helper()
	must(page.Goto(f.base + "/login"))
	mustErr(page.Fill("#username", username))
	mustErr(page.Fill("#password", "password12345"))
	mustErr(page.Click("#login-submit"))
	if err := page.WaitForURL(f.base + "/"); err != nil {
		f.t.Fatalf("login did not reach dashboard: %v", err)
	}
}

// ---- helpers -----------------------------------------------------------

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func mustErr(err error) {
	if err != nil {
		panic(err)
	}
}

func run(t *testing.T, home string, args ...string) string {
	t.Helper()
	return runEnv(t, nil, home, args...)
}

func runEnv(t *testing.T, env map[string]string, home string, args ...string) string {
	t.Helper()
	cmd := exec.Command(binaryPath, append(args, "--home", home)...)
	for k, v := range env {
		cmd.Env = append(os.Environ(), k+"="+v)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("repairflow %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func waitHTTP(t *testing.T, url string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if resp, err := http.Get(url); err == nil {
			resp.Body.Close()
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("server did not come up at %s", url)
}
