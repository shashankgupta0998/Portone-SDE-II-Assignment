package pipeline_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/xuri/excelize/v2"

	"amazonrecon/internal/db"
	"amazonrecon/internal/pipeline"
	"amazonrecon/internal/report"
	"amazonrecon/internal/rules"
)

// Integration tests run against a real PostgreSQL (TEST_DATABASE_URL) in a throwaway schema.
func setup(t *testing.T) (context.Context, *pgxpool.Pool) {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	schema := fmt.Sprintf("recon_test_%d", time.Now().UnixNano())
	pool, err := db.Connect(ctx, url, schema)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
		pool.Close()
	})
	if _, err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if again, err := db.Migrate(ctx, pool); err != nil || len(again) != 0 {
		t.Fatalf("second migrate should be a no-op: %v %v", again, err)
	}
	if _, _, err := rules.LoadCSVs(ctx, pool, "../../testdata/payment_rules.csv", "../../testdata/settlement_rules.csv"); err != nil {
		t.Fatal(err)
	}
	return ctx, pool
}

func scalar[T any](t *testing.T, ctx context.Context, pool *pgxpool.Pool, sql string, args ...any) T {
	t.Helper()
	var v T
	if err := pool.QueryRow(ctx, sql, args...).Scan(&v); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return v
}

