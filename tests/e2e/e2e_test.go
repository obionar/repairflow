package e2e

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/mxschmitt/playwright-go"
)

func textOf(t *testing.T, page playwright.Page, sel string) string {
	t.Helper()
	v, err := page.TextContent(sel)
	if err != nil {
		t.Fatalf("text %s: %v", sel, err)
	}
	return strings.TrimSpace(v)
}

func visible(page playwright.Page, sel string) bool {
	v, _ := page.IsVisible(sel)
	return v
}

// ---- auth flow ------------------------------------------------------------

func TestLoginLogoutAndSession(t *testing.T) {
	f := start(t)
	page := f.newPage()
	defer page.Close()

	must(page.Goto(f.base + "/"))
	// Unauthenticated access redirects to the login page.
	if !strings.Contains(page.URL(), "/login") {
		t.Fatalf("expected login redirect, got %s", page.URL())
	}

	// Bad credentials keep the user on the login page with an error banner.
	mustErr(page.Fill("#username", "admin"))
	mustErr(page.Fill("#password", "wrong-password"))
	mustErr(page.Click("#login-submit"))
	if !strings.Contains(page.URL(), "/login") {
		t.Fatalf("bad login must stay on login, got %s", page.URL())
	}
	if !visible(page, "#error") {
		t.Fatal("login error must be visible (accessible alert)")
	}

	// Real login lands on the dashboard with the identity shown.
	f.login(page, "admin")
	if !strings.Contains(textOf(t, page, "#userbox"), "admin (admin)") {
		t.Fatalf("userbox: %q", textOf(t, page, "#userbox"))
	}
	if !strings.Contains(textOf(t, page, "#dashboard-heading"), "Dashboard") {
		t.Fatal("dashboard heading missing")
	}

	// Logout returns to login and the old page is protected again.
	mustErr(page.Click("#logout"))
	if !strings.Contains(page.URL(), "/login") {
		t.Fatalf("logout must land on login, got %s", page.URL())
	}
	must(page.Goto(f.base + "/repairs"))
	if !strings.Contains(page.URL(), "/login") {
		t.Fatal("session must be gone after logout")
	}
}

// ---- full operator workflow via stable selectors --------------------------

