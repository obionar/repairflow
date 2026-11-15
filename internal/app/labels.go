package app

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"repairflow/internal/errs"
	"repairflow/internal/printer"
)

// PrinterSettings is the workspace label-printer choice. An empty driver means
// label printing is switched off. Printer-specific settings (density, label
// size, printer name) belong to the printer driver's own configuration, so
// there is one authority per setting.
type PrinterSettings struct {
	Driver    string `json:"driver"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

// LabelPrinterDrivers lists the label printers an operator can select.
var LabelPrinterDrivers = printer.Drivers

// LabelPrinter reads the workspace label-printer setting. A missing row means
// printing is off: a damaged or partially restored workspace must still open
// its pages instead of failing every repair view.
func (s *Service) LabelPrinter(ctx context.Context) (PrinterSettings, error) {
	var settings PrinterSettings
	err := s.q(ctx).QueryRowContext(ctx,
		`SELECT driver, updated_at FROM label_printer WHERE id = 1`).Scan(&settings.Driver, &settings.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return PrinterSettings{}, nil
	}
	if err != nil {
		return PrinterSettings{}, errs.Wrap(errs.CodeInternal, err, "cannot read the label printer setting")
	}
	return settings, nil
}

// SetLabelPrinter stores the workspace label printer. It is a workspace-wide
// setting, so only an administrator may change it.
func (s *Service) SetLabelPrinter(ctx context.Context, callerRole, driver string) (PrinterSettings, error) {
	if callerRole != RoleAdmin {
		return PrinterSettings{}, errs.New(errs.CodePermission, "only an administrator can change the label printer")
	}
	driver = strings.TrimSpace(driver)
	if driver != "" && !printer.Known(driver) {
		return PrinterSettings{}, errs.New(errs.CodeValidation, "unknown label printer %q", driver)
	}
	tx, commit, rollback, err := s.beginWrite(ctx)
	if err != nil {
		return PrinterSettings{}, err
	}
	defer rollback()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO label_printer(id, driver, updated_at) VALUES(1, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET driver = excluded.driver, updated_at = excluded.updated_at`,
		driver, s.timestamp()); err != nil {
		return PrinterSettings{}, errs.Wrap(errs.CodeInternal, err, "cannot save the label printer setting")
	}
	if err := commit(); err != nil {
		return PrinterSettings{}, errs.Wrap(errs.CodeInternal, err, "cannot commit the label printer setting")
	}
	return s.LabelPrinter(ctx)
}

// LabelPrint reports one printed sticker.
type LabelPrint struct {
	RepairID     int64  `json:"repair_id"`
	CustomerName string `json:"customer_name,omitempty"`
	Printer      string `json:"printer"`
}

// PrintRepairLabel prints the intake sticker for one repair: the repair number
// and the customer name, so a device on the shelf can be matched to its page.
// The print is recorded on the repair. A failure is reported and never
// retried, because the label may already have reached paper.
func (s *Service) PrintRepairLabel(ctx context.Context, actor string, id int64) (LabelPrint, error) {
	actor, err := normalizeActor(actor)
	if err != nil {
		return LabelPrint{}, err
	}
	repair, err := s.GetRepairRow(ctx, id)
	if err != nil {
		return LabelPrint{}, err
	}
	settings, err := s.LabelPrinter(ctx)
	if err != nil {
		return LabelPrint{}, err
	}
	if settings.Driver == "" {
		return LabelPrint{}, errs.New(errs.CodeValidation, "no label printer is selected; choose one in Settings")
	}
	if err := s.printSticker(ctx, settings.Driver, repair.ID, repair.CustomerName); err != nil {
		return LabelPrint{}, err
	}
	if err := s.InTx(ctx, func(ctx context.Context) error {
		if _, err := s.q(ctx).ExecContext(ctx,
			`INSERT INTO repair_activity(repair_id, actor, action, detail, created_at) VALUES(?,?,?,?,?)`,
			repair.ID, actor, "label", detailJSON(map[string]string{"printer": settings.Driver}),
			s.timestamp()); err != nil {
			// The label is already on paper: say so, or the operator may print
			// a second sticker to compensate for a "failed" request.
			return errs.Wrap(errs.CodeInternal, err, "the label was printed but its audit record could not be saved")
		}
		return nil
	}); err != nil {
		return LabelPrint{}, err
	}
	return LabelPrint{RepairID: repair.ID, CustomerName: repair.CustomerName, Printer: settings.Driver}, nil
}

// TestLabelPrint reports one printed test label.
type TestLabelPrint struct {
	Printer string `json:"printer"`
}

// PrintTestLabel prints one label so an administrator can confirm the printer
// setup without touching a repair record.
func (s *Service) PrintTestLabel(ctx context.Context, callerRole string) (TestLabelPrint, error) {
	if callerRole != RoleAdmin {
		return TestLabelPrint{}, errs.New(errs.CodePermission, "only an administrator can print a test label")
	}
	settings, err := s.LabelPrinter(ctx)
	if err != nil {
		return TestLabelPrint{}, err
	}
	if settings.Driver == "" {
		return TestLabelPrint{}, errs.New(errs.CodeValidation, "no label printer is selected; choose one first")
	}
	if err := s.printTest(ctx, settings.Driver); err != nil {
		return TestLabelPrint{}, err
	}
	return TestLabelPrint{Printer: settings.Driver}, nil
}
