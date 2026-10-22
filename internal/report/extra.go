package report

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/xuri/excelize/v2"

	"amazonrecon/internal/trace"
)

// writeTables writes a sequence of titled tables one below another.
func writeTables(f *excelize.File, st styles, sheet, title string, tables []trace.Table) error {
	row := 1
	if err := f.SetCellValue(sheet, "A1", title); err != nil {
		return err
	}
	if err := f.SetCellStyle(sheet, "A1", "A1", st.bold); err != nil {
		return err
	}
	row += 2
	for _, t := range tables {
		if err := f.SetCellValue(sheet, cell("A", row), fmt.Sprintf("%s (%d)", t.Title, len(t.Rows))); err != nil {
			return err
		}
		if err := f.SetCellStyle(sheet, cell("A", row), cell("A", row), st.bold); err != nil {
			return err
		}
		row++
		if t.Note != "" {
			if err := f.SetCellValue(sheet, cell("A", row), t.Note); err != nil {
				return err
			}
			if err := f.SetCellStyle(sheet, cell("A", row), cell("A", row), st.note); err != nil {
				return err
			}
			row++
		}
		header := make([]any, len(t.Columns))
		for i, c := range t.Columns {
			header[i] = c
		}
		if err := f.SetSheetRow(sheet, cell("A", row), &header); err != nil {
			return err
		}
		last, _ := excelize.CoordinatesToCellName(len(t.Columns), row)
		if err := f.SetCellStyle(sheet, cell("A", row), last, st.header); err != nil {
			return err
		}
		row++
		if len(t.Rows) == 0 {
			if err := f.SetCellValue(sheet, cell("A", row), "(none)"); err != nil {
				return err
			}
			row++
		}
		for _, r := range t.Rows {
			vals := make([]any, len(r))
			for i, v := range r {
				vals[i] = v
				if i < len(t.Numeric) && t.Numeric[i] {
					if n, err := strconv.ParseFloat(v, 64); err == nil {
						vals[i] = n
					}
				}
			}
			if err := f.SetSheetRow(sheet, cell("A", row), &vals); err != nil {
				return err
			}
			for i := range r {
				if i < len(t.Numeric) && t.Numeric[i] && strings.Contains(r[i], ".") {
					c, _ := excelize.CoordinatesToCellName(i+1, row)
					if err := f.SetCellStyle(sheet, c, c, st.money); err != nil {
						return err
					}
				}
			}
			row++
		}
		row++
	}
	return f.SetColWidth(sheet, "A", "I", 24)
}

// writeRunInfo records what produced the workbook: files, config fingerprints, counts, checks,
// and the summary of every run kept in the database (before/after deltas).
func writeRunInfo(ctx context.Context, pool *pgxpool.Pool, f *excelize.File, st styles, run RunInfo) error {
	var tables []trace.Table

	tables = append(tables, trace.Table{
		Title:   "Run",
		Columns: []string{"key", "value"},
		Rows: [][]string{
			{"run id", strconv.FormatInt(run.ID, 10)},
			{"label", run.Label},
			{"started at (UTC)", run.StartedAt.UTC().Format("2006-01-02 15:04:05")},
			{"settlement ids in scope", strings.Join(run.Scope, ", ")},
			{"payment rules sha256", run.PaymentRulesSHA},
			{"settlement rules sha256", run.SettlementRulesSHA},
			{"reconciled records", strconv.Itoa(run.Reconciled)},
			{"unreconciled payment records", strconv.Itoa(run.UnreconciledPayment)},
			{"unreconciled settlement records", strconv.Itoa(run.UnreconciledSettlemnt)},
			{"out-of-scope payment records", strconv.Itoa(run.OutOfScopePayment)},
		},
	})

	q := func(title string, numeric []bool, cols []string, sql string, args ...any) error {
		rows, err := pool.Query(ctx, sql, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		t := trace.Table{Title: title, Columns: cols, Numeric: numeric}
		for rows.Next() {
			vals := make([]string, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				return err
			}
			t.Rows = append(t.Rows, vals)
		}
		tables = append(tables, t)
		return rows.Err()
	}
	if err := q("Input files", []bool{false, false, false, true, true, true, true},
		[]string{"source", "file", "sha256", "bytes", "rows ingested", "skipped lines", "error rows"},
		`SELECT source, file_name, sha256, size_bytes::text, COALESCE(data_rows, 0)::text, COALESCE(skipped_lines, 0)::text,
		        COALESCE(error_rows, 0)::text FROM ingest_files ORDER BY source`); err != nil {
		return err
	}
	if err := q("Mapping rules in effect", []bool{false, true, true},
		[]string{"table", "rules", "rules added by fixes (no config line)"},
		`SELECT 'payment_mapping_rules', count(*)::text, count(*) FILTER (WHERE src_line IS NULL)::text FROM payment_mapping_rules
		 UNION ALL SELECT 'settlement_mapping_rules', count(*)::text, count(*) FILTER (WHERE src_line IS NULL)::text FROM settlement_mapping_rules`); err != nil {
		return err
	}
	if err := q("Self-checks", nil, []string{"check", "status", "detail"},
		`SELECT name, status, detail FROM run_checks WHERE run_id = $1 ORDER BY name`, run.ID); err != nil {
		return err
	}
	if err := q("Run history", []bool{true, false, false, true, true, true, true},
		[]string{"run id", "label", "started at (UTC)", "reconciled", "unreconciled payment", "unreconciled settlement", "out of scope payment"},
		`SELECT id::text, label, to_char(started_at AT TIME ZONE 'UTC', 'YYYY-MM-DD HH24:MI:SS'),
		        COALESCE(reconciled, 0)::text, COALESCE(unreconciled_payment, 0)::text,
		        COALESCE(unreconciled_settlement, 0)::text, COALESCE(out_of_scope_payment, 0)::text
		 FROM runs WHERE reconciled IS NOT NULL ORDER BY id`); err != nil {
		return err
	}
	if err := q("Summary history (in-scope totals per run, Payments - Settlements)", []bool{true, false, false, true, true, true},
		[]string{"run id", "label", "summary field", "payments", "settlements", "difference"},
		`SELECT h.run_id::text, r.label, h.summary_field,
		        COALESCE(sum(h.amount) FILTER (WHERE h.source = 'payments'), 0)::text,
		        COALESCE(sum(h.amount) FILTER (WHERE h.source = 'settlements'), 0)::text,
		        (COALESCE(sum(h.amount) FILTER (WHERE h.source = 'payments'), 0)
		         - COALESCE(sum(h.amount) FILTER (WHERE h.source = 'settlements'), 0))::text
		 FROM run_history_summary h JOIN runs r ON r.id = h.run_id
		 LEFT JOIN summary_lines l ON l.summary_field = h.summary_field
		 GROUP BY h.run_id, r.label, h.summary_field, l.sort_order
		 ORDER BY h.run_id, l.sort_order NULLS LAST, h.summary_field`); err != nil {
		return err
	}
	return writeTables(f, st, SheetRunInfo, "Run information", tables)
}