func TestCustomerAndTicketWorkflow(t *testing.T) {
	f := start(t)
	page := f.newPage()
	defer page.Close()
	f.login(page, "tech")

	// Customer (agent-navigation: stable ids throughout).
	must(page.Goto(f.base + "/customers"))
	mustErr(page.Click("#new-customer"))
	mustErr(page.Fill("#name", "E2E Customer ünïcode"))
	mustErr(page.Fill("#phone", "+49 123 456"))
	mustErr(page.Click("#customer-submit"))
	must(page.WaitForSelector("#customer-heading"))
	if !strings.Contains(textOf(t, page, "#customer-heading"), "E2E Customer") {
		t.Fatalf("customer heading: %q", textOf(t, page, "#customer-heading"))
	}

	// Ticket.
	must(page.Goto(f.base + "/repairs"))
	mustErr(page.Click("#new-ticket"))
	mustErr(page.Fill("#customer_name", "E2E Customer ünïcode"))
	mustErr(page.Fill("#device", "E2E Device"))
	mustErr(page.Fill("#issue", "screen cracked during testing"))
	mustErr(page.Click("#ticket-submit"))
	must(page.WaitForSelector("#ticket-heading"))
	if !strings.Contains(textOf(t, page, "#ticket-heading"), "E2E Device") {
		t.Fatalf("ticket heading: %q", textOf(t, page, "#ticket-heading"))
	}

	// Work-log entry.
	must(page.SelectOption("#kind", playwright.SelectOptionValues{ValuesOrLabels: &[]string{"work"}}))
	mustErr(page.Fill("#body", "Opened the device and inspected the panel"))
	mustErr(page.Click("#entry-submit"))
	must(page.WaitForSelector("#entry-1"))
	if !strings.Contains(textOf(t, page, "#entry-1"), "inspected the panel") {
		t.Fatalf("entry: %q", textOf(t, page, "#entry-1"))
	}

	// Entry correction (in-place edit with audit revision).
	mustErr(page.Click("#entry-edit-toggle-1")) // expand the disclosure first
	mustErr(page.Fill("#body-1", "Opened the device and inspected the cracked panel"))
	mustErr(page.Click("#entry-edit-1"))
	must(page.WaitForSelector("#entry-1"))
	if !strings.Contains(textOf(t, page, "#entry-1"), "cracked panel") {
		t.Fatalf("entry edit not applied: %q", textOf(t, page, "#entry-1"))
	}

	// Stage transition.
	must(page.SelectOption("#stage", playwright.SelectOptionValues{ValuesOrLabels: &[]string{"in_progress"}}))
	mustErr(page.Click("#stage-submit"))
	must(page.WaitForSelector("#ticket-stage"))
	if textOf(t, page, "#ticket-stage") != "in_progress" {
		t.Fatalf("stage: %q", textOf(t, page, "#ticket-stage"))
	}

	// Part line with decimal money/quantity.
	mustErr(page.Fill("#description", "Replacement screen"))
	mustErr(page.Fill("#quantity", "1.5"))
	mustErr(page.Fill("#unit_price", "84.99"))
	mustErr(page.Click("#part-submit"))
	must(page.WaitForSelector("#part-1"))
	if !strings.Contains(textOf(t, page, "#part-1"), "84.99") {
		t.Fatalf("part line: %q", textOf(t, page, "#part-1"))
	}

	// Edit the part line, then exercise reversible removal and restore.
	mustErr(page.Click("#part-edit-toggle-1"))
	mustErr(page.Fill("#part-quantity-1", "2"))
	mustErr(page.Fill("#part-price-1", "90.00"))
	mustErr(page.Click("#part-save-1"))
	must(page.WaitForSelector("#part-1"))
	if !strings.Contains(textOf(t, page, "#part-1"), "180.00") {
		t.Fatalf("edited part total: %q", textOf(t, page, "#part-1"))
	}
	mustErr(page.Click("#part-remove-1"))
	must(page.WaitForSelector(".deleted-parts"))
	mustErr(page.Click(".deleted-parts > summary"))
	must(page.WaitForSelector("#deleted-part-1", playwright.PageWaitForSelectorOptions{State: playwright.WaitForSelectorStateVisible}))
	mustErr(page.Click("#part-restore-1"))
	must(page.WaitForSelector("#part-1"))

	// Estimate snapshot with ticket discount.
	mustErr(page.Fill("#discount_percent", "10"))
	mustErr(page.Click("#estimate-submit"))
	must(page.WaitForSelector("#estimate-1"))
	if !strings.Contains(textOf(t, page, "#estimate-1"), "accepted") && !strings.Contains(textOf(t, page, "#estimate-1"), "draft") {
		t.Fatalf("estimate: %q", textOf(t, page, "#estimate-1"))
	}

	// Pickup record.
	mustErr(page.Fill("#pickup_at", "2026-11-11"))
	mustErr(page.Fill("#picked_up_by", "Customer Person"))
	mustErr(page.Click("#pickup-submit"))
	must(page.WaitForSelector("#pickup-state"))
	if !strings.Contains(textOf(t, page, "#pickup-state"), "Customer Person") {
		t.Fatalf("pickup: %q", textOf(t, page, "#pickup-state"))
	}

	// Activity trail is present and attributed.
	if !strings.Contains(textOf(t, page, "#activity-list"), "web:tech") {
		t.Fatal("activity must attribute the web actor")
	}
	if !strings.Contains(textOf(t, page, "#pickup-state"), "Customer Person") {
		t.Fatalf("pickup flag missing from ticket status: %q", textOf(t, page, "#pickup-state"))
	}

	// Pickup filter: the collected repair appears only in the collected view.
	must(page.Goto(f.base + "/repairs?picked_up=no"))
	if strings.Contains(pageContent(t, page), "E2E Device") {
		t.Fatal("in-shop filter must exclude a collected repair")
	}
	must(page.Goto(f.base + "/repairs?picked_up=yes"))
	if !strings.Contains(pageContent(t, page), "E2E Device") {
		t.Fatal("picked-up filter must include a collected repair")
	}
}

