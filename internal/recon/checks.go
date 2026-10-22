package recon

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// SelfChecks verifies the run against independent computations and Amazon's own control total.
// Nothing here changes any number; failures are reported, not corrected.
func SelfChecks(ctx context.Context, pool *pgxpool.Pool, runID int64, scope []string) ([]Check, error) {
	var checks []Check

	// 1. Streamed summary (computed during ingestion) == SQL aggregate of stored components.
	var mismatches int
	var sample string
	err := pool.QueryRow(ctx, `
WITH sql AS (
  SELECT c.source, COALESCE(r.settlement_id, '') AS settlement_id, COALESCE(r.txn_status, '') AS txn_status,
         c.summary_field, sum(c.amount) AS amount, count(*) AS n
  FROM mapped_components c JOIN source_rows r ON r.id = c.source_row_id
  WHERE c.run_id = $1 AND c.status = 'routed'
  GROUP BY 1, 2, 3, 4),
st AS (SELECT source, settlement_id, txn_status, summary_field, amount, components AS n FROM stream_summary WHERE run_id = $1),
d AS (
  SELECT COALESCE(sql.source, st.source) || '/' || COALESCE(sql.settlement_id, st.settlement_id) || '/' ||
         COALESCE(sql.summary_field, st.summary_field) AS k
  FROM sql FULL JOIN st USING (source, settlement_id, txn_status, summary_field)
  WHERE sql.amount IS DISTINCT FROM st.amount OR sql.n IS DISTINCT FROM st.n)
SELECT count(*), COALESCE(string_agg(k, ', '), '') FROM d`, runID).Scan(&mismatches, &sample)
	if err != nil {
		return nil, err
	}
	if mismatches == 0 {
		checks = append(checks, Check{"stream_summary_equals_sql", "pass", "streamed summary totals equal the SQL aggregate of mapped_components for every (source, settlement, status, field)"})
	} else {
		checks = append(checks, Check{"stream_summary_equals_sql", "fail", fmt.Sprintf("%d keys differ: %s", mismatches, truncate(sample, 400))})
	}

	// 2. Settlement amounts sum to Amazon's header total-amount, per settlement in scope.
	rows, err := pool.Query(ctx, `
SELECT h.settlement_id, h.total_amount::text, COALESCE(d.amount, 0)::text, (h.total_amount = COALESCE(d.amount, 0))
FROM (SELECT settlement_id, sum(total_amount) AS total_amount FROM source_rows
      WHERE source = 'settlements' AND row_kind = 'settlement_header' GROUP BY 1) h
LEFT JOIN (SELECT settlement_id, sum(amount) AS amount FROM source_rows
      WHERE source = 'settlements' AND row_kind = 'data' GROUP BY 1) d USING (settlement_id)
WHERE h.settlement_id = ANY($1::text[]) ORDER BY 1`, scope)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var sid, header, sum string
		var ok bool
		if err := rows.Scan(&sid, &header, &sum, &ok); err != nil {
			return nil, err
		}
		st := "pass"
		if !ok {
			st = "fail"
		}
		checks = append(checks, Check{"settlement_sum_equals_header_total_" + sid, st,
			fmt.Sprintf("settlement %s: sum of amount = %s, header total-amount = %s", sid, sum, header)})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// 3. Payments: the typed amount columns of a row add up to its total (Amazon data property).
	var bad int
	err = pool.QueryRow(ctx, `SELECT count(*) FROM source_rows WHERE source = 'payments' AND row_kind = 'data' AND
		COALESCE(product_sales,0)+COALESCE(shipping_credits,0)+COALESCE(gift_wrap_credits,0)+COALESCE(promotional_rebates,0)+
		COALESCE(sales_tax_collected,0)+COALESCE(low_value_goods,0)+COALESCE(selling_fees,0)+COALESCE(fba_fees,0)+
		COALESCE(other_transaction_fees,0)+COALESCE(other,0) <> COALESCE(total,0)`).Scan(&bad)
	if err != nil {
		return nil, err
	}
	st := "pass"
	if bad > 0 {
		st = "info"
	}
	checks = append(checks, Check{"payment_columns_sum_to_total", st, fmt.Sprintf("%d payment rows where the amount columns do not add up to total", bad)})

	// 4. Ingestion errors (unparseable cells). Rows are kept with their raw payload but not mapped.
	var errs int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ingest_errors WHERE severity = 'error'`).Scan(&errs); err != nil {
		return nil, err
	}
	st = "pass"
	if errs > 0 {
		st = "fail"
	}
	checks = append(checks, Check{"ingest_errors", st, fmt.Sprintf("%d unparseable cells (see ingest_errors)", errs)})

	// 5. Amounts that matched no rule / no rule for their column (informational: they are listed
	//    on the Exceptions sheet and in `recon diagnose`).
	rows, err = pool.Query(ctx, `SELECT source, status, count(*), sum(amount)::text FROM mapped_components
		WHERE run_id = $1 AND status IN ('unmapped', 'unruled_column') GROUP BY 1, 2 ORDER BY 1, 2`, runID)
	if err != nil {
		return nil, err
	}
	var parts []string
	for rows.Next() {
		var src, status, sum string
		var n int
		if err := rows.Scan(&src, &status, &n, &sum); err != nil {
			return nil, err
		}
		parts = append(parts, fmt.Sprintf("%s %s: %d components, %s", src, status, n, sum))
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(parts) == 0 {
		checks = append(checks, Check{"unmapped_amounts", "pass", "every amount matched a rule"})
	} else {
		checks = append(checks, Check{"unmapped_amounts", "info", strings.Join(parts, "; ")})
	}
	return checks, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
