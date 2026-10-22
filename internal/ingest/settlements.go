package ingest

import (
	"bufio"
	"fmt"
	"io"
	"strings"
	"time"
)

var settlementRequiredHeaders = []string{
	"settlement-id", "transaction-type", "order-id", "amount-type", "amount-description",
	"amount", "posted-date", "sku",
}

// SettlementsReader streams the Amazon flat-file (V2) settlement report: tab separated,
// one header line, then one row per amount component. The first data row of a settlement
// carries the settlement totals and no amount; it is returned as KindSettlementHeader.
type SettlementsReader struct {
	sc      *bufio.Scanner
	line    int
	header  []string
	index   map[string]int
	skipped []Skipped
}

// NewSettlementsReader reads the header line.
func NewSettlementsReader(r io.Reader) (*SettlementsReader, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	sr := &SettlementsReader{sc: sc}
	if !sc.Scan() {
		if err := sc.Err(); err != nil {
			return nil, fmt.Errorf("settlements: %w", err)
		}
		return nil, fmt.Errorf("settlements: empty file")
	}
	sr.line = 1
	text := strings.TrimPrefix(strings.TrimRight(sc.Text(), "\r"), "\uFEFF")
	sr.header = strings.Split(text, "\t")
	sr.index = make(map[string]int, len(sr.header))
	for i, h := range sr.header {
		sr.index[strings.ToLower(strings.TrimSpace(h))] = i
	}
	for _, h := range settlementRequiredHeaders {
		if _, ok := sr.index[h]; !ok {
			return nil, fmt.Errorf("settlements: header lacks column %q", h)
		}
	}
	return sr, nil
}

// Skipped returns lines that were not ingested (blank lines).
func (sr *SettlementsReader) Skipped() []Skipped { return sr.skipped }

func (sr *SettlementsReader) get(rec []string, name string) string {
	if i, ok := sr.index[name]; ok && i < len(rec) {
		return strings.TrimSpace(rec[i])
	}
	return ""
}

// Next returns the next row or io.EOF.
func (sr *SettlementsReader) Next() (*Row, error) {
	for sr.sc.Scan() {
		sr.line++
		text := strings.TrimRight(sr.sc.Text(), "\r")
		if strings.TrimSpace(text) == "" {
			sr.skipped = append(sr.skipped, Skipped{LineNo: sr.line, Reason: "blank line"})
			continue
		}
		rec := strings.Split(text, "\t")
		row := &Row{Source: SourceSettlements, LineNo: sr.line, Kind: KindData, RawLine: text,
			Raw: make(map[string]string, len(sr.header))}
		for i, h := range sr.header {
			if i < len(rec) {
				row.Raw[h] = rec[i]
			}
		}
		if len(rec) != len(sr.header) {
			row.Errors = append(row.Errors, FieldError{Message: fmt.Sprintf("expected %d columns, got %d", len(sr.header), len(rec))})
		}
		sr.fill(row, rec)
		if len(row.Errors) > 0 {
			row.Kind = KindError
		}
		return row, nil
	}
	if err := sr.sc.Err(); err != nil {
		return nil, fmt.Errorf("settlements line %d: %w", sr.line+1, err)
	}
	return nil, io.EOF
}

func (sr *SettlementsReader) fill(row *Row, rec []string) {
	fail := func(field, raw string, err error) {
		row.Errors = append(row.Errors, FieldError{Field: field, RawValue: raw, Message: err.Error()})
	}
	row.SettlementID = sr.get(rec, "settlement-id")
	row.TxnType = sr.get(rec, "transaction-type")
	row.OrderID = sr.get(rec, "order-id")
	row.MerchantOrderID = sr.get(rec, "merchant-order-id")
	row.AdjustmentID = sr.get(rec, "adjustment-id")
	row.ShipmentID = sr.get(rec, "shipment-id")
	row.Marketplace = sr.get(rec, "marketplace-name")
	row.AmountType = sr.get(rec, "amount-type")
	row.Description = sr.get(rec, "amount-description")
	row.Fulfillment = sr.get(rec, "fulfillment-id")
	row.SKU = sr.get(rec, "sku")
	row.Currency = sr.get(rec, "currency")

	var err error
	parseTime := func(name string) *time.Time {
		v := sr.get(rec, name)
		t, err := ParseSettlementTime(v)
		if err != nil {
			fail(name, v, err)
		}
		return t
	}
	row.PostedDate = parseTime("posted-date")
	row.PostedAt = parseTime("posted-date-time")
	if row.PostedAt == nil {
		row.PostedAt = row.PostedDate
	}
	qty := sr.get(rec, "quantity-purchased")
	if row.Quantity, err = ParseQuantity(qty); err != nil {
		fail("quantity-purchased", qty, err)
	}

	totalRaw := sr.get(rec, "total-amount")
	amountRaw := sr.get(rec, "amount")
	if totalRaw != "" && row.TxnType == "" && amountRaw == "" {
		row.Kind = KindSettlementHeader
		row.SettlementStart = parseTime("settlement-start-date")
		row.SettlementEnd = parseTime("settlement-end-date")
		row.DepositDate = parseTime("deposit-date")
		total, err := ParseAmount(totalRaw)
		if err != nil {
			fail("total-amount", totalRaw, err)
		} else {
			row.TotalAmount = &total
		}
		return
	}
	if row.Amount, err = ParseAmount(amountRaw); err != nil {
		fail("amount", amountRaw, err)
	}
}