// ---- gallery upload, byte-exact download, caption, detach -----------------

func TestGalleryUploadDownloadAndDetach(t *testing.T) {
	f := start(t)
	page := f.newPage()
	defer page.Close()
	f.login(page, "tech")
	f.createTicket(page)

	// A binary file (firmware-style) and a JPEG photo.
	binPayload := []byte{0x7f, 'E', 'L', 'F', 0x02, 0x01, 0x01, 0x00, 0xde, 0xad, 0xbe, 0xef, 0x00, 0xff}
	jpgPayload := []byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10, 0x4a, 0x46, 0x49, 0x46, 0x00, 0x01, 0xde, 0xad, 0xbe, 0xef}
	binSrc := filepath.Join(t.TempDir(), "firmware dump.bin")
	jpgSrc := filepath.Join(t.TempDir(), "intake photo ünïcode.jpg")
	if err := os.WriteFile(binSrc, binPayload, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(jpgSrc, jpgPayload, 0o600); err != nil {
		t.Fatal(err)
	}

	// Upload the binary linked to no entry; upload the photo with a caption.
	t.Log("step: upload binary")
	mustErr(page.SetInputFiles("#file", binSrc))
	mustErr(page.Click("#attachment-submit"))
	must(page.WaitForSelector("#attachment-1"))
	t.Log("step: upload photo")
	mustErr(page.SetInputFiles("#file", jpgSrc))
	mustErr(page.Fill("#caption", "damage close-up"))
	mustErr(page.Click("#attachment-submit"))
	must(page.WaitForSelector("#attachment-2"))
	if !strings.Contains(textOf(t, page, "#attachment-2"), "intake photo") {
		t.Fatalf("attachment listed: %q", textOf(t, page, "#attachment-2"))
	}

	// Non-preview binary: browser downloads it (attachment disposition) and
	// the bytes are identical to the original.
	t.Log("step: download binary")
	download := must(page.ExpectDownload(func() error {
		return page.Click("#attachment-download-1")
	}))
	dest := filepath.Join(t.TempDir(), "downloaded.bin")
	mustErr(download.SaveAs(dest))
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, binPayload) {
		t.Fatalf("downloaded bytes differ: %d vs %d bytes", len(got), len(binPayload))
	}

	// Preview-safe JPEG: clicking navigates to the inline preview instead of
	// downloading through the allowlist and serves the original bytes.
	t.Log("step: view jpeg inline")
	resp := must(page.ExpectNavigation(func() error {
		return page.Click("#attachment-download-2")
	}))
	if resp.Status() != 200 {
		t.Fatalf("jpeg view: status %d", resp.Status())
	}
	headers := resp.Headers()
	ct := headers["Content-Type"]
	if ct == "" {
		ct = headers["content-type"]
	}
	if ct != "image/jpeg" {
		t.Fatalf("jpeg must serve inline as image/jpeg, got %q (%v)", ct, headers)
	}
	// Return to the ticket page for the remaining flows.
	must(page.Goto(f.base + "/repairs/1"))

	// Caption edit persists.
	t.Log("step: caption")
	mustErr(page.Fill("#caption-2", "edited in browser"))
	mustErr(page.Click("#caption-save-2"))
	must(page.WaitForSelector("#attachment-2"))
	if !strings.Contains(textOf(t, page, "#attachment-2"), "edited in browser") {
		t.Fatalf("caption not saved: %q", textOf(t, page, "#attachment-2"))
	}

	// Detach removes it from the normal gallery (recoverable archive).
	t.Log("step: detach")
	mustErr(page.Click("#detach-2"))
	must(page.WaitForSelector("#attachment-2", playwright.PageWaitForSelectorOptions{State: playwright.WaitForSelectorStateDetached}))
	if visible(page, "#attachment-2") {
		t.Fatal("detached attachment must leave the normal gallery")
	}
	if !visible(page, "#attachment-1") {
		t.Fatal("other attachment must remain")
	}

	// Link, unlink and restore are available from the same files section.
	t.Log("step: link/unlink/restore")
	mustErr(page.Fill("#body", "media evidence"))
	mustErr(page.Click("#entry-submit"))
	must(page.WaitForSelector("#entry-1"))
	mustErr(page.Click("#attachment-links-1 > summary"))
	mustErr(page.Fill("#link-entry-1", "1"))
	mustErr(page.Click("#attachment-link-1"))
	must(page.WaitForSelector("#attachment-1", playwright.PageWaitForSelectorOptions{State: playwright.WaitForSelectorStateVisible}))
	if !strings.Contains(textOf(t, page, "#attachment-1"), "Log links (1)") {
		t.Fatal("entry link must be visible in the gallery")
	}
	mustErr(page.Click("#attachment-links-1 > summary"))
	mustErr(page.Fill("#unlink-entry-1", "1"))
	mustErr(page.Click("#attachment-unlink-1"))
	must(page.WaitForSelector("#attachment-1", playwright.PageWaitForSelectorOptions{State: playwright.WaitForSelectorStateVisible}))

	mustErr(page.Click(".deleted-attachments > summary"))
	must(page.WaitForSelector("#detached-attachment-2", playwright.PageWaitForSelectorOptions{State: playwright.WaitForSelectorStateVisible}))
	mustErr(page.Click("#attachment-restore-2"))
	must(page.WaitForSelector("#attachment-2", playwright.PageWaitForSelectorOptions{State: playwright.WaitForSelectorStateVisible}))
}

