// Package trace answers "where did this number come from": it walks report values back to
// source file lines and config rules, and lists rule/config anomalies for the investigation.
package trace

import (
	"context"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const traceCols = `source, file_name, line_no, COALESCE(transaction_type, ''), COALESCE(amount_type, ''),
	COALESCE(description, ''), COALESCE(rule_id::text, '-'), COALESCE(rule_src_line::text, '-'), amount_field,
	amount::text, summary_field, COALESCE(record_ref, ''), status, raw_line`

func printTrace(w io.Writer, rows pgx.Rows) error {
	tw := tabwriter.NewWriter(w, 0, 0, 1, ' ', 0)
	fmt.Fprintln(tw, "SOURCE\tFILE:LINE\tTYPE\tAMOUNT_TYPE\tDESCRIPTION\tRULE(id/line)\tFIELD\tAMOUNT\tSUMMARY_FIELD\tRECORD_REF\tSTATUS")
	var raws []string
	n := 0
	for rows.Next() {
		var src, file, typ, at, desc, rule, ruleLine, af, amt, sf, ref, st, raw string
		var line int
		if err := rows.Scan(&src, &file, &line, &typ, &at, &desc, &rule, &ruleLine, &af, &amt, &sf, &ref, &st, &raw); err != nil {
			return err
		}
		fmt.Fprintf(tw, "%s\t%s:%d\t%s\t%s\t%s\t%s/%s\t%s\t%s\t%s\t%s\t%s\n", src, file, line, typ, at, desc, rule, ruleLine, af, amt, sf, ref, st)
		raws = append(raws, fmt.Sprintf("%s:%d  %s", file, line, raw))
		n++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	tw.Flush()
	fmt.Fprintf(w, "\n%d components. Raw source lines:\n", n)
	seen := map[string]bool{}
	for _, r := range raws {
		if !seen[r] {
			fmt.Fprintln(w, "  "+r)
			seen[r] = true
		}
	}
	return nil
}

// RecordRef prints every component (both sources) that carries the record_ref.
func RecordRef(ctx context.Context, pool *pgxpool.Pool, w io.Writer, ref string, limit int) error {
	rows, err := pool.Query(ctx, `SELECT `+traceCols+` FROM v_component_trace WHERE record_ref = $1
		ORDER BY source, line_no, component_id LIMIT $2`, ref, limit)
	if err != nil {
		return err
	}
	defer rows.Close()
	return printTrace(w, rows)
}

// SummaryField prints the components of one source routed into a summary field (largest first)
// and the total, so a Summary sheet cell can be decomposed line by line.
func SummaryField(ctx context.Context, pool *pgxpool.Pool, w io.Writer, source, field string, limit int) error {
	var total string
	var n int
	if err := pool.QueryRow(ctx, `SELECT COALESCE(sum(amount), 0)::text, count(*) FROM v_component_trace
		WHERE source = $1 AND summary_field = $2 AND status = 'routed'`, source, field).Scan(&total, &n); err != nil {
		return err
	}
	fmt.Fprintf(w, "%s -> %s: %d components, total %s (all settlement ids)\n\n", source, field, n, total)
	rows, err := pool.Query(ctx, `SELECT `+traceCols+` FROM v_component_trace WHERE source = $1 AND summary_field = $2
		AND status = 'routed' ORDER BY abs(amount) DESC, line_no LIMIT $3`, source, field, limit)
	if err != nil {
		return err
	}
	defer rows.Close()
	return printTrace(w, rows)
}

// Rule prints the components produced by one rule.
func Rule(ctx context.Context, pool *pgxpool.Pool, w io.Writer, source string, id, limit int) error {
	rows, err := pool.Query(ctx, `SELECT `+traceCols+` FROM v_component_trace WHERE source = $1 AND rule_id = $2
		ORDER BY line_no LIMIT $3`, source, id, limit)
	if err != nil {
		return err
	}
	defer rows.Close()
	return printTrace(w, rows)
}
