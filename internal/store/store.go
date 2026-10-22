// Package store writes ingested rows and their mapped components to PostgreSQL with
// batched COPY, inside the caller's per-file transaction.
package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/shopspring/decimal"

	"amazonrecon/internal/ingest"
	"amazonrecon/internal/mapping"
)

// BatchSize is the number of source rows per COPY round trip.
const BatchSize = 5000

var sourceRowColumns = []string{
	"id", "file_id", "source", "line_no", "row_kind",
	"settlement_id", "transaction_type", "amount_type", "description", "order_id", "merchant_order_id",
	"adjustment_id", "shipment_id", "sku", "quantity", "marketplace", "fulfillment",
	"posted_at", "release_at", "txn_status", "ref_date",
	"amount", "product_sales", "shipping_credits", "gift_wrap_credits", "promotional_rebates",
	"sales_tax_collected", "low_value_goods", "selling_fees", "fba_fees", "other_transaction_fees",
	"other", "total",
	"settlement_start", "settlement_end", "deposit_date", "total_amount", "currency",
	"raw", "raw_line",
}

var componentColumns = []string{
	"run_id", "source_row_id", "source", "rule_id", "rule_level", "amount_field", "amount",
	"summary_field", "record_ref", "status", "empty_tokens",
}

// FileInfo identifies an input file.
type FileInfo struct {
	Path   string
	Name   string
	SHA256 string
	Size   int64
}

// Describe hashes a file so the run records exactly which bytes were ingested.
func Describe(path string) (FileInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return FileInfo{}, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return FileInfo{}, err
	}
	return FileInfo{Path: path, Name: filepath.Base(path), SHA256: hex.EncodeToString(h.Sum(nil)), Size: n}, nil
}

// ReplaceFile deletes any earlier ingestion of the same (source, file name) — cascading to its
// rows and components — and registers the file again. This makes re-ingestion idempotent.
func ReplaceFile(ctx context.Context, tx pgx.Tx, source string, fi FileInfo) (int64, error) {
	if _, err := tx.Exec(ctx, `DELETE FROM ingest_files WHERE source = $1 AND file_name = $2`, source, fi.Name); err != nil {
		return 0, err
	}
	var id int64
	err := tx.QueryRow(ctx, `INSERT INTO ingest_files (source, file_name, sha256, size_bytes)
		VALUES ($1, $2, $3, $4) RETURNING id`, source, fi.Name, fi.SHA256, fi.Size).Scan(&id)
	return id, err
}

// FinishFile records row counts for a file.
func FinishFile(ctx context.Context, tx pgx.Tx, fileID int64, dataRows, skipped, errorRows int) error {
	_, err := tx.Exec(ctx, `UPDATE ingest_files SET data_rows = $2, skipped_lines = $3, error_rows = $4 WHERE id = $1`,
		fileID, dataRows, skipped, errorRows)
	return err
}

// Writer buffers rows and components and flushes them with COPY.
type Writer struct {
	tx     pgx.Tx
	runID  int64
	fileID int64
	rows   []*ingest.Row
	comps  [][]mapping.Component
}

// NewWriter returns a writer bound to one file inside tx.
func NewWriter(tx pgx.Tx, runID, fileID int64) *Writer {
	return &Writer{tx: tx, runID: runID, fileID: fileID}
}

// Add buffers one row with its components, flushing when the batch is full.
func (w *Writer) Add(ctx context.Context, r *ingest.Row, cs []mapping.Component) error {
	w.rows = append(w.rows, r)
	w.comps = append(w.comps, cs)
	if len(w.rows) >= BatchSize {
		return w.Flush(ctx)
	}
	return nil
}