// ---- role enforcement in the browser --------------------------------------

func TestRoleEnforcementInBrowser(t *testing.T) {
	f := start(t)

	// Prepare one ticket as technician.
	tech := f.newPage()
	f.login(tech, "tech")
	f.createTicket(tech)
	if strings.Contains(pageContent(t, tech), "purge") {
		t.Fatal("technician UI must not offer purge")
	}
	tech.Close()

	// Viewer: reads fine, write surfaces are refused server-side.
	page := f.newPage()
	f.login(page, "viewer")
	must(page.Goto(f.base + "/customers/new"))
	if !strings.Contains(textOf(t, page, "#error-heading"), "went wrong") {
		t.Fatalf("viewer must be refused the customer form, got %s", page.URL())
	}
	must(page.Goto(f.base + "/repairs/1"))
	if visible(page, "#entry-form button") {
		t.Fatal("viewer must not see write controls")
	}
	page.Close()
}

func pageContent(t *testing.T, page playwright.Page) string {
	t.Helper()
	content, err := page.Content()
	if err != nil {
		t.Fatal(err)
	}
	return content
}

// ---- mobile viewport ------------------------------------------------------

func TestMobileViewportUsable(t *testing.T) {
	f := start(t)
	page := f.newPage()
	defer page.Close()
	mustErr(page.SetViewportSize(375, 667)) // phone-sized
	f.login(page, "tech")

	// Form controls are visible and typed input works at phone width.
	must(page.Goto(f.base + "/customers"))
	mustErr(page.Click("#new-customer"))
	if !visible(page, "#name") || !visible(page, "#customer-submit") {
		t.Fatal("form must be usable at mobile width")
	}
	mustErr(page.Fill("#name", "Mobile User"))
	mustErr(page.Click("#customer-submit"))
	must(page.WaitForSelector("#customer-heading"))
	f.createTicket(page)

	// Mobile-browser upload: file picker + submit at phone width.
	upload := filepath.Join(t.TempDir(), "mobile upload.bin")
	if err := os.WriteFile(upload, []byte{0x7f, 'E', 'L', 'F', 0x01, 0x02}, 0o600); err != nil {
		t.Fatal(err)
	}
	mustErr(page.SetInputFiles("#file", upload))
	mustErr(page.Click("#attachment-submit"))
	must(page.WaitForSelector("#attachment-1"))
	if !strings.Contains(textOf(t, page, "#attachment-1"), "mobile upload.bin") {
		t.Fatalf("mobile upload not listed: %q", textOf(t, page, "#attachment-1"))
	}

	// No horizontal overflow (layout must not require sideways scrolling).
	must(page.Goto(f.base + "/repairs/1"))
	overflow := must(page.Evaluate(`() => document.documentElement.scrollWidth - document.documentElement.clientWidth`))
	if v, ok := overflow.(float64); ok && v > 2 {
		t.Fatalf("horizontal overflow of %.0fpx at mobile width", v)
	}
}

