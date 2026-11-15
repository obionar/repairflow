package e2e

import (
	"testing"

	"github.com/mxschmitt/playwright-go"
)

func TestRejectedCorrectionAndPickupKeepDrafts(t *testing.T) {
	f := start(t)
	run(t, f.home, "repair", "add", "--device", "Bench Device", "--issue", "Intermittent fault")
	run(t, f.home, "repair", "entry", "1", "--kind", "work", "--body", "Saved entry")
	for _, javascript := range []bool{true, false} {
		name := "with JS"
		if !javascript {
			name = "without JS"
		}
		t.Run(name, func(t *testing.T) {
			ctx := must(f.browser.NewContext(playwright.BrowserNewContextOptions{JavaScriptEnabled: playwright.Bool(javascript)}))
			defer ctx.Close()
			page := must(ctx.NewPage())
			f.login(page, "tech")
			must(page.Goto(f.base + "/repairs/1"))
			mustErr(page.Click("#entry-edit-toggle-1"))
			mustErr(page.Fill("#body-1", "Keep this correction <draft>"))
			must(page.Evaluate(`document.querySelector('#entry-edit-panel-1 input[name="kind"]').value = 'invalid'`))
			mustErr(page.Click("#entry-edit-1"))
			must(page.WaitForSelector("#error"))
			must(page.WaitForSelector("#entry-edit-panel-1[open]"))
			if value := must(page.InputValue("#body-1")); value != "Keep this correction <draft>" {
				t.Fatalf("correction draft lost: %q", value)
			}
			if value := must(page.InputValue(`#entry-edit-panel-1 input[name="kind"]`)); value != "work" {
				t.Fatalf("correction cannot be retried with valid kind: %q", value)
			}
			mustErr(page.Fill("#pickup_notes", "Keep the handover notes"))
			must(page.Evaluate(`document.querySelector('#pickup-form').noValidate = true`))
			mustErr(page.Click("#pickup-submit"))
			must(page.WaitForSelector("#error"))
			if value := must(page.InputValue("#pickup_notes")); value != "Keep the handover notes" {
				t.Fatalf("pickup draft lost: %q", value)
			}
			mustErr(page.Focus("#picked_up_by"))
			mustErr(page.Keyboard().Type("Bench Collector"))
			mustErr(page.Keyboard().Press("Tab"))
			if focused := must(page.Evaluate(`document.activeElement.id`)); focused != "pickup_notes" {
				t.Fatalf("pickup draft keyboard order: %v", focused)
			}
		})
	}
}
