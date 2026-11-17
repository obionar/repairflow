package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/mxschmitt/playwright-go"
)

func TestRepairEntryPaginationInBrowser(t *testing.T) {
	f := start(t)
	page := f.newPage()
	defer page.Close()
	f.login(page, "tech")
	run(t, f.home, "repair", "add", "--device", "Long repair", "--issue", "History")
	for i := 1; i <= 21; i++ {
		run(t, f.home, "repair", "entry", "1", "--kind", "note", "--body", fmt.Sprintf("browser-entry-%02d", i))
	}
	must(page.Goto(f.base + "/repairs/1"))
	if !visible(page, "#entries-next") || visible(page, "#entry-21") {
		t.Fatal("first browser page must bound entries and expose next")
	}
	if dir := os.Getenv("REPAIRFLOW_E2E_REVIEW"); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		_, err := page.Screenshot(playwright.PageScreenshotOptions{
			Path: playwright.String(filepath.Join(dir, "repair-pagination-desktop.png")), FullPage: playwright.Bool(true),
		})
		mustErr(err)
	}
	mobile := f.newPage()
	defer mobile.Close()
	mustErr(mobile.SetViewportSize(390, 844))
	f.login(mobile, "tech")
	must(mobile.Goto(f.base + "/repairs/1"))
	width, err := mobile.Evaluate("document.documentElement.scrollWidth <= window.innerWidth")
	mustErr(err)
	if width != true {
		t.Fatal("paginated repair overflows at phone width")
	}
	if dir := os.Getenv("REPAIRFLOW_E2E_REVIEW"); dir != "" {
		_, err := mobile.Screenshot(playwright.PageScreenshotOptions{
			Path: playwright.String(filepath.Join(dir, "repair-pagination-mobile.png")), FullPage: playwright.Bool(true),
		})
		mustErr(err)
	}
	mustErr(page.Click("#entries-next"))
	must(page.WaitForSelector("#entry-21"))
	if page.URL() != f.base+"/repairs/1?entries_page=2#saved-log" || !visible(page, "#entries-prev") {
		t.Fatalf("next link did not reach the second page: %s", page.URL())
	}
	mustErr(page.Click("#entries-prev"))
	must(page.WaitForSelector("#entry-1"))
	if visible(page, "#entry-21") {
		t.Fatal("previous link did not restore the first page")
	}
}