// waitFoldState waits until panels.js has persisted the fold state. The DOM
// toggle event is delivered asynchronously, so reloading immediately after the
// click would race the localStorage write (observed as an intermittent failure
// in TestFoldPanelStatePersists).
func waitFoldState(t *testing.T, page playwright.Page, panelID string, open bool) {
	t.Helper()
	want := "closed"
	if open {
		want = "open"
	}
	if _, err := page.WaitForFunction(
		`([id, want]) => localStorage.getItem('repairflow.panel.' + id) === want`,
		[]any{panelID, want},
	); err != nil {
		t.Fatalf("fold state %s=%s was not persisted: %v", panelID, want, err)
	}
}

func TestFoldPanelStatePersists(t *testing.T) {
	f := start(t)
	page := f.newPage()
	defer page.Close()
	f.login(page, "tech")
	f.createTicket(page)
	must(page.Goto(f.base + "/repairs/1"))

	mustErr(page.Click("#activity-panel > summary"))
	must(page.WaitForSelector("#activity-panel[open]"))
	waitFoldState(t, page, "activity-panel", true)
	must(page.Reload())
	isOpen := must(page.Evaluate(`() => document.querySelector("#activity-panel").open`))
	if isOpen != true {
		t.Fatal("activity fold must remain open after reload")
	}
	mustErr(page.Click("#activity-panel > summary"))
	must(page.WaitForSelector("#activity-panel:not([open])"))
	waitFoldState(t, page, "activity-panel", false)
	must(page.Reload())
	isOpen = must(page.Evaluate(`() => document.querySelector("#activity-panel").open`))
	if isOpen != false {
		t.Fatal("activity fold must remain closed after reload")
	}
}

func TestCoreWorkflowWithoutJavaScript(t *testing.T) {
	f := start(t)
	context := must(f.browser.NewContext(playwright.BrowserNewContextOptions{
		JavaScriptEnabled: playwright.Bool(false),
		Viewport:          &playwright.Size{Width: 375, Height: 667},
	}))
	defer context.Close()
	page := must(context.NewPage())

	must(page.Goto(f.base + "/login"))
	mustErr(page.Fill("#username", "tech"))
	mustErr(page.Fill("#password", "password12345"))
	mustErr(page.Click("#login-submit"))
	mustErr(page.WaitForURL(f.base + "/"))

	// The CSS drawer remains usable without the enhancement script.
	mustErr(page.Click("#navigation-menu > summary"))
	must(page.WaitForSelector("#main-navigation a[href='/repairs']", playwright.PageWaitForSelectorOptions{State: playwright.WaitForSelectorStateVisible}))
	if !visible(page, "#main-navigation a[href='/repairs']") {
		t.Fatal("mobile navigation must work without JavaScript")
	}

	must(page.Goto(f.base + "/customers"))
	mustErr(page.Click("#new-customer"))
	mustErr(page.Fill("#name", "No JS Customer"))
	mustErr(page.Click("#customer-submit"))
	must(page.WaitForSelector("#customer-heading"))
	must(page.Goto(f.base + "/repairs"))
	mustErr(page.Click("#new-ticket"))
	mustErr(page.Fill("#customer_name", "No JS Customer"))
	mustErr(page.Fill("#device", "No JS Device"))
	mustErr(page.Fill("#issue", "no-js issue"))
	mustErr(page.Click("#ticket-submit"))
	must(page.WaitForSelector("#ticket-heading"))
	mustErr(page.Fill("#body", "entry without JavaScript"))
	mustErr(page.Click("#entry-submit"))
	must(page.WaitForSelector("#entry-1"))

	// Fold panels are native details elements and open without scripting.
	mustErr(page.Click("#activity-panel > summary"))
	if !visible(page, "#activity-list") {
		t.Fatal("fold panels must open without JavaScript")
	}
}

