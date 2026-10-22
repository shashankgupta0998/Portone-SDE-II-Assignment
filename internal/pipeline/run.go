package pipeline

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"amazonrecon/internal/ingest"
	"amazonrecon/internal/recon"
	"amazonrecon/internal/rules"
	"amazonrecon/internal/store"
	"amazonrecon/internal/summary"
)

// Options configure one end-to-end run.
type Options struct {
	PaymentsPath    string
	SettlementsPath string
	Label           string
	Scope           []string // settlement ids; empty = the ids found in the settlement file
}

// Result is what a run produced.
type Result struct {
	RunID  int64
	Scope  []string
	Files  []FileResult
	Counts recon.Counts
	Checks []recon.Check
}

// Failed reports whether any self-check failed.
func (r Result) Failed() bool {
	for _, c := range r.Checks {
		if c.Status == "fail" {
			return true
		}
	}
	return false
}

// Ingest maps and stores both files under a new run and persists the streamed summary.
// Derived tables only hold the latest run; runs / run_history_summary keep the history.
func Ingest(ctx context.Context, pool *pgxpool.Pool, opts Options) (Result, error) {
	var res Result
	payRules, setRules, err := rules.Load(ctx, pool)
	if err != nil {
		return res, err
	}
	payEngine, setEngine := rules.Engines(payRules, setRules)

	if _, err := pool.Exec(ctx, `TRUNCATE mapped_components, stream_summary, recon_record_buckets, recon_records`); err != nil {
		return res, err
	}
	err = pool.QueryRow(ctx, `INSERT INTO runs (label, payment_rules_sha256, settlement_rules_sha256)
		VALUES ($1, $2, $3) RETURNING id`, opts.Label, rules.Hash(payRules), rules.Hash(setRules)).Scan(&res.RunID)
	if err != nil {
		return res, err
	}

	acc := summary.New()
	// Settlements first: the settlement file defines the settlement period being reconciled.
	for _, f := range []struct{ source, path string }{
		{ingest.SourceSettlements, opts.SettlementsPath},
		{ingest.SourcePayments, opts.PaymentsPath},
	} {
		engine := payEngine
		if f.source == ingest.SourceSettlements {
			engine = setEngine
		}
		fr, err := IngestFile(ctx, pool, res.RunID, f.source, f.path, engine, acc)
		if err != nil {
			return res, fmt.Errorf("ingest %s: %w", f.path, err)
		}
		res.Files = append(res.Files, fr)
	}

	vals := [][]any{}
	for _, t := range acc.Rows() {
		vals = append(vals, []any{res.RunID, t.Source, t.SettlementID, t.TxnStatus, t.SummaryField, store.Numeric(t.Amount), t.Components})
	}
	_, err = pool.CopyFrom(ctx, pgx.Identifier{"stream_summary"},
		[]string{"run_id", "source", "settlement_id", "txn_status", "summary_field", "amount", "components"}, pgx.CopyFromRows(vals))
	return res, err
}

// Reconcile matches the latest run, records self-checks, counts and summary history.
func Reconcile(ctx context.Context, pool *pgxpool.Pool, res *Result, scope []string) error {
	var err error
	if len(scope) == 0 {
		if scope, err = recon.DefaultScope(ctx, pool); err != nil {
			return err
		}
	}
	if len(scope) == 0 {
		return fmt.Errorf("no settlement ids in scope (is the settlement file ingested?)")
	}
	res.Scope = scope
	if res.Counts, err = recon.Reconcile(ctx, pool, res.RunID, scope); err != nil {
		return err
	}
	if res.Checks, err = recon.SelfChecks(ctx, pool, res.RunID, scope); err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM run_checks WHERE run_id = $1`, res.RunID); err != nil {
			return err
		}
		for _, c := range res.Checks {
			if _, err := tx.Exec(ctx, `INSERT INTO run_checks (run_id, name, status, detail) VALUES ($1, $2, $3, $4)`,
				res.RunID, c.Name, c.Status, c.Detail); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE runs SET scope_settlement_ids = $2, reconciled = $3, unreconciled_payment = $4,
			unreconciled_settlement = $5, out_of_scope_payment = $6 WHERE id = $1`,
			res.RunID, scope, res.Counts.Reconciled, res.Counts.UnreconciledPayment,
			res.Counts.UnreconciledSettlement, res.Counts.OutOfScopePayment); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM run_history_summary WHERE run_id = $1`, res.RunID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO run_history_summary (run_id, source, summary_field, amount)
			SELECT run_id, source, summary_field, sum(amount) FROM stream_summary
			WHERE run_id = $1 AND settlement_id = ANY($2::text[]) GROUP BY run_id, source, summary_field`, res.RunID, scope)
		return err
	})
}

// Finish stamps the run as finished with the report path.
func Finish(ctx context.Context, pool *pgxpool.Pool, runID int64, reportPath string) error {
	_, err := pool.Exec(ctx, `UPDATE runs SET finished_at = now(), report_path = $2 WHERE id = $1`, runID, reportPath)
	return err
}

// LatestRun returns the id of the most recent run (the one whose detail is in the derived tables).
func LatestRun(ctx context.Context, pool *pgxpool.Pool) (int64, []string, error) {
	var id int64
	var scope []string
	err := pool.QueryRow(ctx, `SELECT id, COALESCE(scope_settlement_ids, '{}') FROM runs ORDER BY id DESC LIMIT 1`).Scan(&id, &scope)
	if err == pgx.ErrNoRows {
		return 0, nil, fmt.Errorf("no runs yet: run `recon ingest` or `recon run` first")
	}
	return id, scope, err
}

// FormatCounts renders the recon counts for terminal output.
func FormatCounts(c recon.Counts) string {
	return strings.Join([]string{
		fmt.Sprintf("reconciled=%d", c.Reconciled),
		fmt.Sprintf("unreconciled_payment=%d", c.UnreconciledPayment),
		fmt.Sprintf("unreconciled_settlement=%d", c.UnreconciledSettlement),
		fmt.Sprintf("out_of_scope_payment=%d", c.OutOfScopePayment),
	}, " ")
}
