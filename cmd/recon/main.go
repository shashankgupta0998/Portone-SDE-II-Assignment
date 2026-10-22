// Command recon ingests the Amazon Payments and Settlement reports into PostgreSQL, applies the
// mapping configs, reconciles the two sources on record_ref and writes the Excel report.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"amazonrecon/internal/config"
	"amazonrecon/internal/db"
	"amazonrecon/internal/pipeline"
	"amazonrecon/internal/report"
	"amazonrecon/internal/rules"
	"amazonrecon/internal/trace"
)

const usage = `recon — Amazon Payments vs Settlement reconciliation

Usage:
  recon migrate
  recon load-config  --payments-config PATH --settlement-config PATH
  recon apply-sql    --file MAPPING_FIXES.sql
  recon ingest       --payments PATH --settlements PATH [--label L]
  recon reconcile    [--settlement-id ID,...]
  recon report       --out PATH.xlsx
  recon run          --payments PATH --settlements PATH --out PATH.xlsx [--label L] [--settlement-id ID,...]
  recon trace        --record-ref REF | --summary-field F --source payments|settlements | --rule-id N --source S  [--limit N]
  recon diagnose

Environment: DATABASE_URL (required), RECON_SCHEMA (default "recon").
`

type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error {
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			*s = append(*s, p)
		}
	}
	return nil
}