// ---- Progressive navigation enhancement (focus, Escape, backdrop) --------

func TestMobileNavigationKeyboardAndBackdrop(t *testing.T) {
	f := start(t)
	page := f.newPage()
	defer page.Close()
	mustErr(page.SetViewportSize(375, 667))
	f.login(page, "tech")
	must(page.Goto(f.base + "/"))

	mustErr(page.Click("#navigation-menu > summary"))
	must(page.WaitForSelector("#main-navigation", playwright.PageWaitForSelectorOptions{State: playwright.WaitForSelectorStateVisible}))
	if expanded, _ := page.GetAttribute("#navigation-menu > summary", "aria-expanded"); expanded != "true" {
		t.Fatalf("open drawer must set aria-expanded=true, got %q", expanded)
	}
	inside := must(page.Evaluate(`() => !!document.activeElement.closest("#main-navigation")`))
	if inside != true {
		t.Fatal("opening the drawer must move focus into the navigation")
	}

	mustErr(page.Keyboard().Press("Escape"))
	must(page.WaitForSelector("#main-navigation", playwright.PageWaitForSelectorOptions{State: playwright.WaitForSelectorStateHidden}))
	focused := must(page.Evaluate(`() => document.activeElement === document.querySelector("#navigation-menu > summary")`))
	if focused != true {
		t.Fatal("Escape must close the drawer and return focus to its toggle")
	}

	mustErr(page.Click("#navigation-menu > summary"))
	must(page.WaitForSelector("#main-navigation", playwright.PageWaitForSelectorOptions{State: playwright.WaitForSelectorStateVisible}))
	mustErr(page.Click(".nav-backdrop", playwright.PageClickOptions{Position: &playwright.Position{X: 340, Y: 200}}))
	must(page.WaitForSelector("#main-navigation", playwright.PageWaitForSelectorOptions{State: playwright.WaitForSelectorStateHidden}))
}

func TestResponsiveNoHorizontalOverflow(t *testing.T) {
	f := start(t)
	page := f.newPage()
	defer page.Close()
	f.login(page, "tech")
	f.createTicket(page)
	for _, width := range []int{360, 390, 768, 1280, 1440} {
		mustErr(page.SetViewportSize(width, 900))
		for _, path := range []string{"/", "/customers", "/customers/1", "/repairs", "/repairs/1", "/settings"} {
			must(page.Goto(f.base + path))
			overflow := must(page.Evaluate(`() => document.documentElement.scrollWidth - document.documentElement.clientWidth`))
			if v, ok := overflow.(float64); ok && v > 1 {
				t.Errorf("%dpx %s: horizontal overflow %.0fpx", width, path, v)
			}
		}
	}
}

// ---- accessibility basics -------------------------------------------------

