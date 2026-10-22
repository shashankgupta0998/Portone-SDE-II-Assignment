package trace

import (
	"context"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Table is one diagnostic listing. Numeric marks columns that hold amounts or counts.
type Table struct {
	Title   string
	Note    string
	Columns []string
	Numeric []bool
	Rows    [][]string
}

type query struct {
	title, note string
	columns     []string
	numeric     []bool
	sql         string
}

// describe renders a row's description for grouping: rows matched through a wildcard rule
// (levels 2 and 4) are grouped together, since their description played no part in the match.
func describe(levelExpr string) string {
	return `CASE WHEN ` + levelExpr + ` IN (2, 4) THEN COALESCE(r.amount_type || ' / ', '') || '(any: matched by wildcard)'
	             ELSE COALESCE(r.amount_type || ' / ', '') || COALESCE(r.description, '') END`
}

// Every query reads the latest run only (derived tables hold only that run).
var diagnostics = []query{
	{
		title:   "Component status by source",
		note:    "routed = in a summary field; not_summarised = rule bucket empty for that sign; zero = amount 0; unmapped = no rule group; unruled_column = payments column with no rule in the matched group",
		columns: []string{"source", "status", "components", "source rows", "amount"},
		numeric: []bool{false, false, true, true, true},
		sql: `SELECT source, status, count(*)::text, count(DISTINCT source_row_id)::text, sum(amount)::text
		      FROM mapped_components GROUP BY 1, 2 ORDER BY 1, 2`,
	},
	{
		title:   "Unmapped amounts (no rule group matched the row)",
		columns: []string{"source", "settlement id", "transaction type", "amount type", "description", "amount field", "components", "amount", "file lines (first 20)"},
		numeric: []bool{false, false, false, false, false, false, true, true, false},
		sql: `SELECT c.source, COALESCE(r.settlement_id, ''), COALESCE(r.transaction_type, ''), COALESCE(r.amount_type, ''),
		             COALESCE(r.description, ''), c.amount_field, count(*)::text, sum(c.amount)::text,
		             array_to_string((array_agg(r.line_no ORDER BY r.line_no))[1:20], ',')
		      FROM mapped_components c JOIN source_rows r ON r.id = c.source_row_id
		      WHERE c.status = 'unmapped' GROUP BY 1, 2, 3, 4, 5, 6 ORDER BY 1, 2, 3, 4, 5, 6`,
	},
	{
		title:   "Unruled payment columns (non-zero column, no rule for it in the matched group)",
		columns: []string{"source", "settlement id", "transaction type", "description", "amount field", "group level", "components", "amount", "file lines (first 20)"},
		numeric: []bool{false, false, false, false, false, true, true, true, false},
		sql: `SELECT c.source, COALESCE(r.settlement_id, ''), COALESCE(r.transaction_type, ''), ` + describe("c.rule_level") + `,
		             c.amount_field, COALESCE(c.rule_level::text, ''), count(*)::text, sum(c.amount)::text,
		             array_to_string((array_agg(r.line_no ORDER BY r.line_no))[1:20], ',')
		      FROM mapped_components c JOIN source_rows r ON r.id = c.source_row_id
		      WHERE c.status = 'unruled_column' GROUP BY 1, 2, 3, 4, 5, 6 ORDER BY 1, 2, 3, 4, 5`,
	},
	{
		title:   "Rules that never fired",
		note:    "no source row selected the rule, or its amount_field is not a column of the file",
		columns: []string{"source", "rule id", "config line", "transaction_type", "description / amount_type", "amount_description", "amount_field", "positive bucket", "negative bucket"},
		numeric: []bool{false, true, true, false, false, false, false, false, false},
		sql: `SELECT 'payments', p.id::text, COALESCE(p.src_line::text, 'fix'), p.transaction_type, p.description, '', p.amount_field,
		             p.to_summary_field_when_positive_amount, p.to_summary_field_when_negative_amount
		      FROM payment_mapping_rules p
		      WHERE NOT EXISTS (SELECT 1 FROM mapped_components c WHERE c.source = 'payments' AND c.rule_id = p.id)
		      UNION ALL
		      SELECT 'settlements', s.id::text, COALESCE(s.src_line::text, 'fix'), s.transaction_type, s.amount_type, s.amount_description, 'amount',
		             s.to_summary_field_when_positive_amount, s.to_summary_field_when_negative_amount
		      FROM settlement_mapping_rules s
		      WHERE NOT EXISTS (SELECT 1 FROM mapped_components c WHERE c.source = 'settlements' AND c.rule_id = s.id)
		      ORDER BY 1, 2`,
	},
	{
		title:   "Same amount applied by more than one rule (potential double counting)",
		note:    "one source amount (payments: one column of one row) produced several components",
		columns: []string{"source", "transaction type", "description", "amount field", "rule ids", "summary fields", "source rows", "amount per copy", "amount routed in total"},
		numeric: []bool{false, false, false, false, false, false, true, true, true},
		sql: `WITH dup AS (
		        SELECT c.source_row_id, c.source, c.amount_field,
		               string_agg(c.rule_id::text, ',' ORDER BY c.rule_id) AS rule_ids,
		               string_agg(DISTINCT NULLIF(c.summary_field, ''), ',') AS fields,
		               max(c.amount) AS amount, max(c.rule_level) AS rule_level,
		               sum(c.amount) FILTER (WHERE c.status = 'routed') AS routed
		        FROM mapped_components c WHERE c.rule_id IS NOT NULL
		        GROUP BY c.source_row_id, c.source, c.amount_field HAVING count(*) > 1)
		      SELECT d.source, COALESCE(r.transaction_type, ''), ` + describe("d.rule_level") + `,
		             d.amount_field, d.rule_ids, COALESCE(d.fields, ''), count(*)::text, sum(d.amount)::text,
		             COALESCE(sum(d.routed), 0)::text
		      FROM dup d JOIN source_rows r ON r.id = d.source_row_id
		      WHERE d.amount <> 0
		      GROUP BY 1, 2, 3, 4, 5, 6 ORDER BY 1, 2, 3, 4`,
	},
	{
		title:   "record_ref field tokens that resolved empty",
		columns: []string{"source", "rule id", "transaction type", "description", "empty tokens", "components", "example record_ref"},
		numeric: []bool{false, true, false, false, false, true, false},
		sql: `SELECT c.source, COALESCE(c.rule_id::text, ''), COALESCE(r.transaction_type, ''), ` + describe("c.rule_level") + `,
		             c.empty_tokens, count(*)::text, min(c.record_ref)
		      FROM mapped_components c JOIN source_rows r ON r.id = c.source_row_id
		      WHERE c.empty_tokens IS NOT NULL GROUP BY 1, 2, 3, 4, 5 ORDER BY 1, 2`,
	},
	{
		title:   "Source rows whose rules build more than one record_ref",
		columns: []string{"source", "transaction type", "description", "distinct refs per row", "source rows"},
		numeric: []bool{false, false, false, true, true},
		sql: `WITH m AS (SELECT source_row_id, count(DISTINCT record_ref) AS n, max(rule_level) AS rule_level FROM mapped_components
		               WHERE record_ref IS NOT NULL GROUP BY 1 HAVING count(DISTINCT record_ref) > 1)
		      SELECT r.source, COALESCE(r.transaction_type, ''), ` + describe("m.rule_level") + `, m.n::text, count(*)::text
		      FROM m JOIN source_rows r ON r.id = m.source_row_id GROUP BY 1, 2, 3, 4 ORDER BY 1, 2, 3`,
	},
	{
		title:   "Amounts routed to summary fields that are not a Summary sheet line",
		columns: []string{"source", "settlement id", "summary field", "components", "amount"},
		numeric: []bool{false, false, false, true, true},
		sql: `SELECT c.source, COALESCE(r.settlement_id, ''), c.summary_field, count(*)::text, sum(c.amount)::text
		      FROM mapped_components c JOIN source_rows r ON r.id = c.source_row_id
		      WHERE c.status = 'routed' AND c.summary_field NOT IN (SELECT summary_field FROM summary_lines)
		      GROUP BY 1, 2, 3 ORDER BY 1, 2, 3`,
	},
	{
		title:   "Ingestion errors (first 200)",
		columns: []string{"file", "line", "field", "raw value", "message"},
		numeric: []bool{false, true, false, false, false},
		sql: `SELECT f.file_name, e.line_no::text, e.field, e.raw_value, e.message
		      FROM ingest_errors e JOIN ingest_files f ON f.id = e.file_id
		      WHERE e.severity = 'error' ORDER BY f.file_name, e.line_no LIMIT 200`,
	},
}

// Diagnose runs every diagnostic query against the latest run.
func Diagnose(ctx context.Context, pool *pgxpool.Pool) ([]Table, error) {
	out := make([]Table, 0, len(diagnostics))
	for _, q := range diagnostics {
		rows, err := pool.Query(ctx, q.sql)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", q.title, err)
		}
		t := Table{Title: q.title, Note: q.note, Columns: q.columns, Numeric: q.numeric}
		for rows.Next() {
			vals := make([]string, len(q.columns))
			ptrs := make([]any, len(vals))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				rows.Close()
				return nil, fmt.Errorf("%s: %w", q.title, err)
			}
			t.Rows = append(t.Rows, vals)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("%s: %w", q.title, err)
		}
		out = append(out, t)
	}
	return out, nil
}

// PrintDiagnostics renders the tables for the terminal.
func PrintDiagnostics(w io.Writer, tables []Table) {
	for _, t := range tables {
		fmt.Fprintf(w, "\n== %s (%d)\n", t.Title, len(t.Rows))
		if t.Note != "" {
			fmt.Fprintf(w, "   %s\n", t.Note)
		}
		if len(t.Rows) == 0 {
			fmt.Fprintln(w, "   (none)")
			continue
		}
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "   "+strings.Join(t.Columns, "\t"))
		for _, r := range t.Rows {
			fmt.Fprintln(tw, "   "+strings.Join(r, "\t"))
		}
		tw.Flush()
	}
}
