// Package rules loads the mapping config CSVs verbatim into their tables and reads them back
// for the mapping engine. Fixes are applied to the tables by SQL (MAPPING_FIXES.sql), never here.
package rules

import (
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"amazonrecon/internal/ingest"
	"amazonrecon/internal/mapping"
)

var paymentColumns = []string{
	"transaction_type", "description", "amount_field", "record_ref",
	"to_summary_field_when_positive_amount", "to_summary_field_when_negative_amount",
}

var settlementColumns = []string{
	"transaction_type", "amount_type", "amount_description", "record_ref",
	"to_summary_field_when_positive_amount", "to_summary_field_when_negative_amount",
}

// configRow is one CSV record with its physical line number.
type configRow struct {
	line   int
	values []string
}

func readConfig(path string, want []string) ([]configRow, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.FieldsPerRecord = len(want)
	header, err := r.Read()
	if err != nil {
		return nil, fmt.Errorf("%s: header: %w", path, err)
	}
	for i, h := range header {
		h = strings.TrimSpace(strings.TrimPrefix(h, "\uFEFF"))
		if h != want[i] {
			return nil, fmt.Errorf("%s: column %d is %q, expected %q", path, i+1, h, want[i])
		}
	}
	var out []configRow
	for {
		rec, err := r.Read()
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		line, _ := r.FieldPos(0)
		out = append(out, configRow{line: line, values: rec})
	}
}

// LoadCSVs replaces both rule tables with the contents of the two config files, exactly as
// written (no trimming or case changes). Ids restart at 1 so they follow file order.
func LoadCSVs(ctx context.Context, pool *pgxpool.Pool, paymentsPath, settlementsPath string) (int, int, error) {
	pay, err := readConfig(paymentsPath, paymentColumns)
	if err != nil {
		return 0, 0, err
	}
	set, err := readConfig(settlementsPath, settlementColumns)
	if err != nil {
		return 0, 0, err
	}
	err = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `TRUNCATE payment_mapping_rules, settlement_mapping_rules RESTART IDENTITY`); err != nil {
			return err
		}
		if err := insert(ctx, tx, "payment_mapping_rules", paymentColumns, pay); err != nil {
			return err
		}
		return insert(ctx, tx, "settlement_mapping_rules", settlementColumns, set)
	})
	return len(pay), len(set), err
}

func insert(ctx context.Context, tx pgx.Tx, table string, cols []string, rows []configRow) error {
	src := make([][]any, len(rows))
	for i, r := range rows {
		vals := []any{r.line}
		for _, v := range r.values {
			vals = append(vals, v)
		}
		src[i] = vals
	}
	_, err := tx.CopyFrom(ctx, pgx.Identifier{table}, append([]string{"src_line"}, cols...), pgx.CopyFromRows(src))
	return err
}

// Load reads the current rule tables (including any fixes applied to them) in id order.
func Load(ctx context.Context, pool *pgxpool.Pool) (payments, settlements []mapping.Rule, err error) {
	rows, err := pool.Query(ctx, `SELECT id, src_line, transaction_type, description, amount_field, record_ref,
		to_summary_field_when_positive_amount, to_summary_field_when_negative_amount
		FROM payment_mapping_rules ORDER BY id`)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var r mapping.Rule
		if err := rows.Scan(&r.ID, &r.SrcLine, &r.TxnType, &r.Key1, &r.AmountField, &r.RecordRef, &r.Positive, &r.Negative); err != nil {
			return nil, nil, err
		}
		r.AmountField = strings.TrimSpace(r.AmountField)
		payments = append(payments, r)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	rows, err = pool.Query(ctx, `SELECT id, src_line, transaction_type, amount_type, amount_description, record_ref,
		to_summary_field_when_positive_amount, to_summary_field_when_negative_amount
		FROM settlement_mapping_rules ORDER BY id`)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var r mapping.Rule
		if err := rows.Scan(&r.ID, &r.SrcLine, &r.TxnType, &r.Key1, &r.Key2, &r.RecordRef, &r.Positive, &r.Negative); err != nil {
			return nil, nil, err
		}
		r.AmountField = mapping.SettlementAmountField
		settlements = append(settlements, r)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	if len(payments) == 0 || len(settlements) == 0 {
		return nil, nil, fmt.Errorf("mapping rule tables are empty: run `recon load-config` first")
	}
	return payments, settlements, nil
}

// Hash fingerprints a rule set so each run records exactly which config produced it.
func Hash(rules []mapping.Rule) string {
	h := sha256.New()
	for _, r := range rules {
		fmt.Fprintf(h, "%d\x1f%s\x1f%s\x1f%s\x1f%s\x1f%s\x1f%s\x1f%s\n",
			r.ID, r.TxnType, r.Key1, r.Key2, r.AmountField, r.RecordRef, r.Positive, r.Negative)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Engines builds the two mapping engines.
func Engines(payments, settlements []mapping.Rule) (*mapping.Engine, *mapping.Engine) {
	return mapping.NewEngine(ingest.SourcePayments, payments), mapping.NewEngine(ingest.SourceSettlements, settlements)
}