func TestAccessibilityBasics(t *testing.T) {
	f := start(t)
	page := f.newPage()
	defer page.Close()
	f.login(page, "admin")
	f.createTicket(page)

	for _, path := range []string{"/", "/customers", "/customers/new", "/repairs", "/repairs/new", "/repairs/1", "/settings"} {
		must(page.Goto(f.base + path))
		audit := must(page.Evaluate(`() => {
			const problems = [];
			if (document.documentElement.lang !== "en") problems.push("missing lang");
			const h1 = document.querySelectorAll("h1");
			if (h1.length !== 1) problems.push("h1 count " + h1.length);
			for (const el of document.querySelectorAll("input, select, textarea")) {
				if (el.type === "hidden") continue;
				const labelled = el.id && document.querySelector('label[for="' + el.id + '"]') ||
					el.closest("label") || el.getAttribute("aria-label");
				if (!labelled) problems.push("unlabelled control #" + el.id + " (" + el.name + ")");
			}
			for (const b of document.querySelectorAll("button")) {
				if (!b.textContent.trim() && !b.getAttribute("aria-label")) problems.push("button without accessible name");
			}
			return JSON.stringify(problems);
		}`))
		var problems []string
		if s, ok := audit.(string); ok {
			json.Unmarshal([]byte(s), &problems)
		}
		if len(problems) > 0 {
			t.Errorf("%s: accessibility problems: %v", path, problems)
		}
	}
}

// ---- error handling in the browser ---------------------------------------

func TestErrorPageReadable(t *testing.T) {
	f := start(t)
	page := f.newPage()
	defer page.Close()
	f.login(page, "tech")

	must(page.Goto(f.base + "/repairs/999999"))
	if !strings.Contains(textOf(t, page, "#error-heading"), "went wrong") {
		t.Fatal("missing record must render the error page")
	}
	if !strings.Contains(textOf(t, page, "#error"), "no longer exists") {
		t.Fatalf("error message: %q", textOf(t, page, "#error"))
	}
	// The error page links back into the application (recoverable UX).
	if !visible(page, "#error-back") {
		t.Fatal("error page must offer a way back")
	}
}

// ---- graceful shutdown and restart ----------------------------------------

func TestGracefulShutdownAndRestart(t *testing.T) {
	f := start(t)

	// The server answers before shutdown.
	page := f.newPage()
	must(page.Goto(f.base + "/login"))
	page.Close()

	// SIGTERM must trigger a graceful stop (exit 0), not a crash.
	if err := f.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- f.cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("graceful shutdown must exit 0, got: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serve did not stop within 10s of SIGTERM")
	}

	// The workspace is intact and a restart serves again (service restart
	// semantics: same command, same workspace).
	if resp, err := http.Get(f.base + "/login"); err == nil {
		resp.Body.Close()
		t.Fatal("port must be released after shutdown")
	}
	cmd := exec.Command(binaryPath, "serve", "--home", f.home)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	waitHTTP(t, f.base+"/login")
}

// TestSettingsFoldKeyboardAndCSRF covers C-06: the password fold is closed by
// default, opens from the keyboard, and its CSRF token stays discoverable by
// automation while it is closed.
func TestSettingsFoldKeyboardAndCSRF(t *testing.T) {
	f := start(t)
	page := f.newPage()
	defer page.Close()
	f.login(page, "tech")
	must(page.Goto(f.base + "/settings"))

	if open := must(page.Evaluate(`() => document.querySelector("#settings-password").open`)); open != false {
		t.Fatal("settings password fold must be closed by default")
	}
	token := must(page.Evaluate(`() => document.querySelector('#password-form input[name="csrf_token"]').value`))
	if s, _ := token.(string); s == "" {
		t.Fatal("CSRF token must be discoverable by automation while the fold is closed")
	}

	// Keyboard: focus the summary and reveal the form with Enter.
	mustErr(page.Focus("#settings-password > summary"))
	if err := page.Keyboard().Press("Enter"); err != nil {
		t.Fatalf("keyboard activate fold: %v", err)
	}
	must(page.WaitForSelector("#settings-password[open]"))
	mustErr(page.Focus("#current_password"))
	if focused := must(page.Evaluate(`() => document.activeElement.id`)); focused != "current_password" {
		t.Fatalf("keyboard navigation into the fold failed, focus=%v", focused)
	}
	mustErr(page.Focus("#current_password"))
	if err := page.Keyboard().Press("Tab"); err != nil {
		t.Fatalf("tab inside fold: %v", err)
	}
	if focused := must(page.Evaluate(`() => document.activeElement.id`)); focused != "new_password" {
		t.Fatalf("tab order inside the fold, focus=%v", focused)
	}
}
