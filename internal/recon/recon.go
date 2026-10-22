// Package recon matches payments and settlements on record_ref. It is set-based SQL over
// mapped_components: aggregate per (side, record_ref) first, then FULL OUTER JOIN.
package recon

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Counts are distinct record_refs per status.
type Counts struct {
	Reconciled             int
	UnreconciledPayment    int
	UnreconciledSettlement int
	OutOfScopePayment      int
}

// Check is one self-check recorded for a run.
type Check struct {
	Name   string
	Status string // pass | fail | info
	Detail string
}

// DefaultScope returns the settlement ids present in the settlement file(s): the settlement
// period being reconciled.
func DefaultScope(ctx context.Context, pool *pgxpool.Pool) ([]string, error) {
	rows, err := pool.Query(ctx, `SELECT DISTINCT settlement_id FROM source_rows
		WHERE source = 'settlements' AND settlement_id IS NOT NULL ORDER BY 1`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// Statements run in order inside one transaction. $1 = run id, $2 = scope (text[]).
var steps = []struct {
	name string
	sql  string
}{
	{"clear", `DELETE FROM recon_record_buckets; DELETE FROM recon_records;`},
	{"facts", `
CREATE TEMP TABLE _facts ON COMMIT DROP AS
SELECT c.source_row_id, c.record_ref, c.status, c.summary_field, c.amount, c.rule_id,
       CASE WHEN r.source = 'settlements'         THEN 'settlements'
            WHEN r.settlement_id = ANY($2::text[]) THEN 'payments'
            ELSE 'out_of_scope_payments' END AS side
FROM mapped_components c
JOIN source_rows r ON r.id = c.source_row_id
WHERE c.run_id = $1
  AND c.record_ref IS NOT NULL
  AND (r.source = 'payments' OR r.settlement_id = ANY($2::text[]))`},
	// Aggregate before matching: one row per (side, record_ref). A record's Total is the sum of
	// the source amounts of the distinct rows behind it (payments: the row's "total" column;
	// settlements: the row's amount), so a row counted by two rules is not counted twice.
	{"aggregate", `
CREATE TEMP TABLE _agg ON COMMIT DROP AS
WITH rr AS (SELECT DISTINCT side, record_ref, source_row_id FROM _facts)
SELECT rr.side, rr.record_ref,
       count(*)                                    AS n_rows,
       min(rr.source_row_id)                       AS first_row,
       sum(COALESCE(r.total, r.amount, 0))         AS total,
       array_agg(r.line_no ORDER BY r.line_no)     AS lines,
       string_agg(DISTINCT COALESCE(r.amount_type, '') || '/' || COALESCE(r.description, ''), ' | ') AS amount_types
FROM rr JOIN source_rows r ON r.id = rr.source_row_id
GROUP BY rr.side, rr.record_ref;
CREATE TEMP TABLE _rules ON COMMIT DROP AS
SELECT side, record_ref, array_agg(DISTINCT rule_id ORDER BY rule_id) AS rule_ids
FROM _facts WHERE rule_id IS NOT NULL GROUP BY side, record_ref;`},
	{"match", `
INSERT INTO recon_records (run_id, record_ref, status, pay_rows, set_rows, pay_total, set_total, diff_total,
                           pay_first_row_id, set_first_row_id, pay_lines, set_lines, pay_rule_ids, set_rule_ids, set_amount_types)
SELECT $1, COALESCE(p.record_ref, s.record_ref),
       CASE WHEN p.record_ref IS NOT NULL AND s.record_ref IS NOT NULL THEN 'reconciled'
            WHEN p.record_ref IS NOT NULL THEN 'unreconciled_payment'
            ELSE 'unreconciled_settlement' END,
       COALESCE(p.n_rows, 0), COALESCE(s.n_rows, 0), p.total, s.total,
       COALESCE(p.total, 0) - COALESCE(s.total, 0),
       p.first_row, s.first_row, p.lines, s.lines, pr.rule_ids, sr.rule_ids, s.amount_types
FROM (SELECT * FROM _agg WHERE side = 'payments') p
FULL OUTER JOIN (SELECT * FROM _agg WHERE side = 'settlements') s ON s.record_ref = p.record_ref
LEFT JOIN _rules pr ON pr.side = 'payments'    AND pr.record_ref = p.record_ref
LEFT JOIN _rules sr ON sr.side = 'settlements' AND sr.record_ref = s.record_ref;
-- payment rows of other settlements: kept visible, not counted as unreconciled
INSERT INTO recon_records (run_id, record_ref, status, pay_rows, pay_total, diff_total, pay_first_row_id, pay_lines, pay_rule_ids)
SELECT $1, o.record_ref, 'out_of_scope_payment', o.n_rows, o.total, o.total, o.first_row, o.lines, r.rule_ids
FROM _agg o LEFT JOIN _rules r ON r.side = o.side AND r.record_ref = o.record_ref
WHERE o.side = 'out_of_scope_payments';`},
	{"buckets", `
INSERT INTO recon_record_buckets (run_id, record_ref, side, summary_field, amount)
SELECT $1, record_ref, side, summary_field, sum(amount)
FROM _facts WHERE status = 'routed'
GROUP BY record_ref, side, summary_field;`},
}

// Reconcile rebuilds recon_records / recon_record_buckets for a run and returns the counts.
func Reconcile(ctx context.Context, pool *pgxpool.Pool, runID int64, scope []string) (Counts, error) {
	var c Counts
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		for _, s := range steps {
			if err := execStep(ctx, tx, s.sql, runID, scope); err != nil {
				return fmt.Errorf("recon step %s: %w", s.name, err)
			}
		}
		return tx.QueryRow(ctx, `SELECT
			count(*) FILTER (WHERE status = 'reconciled'),
			count(*) FILTER (WHERE status = 'unreconciled_payment'),
			count(*) FILTER (WHERE status = 'unreconciled_settlement'),
			count(*) FILTER (WHERE status = 'out_of_scope_payment')
			FROM recon_records WHERE run_id = $1`, runID).
			Scan(&c.Reconciled, &c.UnreconciledPayment, &c.UnreconciledSettlement, &c.OutOfScopePayment)
	})
	return c, err
}

// execStep runs a step that may hold several statements; parameters are bound per statement.
func execStep(ctx context.Context, tx pgx.Tx, sql string, runID int64, scope []string) error {
	for _, stmt := range splitStatements(sql) {
		args := []any{}
		if containsParam(stmt, "$2") {
			args = []any{runID, scope}
		} else if containsParam(stmt, "$1") {
			args = []any{runID}
		}
		if _, err := tx.Exec(ctx, stmt, args...); err != nil {
			return err
		}
	}
	return nil
}