// Flush writes buffered rows, their components and any parse errors.
func (w *Writer) Flush(ctx context.Context) error {
	if len(w.rows) == 0 {
		return nil
	}
	ids, err := w.reserveIDs(ctx, len(w.rows))
	if err != nil {
		return err
	}
	rowVals := make([][]any, len(w.rows))
	var compVals, errVals [][]any
	for i, r := range w.rows {
		rowVals[i] = rowValues(ids[i], w.fileID, r)
		for _, c := range w.comps[i] {
			compVals = append(compVals, componentValues(w.runID, ids[i], r.Source, c))
		}
		for _, fe := range r.Errors {
			errVals = append(errVals, []any{w.fileID, r.LineNo, "error", fe.Field, fe.RawValue, fe.Message})
		}
	}
	if _, err := w.tx.CopyFrom(ctx, pgx.Identifier{"source_rows"}, sourceRowColumns, pgx.CopyFromRows(rowVals)); err != nil {
		return fmt.Errorf("copy source_rows: %w", err)
	}
	if len(compVals) > 0 {
		if _, err := w.tx.CopyFrom(ctx, pgx.Identifier{"mapped_components"}, componentColumns, pgx.CopyFromRows(compVals)); err != nil {
			return fmt.Errorf("copy mapped_components: %w", err)
		}
	}
	if len(errVals) > 0 {
		if err := WriteErrors(ctx, w.tx, errVals); err != nil {
			return err
		}
	}
	w.rows, w.comps = w.rows[:0], w.comps[:0]
	return nil
}

// WriteErrors stores ingest_errors rows: file_id, line_no, severity, field, raw_value, message.
func WriteErrors(ctx context.Context, tx pgx.Tx, vals [][]any) error {
	_, err := tx.CopyFrom(ctx, pgx.Identifier{"ingest_errors"},
		[]string{"file_id", "line_no", "severity", "field", "raw_value", "message"}, pgx.CopyFromRows(vals))
	if err != nil {
		return fmt.Errorf("copy ingest_errors: %w", err)
	}
	return nil
}

// reserveIDs takes n ids from the source_rows sequence so components can reference rows
// written in the same COPY batch.
func (w *Writer) reserveIDs(ctx context.Context, n int) ([]int64, error) {
	rows, err := w.tx.Query(ctx, `SELECT nextval(pg_get_serial_sequence('source_rows', 'id')) FROM generate_series(1, $1)`, n)
	if err != nil {
		return nil, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return nil, err
	}
	return ids, nil
}

func rowValues(id, fileID int64, r *ingest.Row) []any {
	v := []any{
		id, fileID, r.Source, r.LineNo, r.Kind,
		nullStr(r.SettlementID), nullStr(r.TxnType), nullStr(r.AmountType), nullStr(r.Description),
		nullStr(r.OrderID), nullStr(r.MerchantOrderID), nullStr(r.AdjustmentID), nullStr(r.ShipmentID),
		nullStr(r.SKU), r.Quantity, nullStr(r.Marketplace), nullStr(r.Fulfillment),
		r.PostedAt, r.ReleaseAt, nullStr(r.TxnStatus), dateOrNil(r.RefDate),
	}
	if r.Source == ingest.SourceSettlements && r.Kind == ingest.KindData {
		v = append(v, Numeric(r.Amount))
	} else {
		v = append(v, pgtype.Numeric{})
	}
	for _, f := range ingest.PaymentAmountFields {
		if d, ok := r.Cols[f]; ok {
			v = append(v, Numeric(d))
		} else {
			v = append(v, pgtype.Numeric{})
		}
	}
	total := pgtype.Numeric{}
	if r.TotalAmount != nil {
		total = Numeric(*r.TotalAmount)
	}
	v = append(v, r.SettlementStart, r.SettlementEnd, r.DepositDate, total, nullStr(r.Currency), r.Raw, r.RawLine)
	return v
}

func componentValues(runID, rowID int64, source string, c mapping.Component) []any {
	var ruleID *int32
	var level *int16
	if c.RuleID != 0 {
		id := int32(c.RuleID)
		ruleID = &id
	}
	if c.Level != 0 {
		l := int16(c.Level)
		level = &l
	}
	var ref *string
	if c.HasRef {
		ref = &c.RecordRef
	}
	var empty *string
	if len(c.EmptyTokens) > 0 {
		s := strings.Join(c.EmptyTokens, ",")
		empty = &s
	}
	return []any{runID, rowID, source, ruleID, level, c.AmountField, Numeric(c.Amount), c.SummaryField, ref, c.Status, empty}
}

// Numeric converts an exact decimal to the pgx numeric type without going through float.
func Numeric(d decimal.Decimal) pgtype.Numeric {
	return pgtype.Numeric{Int: d.Coefficient(), Exp: d.Exponent(), Valid: true}
}

func nullStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func dateOrNil(t *time.Time) any {
	if t == nil {
		return nil
	}
	return pgtype.Date{Time: *t, Valid: true}
}
