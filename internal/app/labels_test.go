package app_test

import (
	"context"
	"errors"
	"testing"

	"repairflow/internal/app"
	"repairflow/internal/errs"
)

type stickerCall struct {
	driver string
	id     int64
	name   string
}

func TestLabelPrinterSticker(t *testing.T) {
	ctx := context.Background()
	svc := newService(t)
	var calls []stickerCall
	svc.WithLabelPrinter(
		func(_ context.Context, driver string, id int64, name string) error {
			calls = append(calls, stickerCall{driver: driver, id: id, name: name})
			return nil
		},
		func(_ context.Context, driver string) error {
			calls = append(calls, stickerCall{driver: driver})
			return nil
		})

	// New workspaces start with printing switched off.
	settings, err := svc.LabelPrinter(ctx)
	if err != nil || settings.Driver != "" {
		t.Fatalf("default printer = %+v, %v", settings, err)
	}

	customer, err := svc.CreateCustomer(ctx, "setup", app.CustomerInput{Name: "Maria Pop"})
	if err != nil {
		t.Fatal(err)
	}
	repair, err := svc.CreateRepair(ctx, "setup", app.RepairInput{
		CustomerID: customer.ID, DeviceType: "Laptop", Issue: "no power",
	})
	if err != nil {
		t.Fatal(err)
	}
	unassigned, err := svc.CreateRepair(ctx, "setup", app.RepairInput{DeviceType: "Phone", Issue: "screen"})
	if err != nil {
		t.Fatal(err)
	}

	// With no printer selected nothing is sent anywhere.
	if _, err := svc.PrintRepairLabel(ctx, "web:tech", repair.ID); errs.CodeOf(err) != errs.CodeValidation {
		t.Fatalf("disabled printer: code = %s (err %v), want validation", errs.CodeOf(err), err)
	}
	if len(calls) != 0 {
		t.Fatalf("a disabled printer must not reach the driver: %+v", calls)
	}

	// Only an administrator changes the workspace-wide setting.
	if _, err := svc.SetLabelPrinter(ctx, app.RoleTechnician, "p15"); errs.CodeOf(err) != errs.CodePermission {
		t.Fatalf("technician set: code = %s, want permission", errs.CodeOf(err))
	}
	if _, err := svc.SetLabelPrinter(ctx, app.RoleAdmin, "hp2000"); errs.CodeOf(err) != errs.CodeValidation {
		t.Fatalf("unknown driver: code = %s, want validation", errs.CodeOf(err))
	}
	settings, err = svc.SetLabelPrinter(ctx, app.RoleAdmin, " p15 ")
	if err != nil || settings.Driver != "p15" {
		t.Fatalf("saved printer = %+v, %v", settings, err)
	}

	// The sticker carries the saved repair number and the customer name.
	if _, err := svc.PrintRepairLabel(ctx, "web:tech", repair.ID); err != nil {
		t.Fatalf("print: %v", err)
	}
	if len(calls) != 1 || calls[0] != (stickerCall{driver: "p15", id: repair.ID, name: "Maria Pop"}) {
		t.Fatalf("sticker call = %+v", calls)
	}

	// An unassigned repair prints the number alone.
	if _, err := svc.PrintRepairLabel(ctx, "web:tech", unassigned.ID); err != nil {
		t.Fatalf("unassigned print: %v", err)
	}
	if len(calls) != 2 || calls[1].name != "" {
		t.Fatalf("unassigned sticker = %+v", calls[1])
	}

	// Successful prints are recorded on the repair.
	detail, err := svc.GetRepair(ctx, repair.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	printed := 0
	for _, a := range detail.Activity {
		if a.Action == "label" {
			printed++
		}
	}
	if printed != 1 {
		t.Fatalf("label activity rows = %d, want 1", printed)
	}

	// A failed print is reported and leaves no success record behind.
	svc.WithLabelPrinter(
		func(context.Context, string, int64, string) error {
			return errs.New(errs.CodeConflict, "printer is out of paper; check the printer before printing again")
		},
		func(context.Context, string) error { return nil })
	if _, err := svc.PrintRepairLabel(ctx, "web:tech", repair.ID); errs.CodeOf(err) != errs.CodeConflict {
		t.Fatalf("failed print: code = %s, want conflict", errs.CodeOf(err))
	}
	detail, err = svc.GetRepair(ctx, repair.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	printed = 0
	for _, a := range detail.Activity {
		if a.Action == "label" {
			printed++
		}
	}
	if printed != 1 {
		t.Fatalf("a failed print must not be recorded as printed: rows = %d", printed)
	}
}

func TestPrintTestLabelIsAdministratorOnly(t *testing.T) {
	ctx := context.Background()
	svc := newService(t)
	var tested string
	svc.WithLabelPrinter(
		func(context.Context, string, int64, string) error { return nil },
		func(_ context.Context, driver string) error { tested = driver; return nil })

	if _, err := svc.PrintTestLabel(ctx, app.RoleAdmin); errs.CodeOf(err) != errs.CodeValidation {
		t.Fatalf("no printer selected: code = %s, want validation", errs.CodeOf(err))
	}
	if _, err := svc.SetLabelPrinter(ctx, app.RoleAdmin, "p15"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.PrintTestLabel(ctx, app.RoleTechnician); errs.CodeOf(err) != errs.CodePermission {
		t.Fatalf("technician test print: code = %s, want permission", errs.CodeOf(err))
	}
	if _, err := svc.PrintTestLabel(ctx, app.RoleAdmin); err != nil {
		t.Fatalf("test print: %v", err)
	}
	if tested != "p15" {
		t.Fatalf("driver = %q, want p15", tested)
	}
}

func TestPrintRepairLabelMissingRepair(t *testing.T) {
	ctx := context.Background()
	svc := newService(t)
	if _, err := svc.SetLabelPrinter(ctx, app.RoleAdmin, "p15"); err != nil {
		t.Fatal(err)
	}
	svc.WithLabelPrinter(
		func(context.Context, string, int64, string) error { return errors.New("must not print") },
		func(context.Context, string) error { return errors.New("must not print") })
	if _, err := svc.PrintRepairLabel(ctx, "web:tech", 4242); errs.CodeOf(err) != errs.CodeNotFound {
		t.Fatalf("code = %s, want not_found", errs.CodeOf(err))
	}
}

// A damaged or partly restored workspace can lose the settings row. That must
// switch printing off, not break every repair page, and saving must recreate it.
func TestLabelPrinterSettingSurvivesMissingRow(t *testing.T) {
	ctx := context.Background()
	svc := newService(t)
	customer, err := svc.CreateCustomer(ctx, "setup", app.CustomerInput{Name: "Maria Pop"})
	if err != nil {
		t.Fatal(err)
	}
	repair, err := svc.CreateRepair(ctx, "setup", app.RepairInput{CustomerID: customer.ID, DeviceType: "Laptop", Issue: "no power"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.DB().ExecContext(ctx, `DELETE FROM label_printer`); err != nil {
		t.Fatal(err)
	}
	settings, err := svc.LabelPrinter(ctx)
	if err != nil || settings.Driver != "" {
		t.Fatalf("missing row: settings = %+v, err = %v", settings, err)
	}
	if _, err := svc.PrintRepairLabel(ctx, "web:tech", repair.ID); errs.CodeOf(err) != errs.CodeValidation {
		t.Fatalf("missing row must read as printing off, got %v", err)
	}
	settings, err = svc.SetLabelPrinter(ctx, app.RoleAdmin, "p15")
	if err != nil || settings.Driver != "p15" {
		t.Fatalf("save after missing row = %+v, %v", settings, err)
	}
	var rows int
	if err := svc.DB().QueryRowContext(ctx, `SELECT count(*) FROM label_printer`).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("label_printer rows = %d, err = %v", rows, err)
	}
}