func main() {
	if len(os.Args) < 2 || os.Args[1] == "-h" || os.Args[1] == "--help" || os.Args[1] == "help" {
		fmt.Print(usage)
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := dispatch(ctx, os.Args[1], os.Args[2:]); err != nil {
		fmt.Fprintln(os.Stderr, "recon:", err)
		os.Exit(1)
	}
}

func connect(ctx context.Context) (*pgxpool.Pool, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	pool, err := db.Connect(ctx, cfg.DatabaseURL, cfg.Schema)
	if err != nil {
		return nil, err
	}
	if _, err := db.Migrate(ctx, pool); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}

func dispatch(ctx context.Context, cmd string, args []string) error {
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	var (
		payments    = fs.String("payments", "", "Amazon Payments CSV")
		settlements = fs.String("settlements", "", "Amazon Settlement flat file (TSV)")
		payConfig   = fs.String("payments-config", "", "payment mapping config CSV")
		setConfig   = fs.String("settlement-config", "", "settlement mapping config CSV")
		sqlFile     = fs.String("file", "", "SQL script to execute")
		label       = fs.String("label", "", "run label, e.g. before-fix / after-fix")
		out         = fs.String("out", "", "output .xlsx path")
		recordRef   = fs.String("record-ref", "", "record_ref to trace")
		field       = fs.String("summary-field", "", "summary field to trace")
		source      = fs.String("source", "", "payments | settlements")
		ruleID      = fs.Int("rule-id", 0, "rule id to trace")
		limit       = fs.Int("limit", 50, "max rows to print")
		scope       stringList
	)
	fs.Var(&scope, "settlement-id", "settlement id(s) to reconcile (default: those in the settlement file)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *label == "" {
		*label = time.Now().UTC().Format("run-20060102T150405Z")
	}
	required := func(names ...string) error {
		for _, n := range names {
			if fs.Lookup(n).Value.String() == "" {
				return fmt.Errorf("%s: --%s is required", cmd, n)
			}
		}
		return nil
	}

	pool, err := connect(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()

	switch cmd {
	case "migrate":
		fmt.Println("schema is up to date")
		return nil

	case "load-config":
		if err := required("payments-config", "settlement-config"); err != nil {
			return err
		}
		np, ns, err := rules.LoadCSVs(ctx, pool, *payConfig, *setConfig)
		if err != nil {
			return err
		}
		fmt.Printf("loaded %d payment rules and %d settlement rules (verbatim)\n", np, ns)
		return nil

	case "apply-sql":
		if err := required("file"); err != nil {
			return err
		}
		body, err := os.ReadFile(*sqlFile)
		if err != nil {
			return err
		}
		if err := db.ExecFile(ctx, pool, string(body)); err != nil {
			return fmt.Errorf("%s: %w", *sqlFile, err)
		}
		fmt.Printf("applied %s\n", *sqlFile)
		return nil

	case "ingest":
		if err := required("payments", "settlements"); err != nil {
			return err
		}
		res, err := pipeline.Ingest(ctx, pool, pipeline.Options{PaymentsPath: *payments, SettlementsPath: *settlements, Label: *label})
		if err != nil {
			return err
		}
		printFiles(res)
		return nil

	case "reconcile":
		runID, _, err := pipeline.LatestRun(ctx, pool)
		if err != nil {
			return err
		}
		res := pipeline.Result{RunID: runID}
		if err := pipeline.Reconcile(ctx, pool, &res, scope); err != nil {
			return err
		}
		printRecon(res)
		return failOnChecks(res)

	case "report":
		if err := required("out"); err != nil {
			return err
		}
		runID, _, err := pipeline.LatestRun(ctx, pool)
		if err != nil {
			return err
		}
		if err := report.Write(ctx, pool, runID, *out); err != nil {
			return err
		}
		fmt.Printf("wrote %s (run %d)\n", *out, runID)
		return pipeline.Finish(ctx, pool, runID, *out)

	case "run":
		if err := required("payments", "settlements", "out"); err != nil {
			return err
		}
		res, err := pipeline.Ingest(ctx, pool, pipeline.Options{PaymentsPath: *payments, SettlementsPath: *settlements, Label: *label})
		if err != nil {
			return err
		}
		printFiles(res)
		if err := pipeline.Reconcile(ctx, pool, &res, scope); err != nil {
			return err
		}
		printRecon(res)
		if err := report.Write(ctx, pool, res.RunID, *out); err != nil {
			return err
		}
		if err := pipeline.Finish(ctx, pool, res.RunID, *out); err != nil {
			return err
		}
		fmt.Printf("wrote %s (run %d, label %s)\n", *out, res.RunID, *label)
		return failOnChecks(res)

	case "trace":
		switch {
		case *recordRef != "":
			return trace.RecordRef(ctx, pool, os.Stdout, *recordRef, *limit)
		case *field != "":
			if err := required("source"); err != nil {
				return err
			}
			return trace.SummaryField(ctx, pool, os.Stdout, *source, *field, *limit)
		case *ruleID != 0:
			if err := required("source"); err != nil {
				return err
			}
			return trace.Rule(ctx, pool, os.Stdout, *source, *ruleID, *limit)
		}
		return errors.New("trace: give --record-ref, --summary-field or --rule-id")

	case "diagnose":
		d, err := trace.Diagnose(ctx, pool)
		if err != nil {
			return err
		}
		trace.PrintDiagnostics(os.Stdout, d)
		return nil
	}
	fmt.Print(usage)
	return fmt.Errorf("unknown command %q", cmd)
}

func printFiles(res pipeline.Result) {
	fmt.Printf("run %d\n", res.RunID)
	for _, f := range res.Files {
		fmt.Printf("  %-11s %s: %d data rows, %d header rows, %d error rows, %d skipped lines, %d components\n",
			f.Source, f.File.Name, f.DataRows, f.HeaderRows, f.ErrorRows, f.Skipped, f.Components)
	}
}

func printRecon(res pipeline.Result) {
	fmt.Printf("scope: settlement %s\n", strings.Join(res.Scope, ","))
	fmt.Printf("records: %s\n", pipeline.FormatCounts(res.Counts))
	for _, c := range res.Checks {
		fmt.Printf("  [%s] %s: %s\n", c.Status, c.Name, c.Detail)
	}
}

func failOnChecks(res pipeline.Result) error {
	if res.Failed() {
		return errors.New("one or more self-checks failed (see above)")
	}
	return nil
}
