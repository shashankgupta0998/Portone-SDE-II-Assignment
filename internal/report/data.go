// Package report writes the Excel workbook. It only reads the database: every value is a sum
// of stored rows; there are no adjustments or constants in this package.
package report

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
)

// SummaryLine is one Summary sheet line (from the summary_lines table).
type SummaryLine struct {
	Field             string
	Section           string
	Label             string
	ConsolidatedLabel string
}

// RunInfo describes the run being reported.
type RunInfo struct {
	ID                    int64
	Label                 string
	StartedAt             time.Time
	Scope                 []string
	PaymentRulesSHA       string
	SettlementRulesSHA    string
	Reconciled            int
	UnreconciledPayment   int
	UnreconciledSettlemnt int
	OutOfScopePayment     int
}

type sourceTotals struct {
	payments, settlements decimal.Decimal
}

// summaryData is everything the Summary sheet shows.
type summaryData struct {
	lines        []SummaryLine
	routed       map[string]map[string]decimal.Decimal // source -> field -> amount (streamed, in scope, Released)
	deferredBy   map[string]decimal.Decimal            // field -> in-scope Deferred payment amount (streamed)
	net          sourceTotals                          // payments: sum(total) of Released rows; settlements: sum(amount)
	headerTotal  decimal.Decimal                       // Amazon's settlement total-amount
	deferred     decimal.Decimal                       // in-scope payments with status Deferred, sum(total)
	outOfScope   decimal.Decimal                       // payments of other settlement ids, sum(total)
	outOfScopeN  int
	fileNames    map[string]string // source -> file name
	headerByID   map[string]map[string]string
	consolidated []SummaryLine // the 21 sheet fields, in order
}

func dec(s string) decimal.Decimal {
	d, err := decimal.NewFromString(s)
	if err != nil {
		return decimal.Zero
	}
	return d
}

func loadRun(ctx context.Context, pool *pgxpool.Pool, runID int64) (RunInfo, error) {
	var r RunInfo
	err := pool.QueryRow(ctx, `SELECT id, label, started_at, COALESCE(scope_settlement_ids, '{}'),
		COALESCE(payment_rules_sha256, ''), COALESCE(settlement_rules_sha256, ''),
		COALESCE(reconciled, 0), COALESCE(unreconciled_payment, 0), COALESCE(unreconciled_settlement, 0),
		COALESCE(out_of_scope_payment, 0) FROM runs WHERE id = $1`, runID).
		Scan(&r.ID, &r.Label, &r.StartedAt, &r.Scope, &r.PaymentRulesSHA, &r.SettlementRulesSHA,
			&r.Reconciled, &r.UnreconciledPayment, &r.UnreconciledSettlemnt, &r.OutOfScopePayment)
	if err == pgx.ErrNoRows {
		return r, fmt.Errorf("run %d not found", runID)
	}
	if err == nil && len(r.Scope) == 0 {
		err = fmt.Errorf("run %d has not been reconciled yet (run `recon reconcile`)", runID)
	}
	return r, err
}

func loadSummary(ctx context.Context, pool *pgxpool.Pool, run RunInfo) (*summaryData, error) {
	d := &summaryData{
		routed:     map[string]map[string]decimal.Decimal{"payments": {}, "settlements": {}},
		deferredBy: map[string]decimal.Decimal{},
		fileNames:  map[string]string{},
		headerByID: map[string]map[string]string{},
	}
	rows, err := pool.Query(ctx, `SELECT summary_field, section, label, consolidated_label FROM summary_lines ORDER BY sort_order`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var l SummaryLine
		if err := rows.Scan(&l.Field, &l.Section, &l.Label, &l.ConsolidatedLabel); err != nil {
			return nil, err
		}
		d.lines = append(d.lines, l)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	d.consolidated = d.lines

	// Summary values come from the totals streamed during ingestion (cross-checked against SQL
	// by the stream_summary_equals_sql self-check). Payment rows with Transaction status
	// "Deferred" carry the settlement id but have not been released into that settlement (no
	// release date, no settlement line); they are kept apart from the Payments column and shown
	// next to it.
	rows, err = pool.Query(ctx, `SELECT source, txn_status = 'Deferred', summary_field, sum(amount)::text FROM stream_summary
		WHERE run_id = $1 AND settlement_id = ANY($2::text[]) GROUP BY 1, 2, 3`, run.ID, run.Scope)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var src, field, amt string
		var deferred bool
		if err := rows.Scan(&src, &deferred, &field, &amt); err != nil {
			return nil, err
		}
		if deferred {
			d.deferredBy[field] = dec(amt)
			continue
		}
		d.routed[src][field] = dec(amt)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var pay, set, hdr, deferred, oos string
	err = pool.QueryRow(ctx, `SELECT
		COALESCE(sum(total)  FILTER (WHERE source = 'payments'    AND row_kind = 'data' AND settlement_id = ANY($1::text[]) AND txn_status IS DISTINCT FROM 'Deferred'), 0)::text,
		COALESCE(sum(amount) FILTER (WHERE source = 'settlements' AND row_kind = 'data' AND settlement_id = ANY($1::text[])), 0)::text,
		COALESCE(sum(total_amount) FILTER (WHERE source = 'settlements' AND row_kind = 'settlement_header' AND settlement_id = ANY($1::text[])), 0)::text,
		COALESCE(sum(total)  FILTER (WHERE source = 'payments' AND row_kind = 'data' AND settlement_id = ANY($1::text[]) AND txn_status = 'Deferred'), 0)::text,
		COALESCE(sum(total)  FILTER (WHERE source = 'payments' AND row_kind = 'data' AND NOT (settlement_id = ANY($1::text[]))), 0)::text,
		count(*) FILTER (WHERE source = 'payments' AND row_kind = 'data' AND NOT (settlement_id = ANY($1::text[])))
		FROM source_rows`, run.Scope).Scan(&pay, &set, &hdr, &deferred, &oos, &d.outOfScopeN)
	if err != nil {
		return nil, err
	}
	d.net = sourceTotals{payments: dec(pay), settlements: dec(set)}
	d.headerTotal, d.deferred, d.outOfScope = dec(hdr), dec(deferred), dec(oos)

	rows, err = pool.Query(ctx, `SELECT source, file_name FROM ingest_files`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var src, name string
		if err := rows.Scan(&src, &name); err != nil {
			return nil, err
		}
		d.fileNames[src] = name
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = pool.Query(ctx, `SELECT settlement_id, raw FROM source_rows WHERE source = 'settlements' AND row_kind = 'settlement_header'`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		var raw map[string]string
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		d.headerByID[id] = raw
	}
	return d, rows.Err()
}