func run(t *testing.T, ctx context.Context, pool *pgxpool.Pool, label string) pipeline.Result {
	t.Helper()
	res, err := pipeline.Ingest(ctx, pool, pipeline.Options{
		PaymentsPath: "../../testdata/payments.csv", SettlementsPath: "../../testdata/settlements.txt", Label: label,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := pipeline.Reconcile(ctx, pool, &res, nil); err != nil {
		t.Fatal(err)
	}
	return res
}

func TestEndToEnd(t *testing.T) {
	ctx, pool := setup(t)
	res := run(t, ctx, pool, "first")

	if res.Failed() {
		t.Fatalf("self-checks failed: %+v", res.Checks)
	}
	if len(res.Scope) != 1 || res.Scope[0] != "S1" {
		t.Fatalf("scope = %v", res.Scope)
	}
	// 111-1, 111-2 (two payment rows -> one settlement row), 222-1 reconcile;
	// deferred 111-3 and the transfer are payments only; the subscription fee is settlement only;
	// order 000-1 belongs to settlement S0.
	want := [4]int{3, 2, 1, 1}
	got := [4]int{res.Counts.Reconciled, res.Counts.UnreconciledPayment, res.Counts.UnreconciledSettlement, res.Counts.OutOfScopePayment}
	if got != want {
		t.Fatalf("counts = %v, want %v", got, want)
	}

	// single ingestion table holds both files, with lineage
	if n := scalar[int](t, ctx, pool, `SELECT count(*) FROM source_rows`); n != 17 {
		t.Errorf("source_rows = %d, want 17 (7 payments + 10 settlement lines)", n)
	}
	raw := scalar[string](t, ctx, pool, `SELECT raw_line FROM source_rows WHERE source = 'payments' AND line_no = 4`)
	lines, _ := os.ReadFile("../../testdata/payments.csv")
	if want := splitLines(string(lines))[3]; raw != want {
		t.Errorf("raw_line = %q, want %q", raw, want)
	}

	// N:1 aggregation: two payment rows of 10.00 vs one settlement row of 20.00
	var payRows, setRows int
	var diff string
	if err := pool.QueryRow(ctx, `SELECT pay_rows, set_rows, diff_total::text FROM recon_records WHERE record_ref = '111-2+SKU-B+2026-07-17'`).
		Scan(&payRows, &setRows, &diff); err != nil {
		t.Fatal(err)
	}
	if payRows != 2 || setRows != 1 || diff != "0.00" {
		t.Errorf("111-2: pay_rows %d set_rows %d diff %s", payRows, setRows, diff)
	}
	// 1:N: one payment row vs five settlement rows, buckets agree
	if n := scalar[int](t, ctx, pool, `SELECT set_rows FROM recon_records WHERE record_ref = '111-1+SKU-A+2026-07-17'`); n != 5 {
		t.Errorf("111-1 set_rows = %d", n)
	}
	if n := scalar[int](t, ctx, pool, `SELECT count(*) FROM (
		SELECT summary_field, sum(amount) FILTER (WHERE side = 'payments') p, sum(amount) FILTER (WHERE side = 'settlements') s
		FROM recon_record_buckets WHERE record_ref = '111-1+SKU-A+2026-07-17' GROUP BY 1) x WHERE p IS DISTINCT FROM s`); n != 0 {
		t.Errorf("111-1 has %d bucket differences", n)
	}
	// the transfer falls through to the fallback rule (empty transaction_type + any)
	if f := scalar[string](t, ctx, pool, `SELECT summary_field FROM mapped_components c JOIN source_rows r ON r.id = c.source_row_id
		WHERE r.transaction_type = 'Transfer' AND c.amount_field = 'total'`); f != "expenses_amazon_fees" {
		t.Errorf("transfer routed to %q", f)
	}

	// streamed summary for the scope
	if v := scalar[string](t, ctx, pool, `SELECT sum(amount)::text FROM stream_summary WHERE source = 'settlements'
		AND settlement_id = 'S1'`); v != "-1.97" {
		t.Errorf("settlement routed total = %s, want -1.97 (= header total-amount)", v)
	}

	// report
	out := filepath.Join(t.TempDir(), "r.xlsx")
	if err := report.Write(ctx, pool, res.RunID, out); err != nil {
		t.Fatal(err)
	}
	f, err := excelize.OpenFile(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sheets := f.GetSheetList()
	if len(sheets) < 2 || sheets[0] != report.SheetSummary || sheets[1] != report.SheetConsolidated {
		t.Fatalf("sheets = %v", sheets)
	}
	for c, want := range map[string]string{"C1": "Payments", "D1": "Settlements", "E1": "Payments - Settlements",
		"B4": "Sales", "B5": "Product Charges", "B15": "Refunds", "B19": "Expenses", "B28": "Micro Deposit", "B32": "Paid To Amazon"} {
		if v, _ := f.GetCellValue(report.SheetSummary, c); v != want {
			t.Errorf("Summary %s = %q, want %q", c, v, want)
		}
	}
	// Payments Product Charges: 20 + 10 + 10 (Released) = 40, the deferred 15 is shown in F;
	// Settlements: 20 + 20 = 40
	for c, want := range map[string]string{"C5": "40.00", "D5": "40.00", "E5": "0.00", "F5": "15.00", "D17": "-20.00",
		"F1": "Memo: Payments Deferred (not in C)"} {
		if v, _ := f.GetCellValue(report.SheetSummary, c); v != want {
			t.Errorf("Summary %s = %q, want %q", c, v, want)
		}
	}
	rows, err := f.GetRows(report.SheetConsolidated)
	if err != nil {
		t.Fatal(err)
	}
	if rows[9][0] != "Date" || rows[9][82] != "Reconciliation Status" || rows[9][83] != "total (DS1-DS2)" {
		t.Errorf("header row 10 = %v", rows[9][:5])
	}
	var statuses []string
	for _, r := range rows[10:] {
		statuses = append(statuses, r[82])
	}
	wantOrder := []string{"Reconciled", "Reconciled", "Reconciled", "Unreconciled Payment", "Unreconciled Payment", "Unreconciled Settlement"}
	if fmt.Sprint(statuses) != fmt.Sprint(wantOrder) {
		t.Errorf("consolidated order = %v", statuses)
	}
}

func TestReingestIsIdempotent(t *testing.T) {
	ctx, pool := setup(t)
	first := run(t, ctx, pool, "first")
	second := run(t, ctx, pool, "second")
	if first.Counts != second.Counts {
		t.Errorf("counts changed: %+v vs %+v", first.Counts, second.Counts)
	}
	if n := scalar[int](t, ctx, pool, `SELECT count(*) FROM source_rows`); n != 17 {
		t.Errorf("source_rows after re-ingest = %d, want 17", n)
	}
	if n := scalar[int](t, ctx, pool, `SELECT count(*) FROM ingest_files`); n != 2 {
		t.Errorf("ingest_files = %d", n)
	}
	if n := scalar[int](t, ctx, pool, `SELECT count(DISTINCT run_id) FROM mapped_components`); n != 1 {
		t.Errorf("mapped_components should only hold the latest run, has %d", n)
	}
	if n := scalar[int](t, ctx, pool, `SELECT count(DISTINCT run_id) FROM run_history_summary`); n != 2 {
		t.Errorf("run history should keep both runs, has %d", n)
	}
}

func TestConfigFixIsADataChange(t *testing.T) {
	ctx, pool := setup(t)
	before := run(t, ctx, pool, "before")
	// A fix applied as SQL to the rule table changes the next run without any code change.
	if err := db.ExecFile(ctx, pool, `UPDATE payment_mapping_rules
		SET to_summary_field_when_negative_amount = 'expenses_other'
		WHERE transaction_type = '' AND description = 'any' AND amount_field = 'total';`); err != nil {
		t.Fatal(err)
	}
	after := run(t, ctx, pool, "after")
	if before.RunID == after.RunID {
		t.Fatal("expected a new run")
	}
	if v := scalar[string](t, ctx, pool, `SELECT amount::text FROM run_history_summary WHERE run_id = $1
		AND source = 'payments' AND summary_field = 'expenses_other'`, after.RunID); v != "-1050.00" {
		t.Errorf("expenses_other after fix = %s", v)
	}
}

func checkStatus(t *testing.T, res pipeline.Result, name string) string {
	t.Helper()
	for _, c := range res.Checks {
		if c.Name == name {
			return c.Status
		}
	}
	t.Fatalf("check %s not run", name)
	return ""
}

// The defect classes found in the real configs must be caught by the self-checks, not only by
// comparing Summary columns.
func TestSelfChecksCatchConfigDefects(t *testing.T) {
	ctx, pool := setup(t)

	// Catch-all: the transfer's description matches no TRANSFER rule, so the fallback summarises it.
	base := run(t, ctx, pool, "base")
	if st := checkStatus(t, base, "fallback_rule_routed"); st != "info" {
		t.Errorf("fallback_rule_routed = %s, want info", st)
	}
	if st := checkStatus(t, base, "amount_routed_once"); st != "pass" {
		t.Errorf("amount_routed_once = %s, want pass", st)
	}

	// Duplicate rule on one column (MAPPING_FIXES D3/D4): counted twice, and reconciled records
	// disagree per field although their totals still agree.
	if err := db.ExecFile(ctx, pool, `INSERT INTO payment_mapping_rules (transaction_type, description, amount_field, record_ref,
		to_summary_field_when_positive_amount, to_summary_field_when_negative_amount)
		VALUES ('ORDER', 'any', 'product_sales', 'txn_ref+sku+date', 'sales_shipping', 'sales_shipping');`); err != nil {
		t.Fatal(err)
	}
	dup := run(t, ctx, pool, "duplicate")
	if st := checkStatus(t, dup, "amount_routed_once"); st != "fail" {
		t.Errorf("amount_routed_once = %s, want fail", st)
	}
	if st := checkStatus(t, dup, "reconciled_fields_agree"); st != "fail" {
		t.Errorf("reconciled_fields_agree = %s, want fail", st)
	}
	if v := scalar[string](t, ctx, pool, `SELECT diff_total::text FROM recon_records WHERE record_ref = '111-1+SKU-A+2026-07-17'`); v != "0.00" {
		t.Errorf("record total diff = %s, want 0.00 (the defect only shows per field)", v)
	}

	// Data fixes as in MAPPING_FIXES D1 and D3: the transfer gets its own rule and key.
	if err := db.ExecFile(ctx, pool, `DELETE FROM payment_mapping_rules WHERE src_line IS NULL;
		INSERT INTO payment_mapping_rules (transaction_type, description, amount_field, record_ref)
		VALUES ('TRANSFER', 'any', 'total', 'TRANSFER+description+settlement_id+date');`); err != nil {
		t.Fatal(err)
	}
	fixed := run(t, ctx, pool, "fixed")
	for _, name := range []string{"fallback_rule_routed", "amount_routed_once", "reconciled_fields_agree"} {
		if st := checkStatus(t, fixed, name); st != "pass" {
			t.Errorf("%s = %s, want pass", name, st)
		}
	}
	if n := scalar[int](t, ctx, pool, `SELECT count(*) FROM recon_records
		WHERE record_ref = 'TRANSFER+TO_ACCOUNT_ENDING_WITH:_334+S1+2026-07-18' AND status = 'unreconciled_payment'`); n != 1 {
		t.Errorf("transfer record_ref not built from its TRANSFER rule")
	}
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return out
}
