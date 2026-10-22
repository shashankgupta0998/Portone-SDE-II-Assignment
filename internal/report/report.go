package report

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/xuri/excelize/v2"

	"amazonrecon/internal/trace"
)

// Sheet names.
const (
	SheetSummary      = "Summary"
	SheetConsolidated = "Consolidated Data"
	SheetOutOfScope   = "Out of Scope Payments"
	SheetExceptions   = "Exceptions"
	SheetRunInfo      = "Run Info"
)

const moneyFormat = `#,##0.00;[Red]-#,##0.00`

type styles struct {
	bold, header, money, moneyBold, indent2Bold, indent4, note int
}

func newStyles(f *excelize.File) (styles, error) {
	var s styles
	var err error
	mk := func(st *excelize.Style) int {
		if err != nil {
			return 0
		}
		var id int
		id, err = f.NewStyle(st)
		return id
	}
	numFmt := moneyFormat
	s.bold = mk(&excelize.Style{Font: &excelize.Font{Bold: true}})
	s.header = mk(&excelize.Style{Font: &excelize.Font{Bold: true}, Fill: excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{"DDEBF7"}},
		Alignment: &excelize.Alignment{WrapText: true, Vertical: "top"}})
	s.money = mk(&excelize.Style{CustomNumFmt: &numFmt})
	s.moneyBold = mk(&excelize.Style{CustomNumFmt: &numFmt, Font: &excelize.Font{Bold: true}})
	s.indent2Bold = mk(&excelize.Style{Font: &excelize.Font{Bold: true}, Alignment: &excelize.Alignment{Indent: 2}})
	s.indent4 = mk(&excelize.Style{Alignment: &excelize.Alignment{Indent: 4}})
	s.note = mk(&excelize.Style{Font: &excelize.Font{Italic: true, Color: "595959"}})
	return s, err
}

// Write builds the workbook for a run: Summary, Consolidated Data, Out of Scope Payments,
// Exceptions and Run Info.
func Write(ctx context.Context, pool *pgxpool.Pool, runID int64, path string) error {
	run, err := loadRun(ctx, pool, runID)
	if err != nil {
		return err
	}
	sum, err := loadSummary(ctx, pool, run)
	if err != nil {
		return err
	}
	diag, err := trace.Diagnose(ctx, pool)
	if err != nil {
		return err
	}

	f := excelize.NewFile()
	defer f.Close()
	st, err := newStyles(f)
	if err != nil {
		return err
	}
	if err := f.SetSheetName("Sheet1", SheetSummary); err != nil {
		return err
	}
	if err := writeSummary(f, st, run, sum); err != nil {
		return fmt.Errorf("summary sheet: %w", err)
	}
	if _, err := f.NewSheet(SheetConsolidated); err != nil {
		return err
	}
	if err := writeConsolidated(ctx, pool, f, st, SheetConsolidated, run, sum, false); err != nil {
		return fmt.Errorf("consolidated sheet: %w", err)
	}
	if _, err := f.NewSheet(SheetOutOfScope); err != nil {
		return err
	}
	if err := writeConsolidated(ctx, pool, f, st, SheetOutOfScope, run, sum, true); err != nil {
		return fmt.Errorf("out-of-scope sheet: %w", err)
	}
	if _, err := f.NewSheet(SheetExceptions); err != nil {
		return err
	}
	if err := writeTables(f, st, SheetExceptions, "Exceptions and rule diagnostics (latest run, all settlement ids)", diag); err != nil {
		return fmt.Errorf("exceptions sheet: %w", err)
	}
	if _, err := f.NewSheet(SheetRunInfo); err != nil {
		return err
	}
	if err := writeRunInfo(ctx, pool, f, st, run); err != nil {
		return fmt.Errorf("run info sheet: %w", err)
	}
	f.SetActiveSheet(0)

	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return f.SaveAs(path)
}
