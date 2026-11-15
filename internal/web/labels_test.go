package web_test

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"repairflow/internal/app"
	"repairflow/internal/errs"
)

// TestPrinterSettingsAreAdministratorOnly checks the settings surface: the
// printer section is visible to an administrator, hidden from a technician and
// only an administrator can change it.
func TestPrinterSettingsAreAdministratorOnly(t *testing.T) {
	f := newWeb(t)
	admin := f.login("admin", "password12345")
	tech := f.login("tech", "password12345")

	if body := f.get("/settings", admin); !strings.Contains(body, `id="settings-printer"`) {
		t.Fatalf("administrator settings page has no printer section: %s", body)
	}
	if body := f.get("/settings", tech); strings.Contains(body, `id="settings-printer"`) {
		t.Fatal("a technician must not see the printer settings")
	}

	rec := f.post("/settings/printer", tech, url.Values{
		"csrf_token": {f.csrfFor(tech)}, "driver": {"p15"},
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("technician printer save: status %d, want 403", rec.Code)
	}

	rec = f.post("/settings/printer", admin, url.Values{
		"csrf_token": {f.csrfFor(admin)}, "driver": {"p15"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("printer save: status %d body %s", rec.Code, rec.Body.String())
	}
	settings, err := f.svc.LabelPrinter(context.Background())
	if err != nil || settings.Driver != "p15" {
		t.Fatalf("saved printer = %+v, %v", settings, err)
	}

	// An unknown printer is refused and the saved choice is unchanged.
	rec = f.post("/settings/printer", admin, url.Values{
		"csrf_token": {f.csrfFor(admin)}, "driver": {"hp2000"},
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown printer: status %d, want 400", rec.Code)
	}
	if settings, _ = f.svc.LabelPrinter(context.Background()); settings.Driver != "p15" {
		t.Fatalf("refused save changed the printer to %q", settings.Driver)
	}
}

// TestRepairLabelPrinting covers the repair-page action end to end with a
// stand-in printer: success redirects so a refresh cannot reprint, failures are
// shown on the page, and the audit trail records only confirmed prints.
func TestRepairLabelPrinting(t *testing.T) {
	ctx := context.Background()
	f := newWeb(t)
	var printed []stickerArg
	f.svc.WithLabelPrinter(
		func(_ context.Context, driver string, id int64, name string) error {
			printed = append(printed, stickerArg{driver: driver, id: id, name: name})
			return nil
		},
		func(context.Context, string) error { return nil })

	customer, err := f.svc.CreateCustomer(ctx, "setup", app.CustomerInput{Name: "Maria Pop"})
	if err != nil {
		t.Fatal(err)
	}
	repair, err := f.svc.CreateRepair(ctx, "setup", app.RepairInput{
		CustomerID: customer.ID, DeviceType: "Laptop", Issue: "no power",
	})
	if err != nil {
		t.Fatal(err)
	}
	unassigned, err := f.svc.CreateRepair(ctx, "setup", app.RepairInput{DeviceType: "Phone", Issue: "screen"})
	if err != nil {
		t.Fatal(err)
	}

	admin := f.login("admin", "password12345")
	tech := f.login("tech", "password12345")
	viewer := f.login("viewer", "password12345")
	page := "/repairs/" + itoaLocal(repair.ID)

	// With no printer selected the action explains what to change and prints
	// nothing.
	rec := f.post(page+"/label", tech, url.Values{"csrf_token": {f.csrfFor(tech)}})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "No label printer is selected") {
		t.Fatalf("unconfigured print: status %d body %s", rec.Code, rec.Body.String())
	}
	if len(printed) != 0 {
		t.Fatalf("a disabled printer must not print: %+v", printed)
	}

	if rec := f.post("/settings/printer", admin, url.Values{
		"csrf_token": {f.csrfFor(admin)}, "driver": {"p15"},
	}); rec.Code != http.StatusSeeOther {
		t.Fatalf("printer save: %d", rec.Code)
	}

	// The repair page offers the action once a printer is configured.
	if body := f.get(page, tech); !strings.Contains(body, `id="label-print"`) {
		t.Fatalf("repair page has no print button: %s", body)
	}
	// A viewer may read the repair but not print.
	if rec := f.post(page+"/label", viewer, url.Values{"csrf_token": {f.csrfFor(viewer)}}); rec.Code != http.StatusForbidden {
		t.Fatalf("viewer print: status %d, want 403", rec.Code)
	}

	// A confirmed print redirects with the repair number and customer name, so
	// a page refresh cannot produce a second sticker.
	rec = f.post(page+"/label", tech, url.Values{"csrf_token": {f.csrfFor(tech)}})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != page+"?label=printed" {
		t.Fatalf("print: status %d location %q", rec.Code, rec.Header().Get("Location"))
	}
	if len(printed) != 1 || printed[0] != (stickerArg{driver: "p15", id: repair.ID, name: "Maria Pop"}) {
		t.Fatalf("printed = %+v", printed)
	}
	if body := f.get(page+"?label=printed", tech); !strings.Contains(body, "Label sent to the printer.") {
		t.Fatalf("no success notice after printing: %s", body)
	}

	// A repair without a customer prints its number alone.
	if rec := f.post("/repairs/"+itoaLocal(unassigned.ID)+"/label", tech,
		url.Values{"csrf_token": {f.csrfFor(tech)}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("unassigned print: status %d body %s", rec.Code, rec.Body.String())
	}
	if len(printed) != 2 || printed[1].name != "" {
		t.Fatalf("unassigned sticker = %+v", printed[1])
	}

	// A driver failure is reported on the repair page and is not recorded as a
	// print.
	f.svc.WithLabelPrinter(
		func(context.Context, string, int64, string) error {
			return errs.New(errs.CodeConflict, "printer is out of paper; check the printer before printing again")
		},
		func(context.Context, string) error { return nil })
	rec = f.post(page+"/label", tech, url.Values{"csrf_token": {f.csrfFor(tech)}})
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "out of paper") {
		t.Fatalf("failed print: status %d body %s", rec.Code, rec.Body.String())
	}
	detail, err := f.svc.GetRepair(ctx, repair.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	printedRows := 0
	for _, a := range detail.Activity {
		if a.Action == "label" {
			printedRows++
		}
	}
	if printedRows != 1 {
		t.Fatalf("label activity rows = %d, want only the confirmed print", printedRows)
	}
}

type stickerArg struct {
	driver string
	id     int64
	name   string
}

// TestPrinterTestLabel checks the setup path: the test label needs a selected
// printer, is administrator-only, and redirects so a refresh cannot print again.
func TestPrinterTestLabel(t *testing.T) {
	f := newWeb(t)
	var tested string
	f.svc.WithLabelPrinter(
		func(context.Context, string, int64, string) error { return nil },
		func(_ context.Context, driver string) error { tested = driver; return nil })

	admin := f.login("admin", "password12345")
	tech := f.login("tech", "password12345")

	rec := f.post("/settings/printer/test", admin, url.Values{"csrf_token": {f.csrfFor(admin)}})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "no label printer is selected") {
		t.Fatalf("test label without a printer: status %d body %s", rec.Code, rec.Body.String())
	}

	if rec := f.post("/settings/printer", admin, url.Values{
		"csrf_token": {f.csrfFor(admin)}, "driver": {"p15"},
	}); rec.Code != http.StatusSeeOther {
		t.Fatalf("printer save: status %d", rec.Code)
	}

	if rec := f.post("/settings/printer/test", tech, url.Values{
		"csrf_token": {f.csrfFor(tech)},
	}); rec.Code != http.StatusForbidden {
		t.Fatalf("technician test label: status %d, want 403", rec.Code)
	}

	rec = f.post("/settings/printer/test", admin, url.Values{"csrf_token": {f.csrfFor(admin)}})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/settings?printer=tested" {
		t.Fatalf("test label: status %d location %q", rec.Code, rec.Header().Get("Location"))
	}
	if tested != "p15" {
		t.Fatalf("driver = %q, want p15", tested)
	}
	if body := f.get("/settings?printer=tested", admin); !strings.Contains(body, "Test label sent to the printer.") {
		t.Fatalf("no success notice after the test label: %s", body)
	}
}
