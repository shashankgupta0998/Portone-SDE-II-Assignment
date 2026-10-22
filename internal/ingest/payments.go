package ingest

import (
	"bufio"
	"encoding/csv"
	"fmt"
	"io"
	"strings"

	"github.com/shopspring/decimal"
)

// payments header name -> amount_field used by the payment config.
var paymentAmountHeaders = map[string]string{
	"product sales":             "product_sales",
	"shipping credits":          "shipping_credits",
	"gift wrap credits":         "gift_wrap_credits",
	"promotional rebates":       "promotional_rebates",
	"sales tax collected":       "sales_tax_collected",
	"low value goods":           "low_value_goods",
	"selling fees":              "selling_fees",
	"fulfilment by amazon fees": "fba_fees",
	"other transaction fees":    "other_transaction_fees",
	"other":                     "other",
	"total":                     "total",
}

var paymentRequiredHeaders = []string{
	"date/time", "settlement id", "type", "order id", "sku", "description", "total",
}

// lineReader yields logical CSV records as raw text, joining physical lines while a quoted
// field is open, and tracks the physical line number each record starts on.
type lineReader struct {
	r    *bufio.Reader
	line int
	bom  bool
}

func (lr *lineReader) next() (text string, start int, err error) {
	var b strings.Builder
	for {
		s, err := lr.r.ReadString('\n')
		if err != nil && (err != io.EOF || s == "") {
			if b.Len() > 0 {
				return "", start, fmt.Errorf("line %d: unterminated quoted field at end of file", start)
			}
			return "", 0, err
		}
		lr.line++
		if b.Len() == 0 {
			start = lr.line
			if lr.line == 1 {
				if t := strings.TrimPrefix(s, "\uFEFF"); t != s {
					lr.bom = true
					s = t
				}
			}
		} else {
			b.WriteString("\n")
		}
		b.WriteString(strings.TrimRight(s, "\r\n"))
		if strings.Count(b.String(), `"`)%2 == 0 {
			return b.String(), start, nil
		}
		if err == io.EOF {
			return "", start, fmt.Errorf("line %d: unterminated quoted field at end of file", start)
		}
	}
}

func parseCSVLine(text string) ([]string, error) {
	r := csv.NewReader(strings.NewReader(text))
	r.FieldsPerRecord = -1
	r.LazyQuotes = false
	rec, err := r.Read()
	if err == io.EOF {
		return []string{}, nil
	}
	return rec, err
}

// PaymentsReader streams the Amazon Payments ("date range") CSV report. The disclaimer preamble
// before the header is detected by content (first record whose first cell is "date/time").
type PaymentsReader struct {
	lr      lineReader
	header  []string
	index   map[string]int // lower-cased header -> column
	skipped []Skipped
}

// NewPaymentsReader reads up to and including the header row.
func NewPaymentsReader(r io.Reader) (*PaymentsReader, error) {
	pr := &PaymentsReader{lr: lineReader{r: bufio.NewReaderSize(r, 1<<20)}}
	for {
		text, line, err := pr.lr.next()
		if err == io.EOF {
			return nil, fmt.Errorf("payments: header row (\"date/time\", ...) not found")
		}
		if err != nil {
			return nil, fmt.Errorf("payments: %w", err)
		}
		rec, err := parseCSVLine(text)
		if err == nil && len(rec) > 1 && strings.EqualFold(strings.TrimSpace(rec[0]), "date/time") {
			pr.header = rec
			pr.index = make(map[string]int, len(rec))
			for i, h := range rec {
				pr.index[strings.ToLower(strings.TrimSpace(h))] = i
			}
			for _, h := range paymentRequiredHeaders {
				if _, ok := pr.index[h]; !ok {
					return nil, fmt.Errorf("payments: header on line %d lacks column %q", line, h)
				}
			}
			return pr, nil
		}
		pr.skipped = append(pr.skipped, Skipped{LineNo: line, Reason: "preamble line before the payments header"})
	}
}

// Skipped returns the preamble/blank lines that were not ingested.
func (pr *PaymentsReader) Skipped() []Skipped { return pr.skipped }

func (pr *PaymentsReader) get(rec []string, name string) string {
	if i, ok := pr.index[name]; ok && i < len(rec) {
		return strings.TrimSpace(rec[i])
	}
	return ""
}

// Next returns the next data row or io.EOF. Unparseable cells do not stop the stream: the row
// comes back with Kind=KindError and its Errors populated.
func (pr *PaymentsReader) Next() (*Row, error) {
	for {
		text, line, err := pr.lr.next()
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(text) == "" {
			pr.skipped = append(pr.skipped, Skipped{LineNo: line, Reason: "blank line"})
			continue
		}
		row := &Row{Source: SourcePayments, LineNo: line, Kind: KindData, RawLine: text}
		rec, err := parseCSVLine(text)
		if err != nil {
			row.Kind = KindError
			row.Raw = map[string]string{"_line": text}
			row.Errors = append(row.Errors, FieldError{Message: "malformed CSV: " + err.Error()})
			return row, nil
		}
		row.Raw = make(map[string]string, len(pr.header))
		for i, h := range pr.header {
			if i < len(rec) {
				row.Raw[h] = rec[i]
			}
		}
		if len(rec) != len(pr.header) {
			row.Errors = append(row.Errors, FieldError{Message: fmt.Sprintf("expected %d columns, got %d", len(pr.header), len(rec))})
		}
		pr.fill(row, rec)
		if len(row.Errors) > 0 {
			row.Kind = KindError
		}
		return row, nil
	}
}

func (pr *PaymentsReader) fill(row *Row, rec []string) {
	fail := func(field, raw string, err error) {
		row.Errors = append(row.Errors, FieldError{Field: field, RawValue: raw, Message: err.Error()})
	}
	row.SettlementID = pr.get(rec, "settlement id")
	row.TxnType = pr.get(rec, "type")
	row.OrderID = pr.get(rec, "order id")
	row.SKU = pr.get(rec, "sku")
	row.Description = pr.get(rec, "description")
	row.Marketplace = pr.get(rec, "marketplace")
	row.Fulfillment = pr.get(rec, "fulfilment")
	row.TxnStatus = pr.get(rec, "transaction status")

	var err error
	qty := pr.get(rec, "quantity")
	if row.Quantity, err = ParseQuantity(qty); err != nil {
		fail("quantity", qty, err)
	}
	posted := pr.get(rec, "date/time")
	if row.PostedAt, err = ParsePaymentTime(posted); err != nil {
		fail("date/time", posted, err)
	}
	release := pr.get(rec, "transaction release date")
	if row.ReleaseAt, err = ParsePaymentTime(release); err != nil {
		fail("Transaction Release Date", release, err)
	}
	row.Cols = make(map[string]decimal.Decimal, len(paymentAmountHeaders))
	for header, field := range paymentAmountHeaders {
		if _, ok := pr.index[header]; !ok {
			continue // column absent from this file: rules on it simply never fire
		}
		v := pr.get(rec, header)
		d, err := ParseAmount(v)
		if err != nil {
			fail(header, v, err)
			continue
		}
		row.Cols[field] = d
	}
}
