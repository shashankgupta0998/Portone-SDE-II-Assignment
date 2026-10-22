// Package ingest streams the Amazon Payments CSV and Settlement TXT, exactly as provided,
// into typed rows that keep their raw payload and physical line number.
package ingest

import (
	"time"

	"github.com/shopspring/decimal"
)

// Sources.
const (
	SourcePayments    = "payments"
	SourceSettlements = "settlements"
)

// Row kinds.
const (
	KindData             = "data"
	KindSettlementHeader = "settlement_header"
	KindError            = "error" // a field failed to parse; stored with its raw payload, not mapped
)

// PaymentAmountFields are the payments amount columns, named as the payment config's amount_field.
var PaymentAmountFields = []string{
	"product_sales", "shipping_credits", "gift_wrap_credits", "promotional_rebates",
	"sales_tax_collected", "low_value_goods", "selling_fees", "fba_fees",
	"other_transaction_fees", "other", "total",
}

// FieldError describes one unparseable cell.
type FieldError struct {
	Field    string
	RawValue string
	Message  string
}

// Row is one physical record of either source file.
type Row struct {
	Source string
	LineNo int // physical 1-based line in the original file
	Kind   string

	SettlementID    string
	TxnType         string // payments "type" / settlements "transaction-type"
	AmountType      string // settlements only
	Description     string // payments "description" / settlements "amount-description"
	OrderID         string
	MerchantOrderID string
	AdjustmentID    string
	ShipmentID      string
	SKU             string
	Quantity        *int32
	Marketplace     string
	Fulfillment     string
	TxnStatus       string // payments only
	PostedAt        *time.Time
	ReleaseAt       *time.Time
	PostedDate      *time.Time // settlements "posted-date" (00:00 UTC)
	RefDate         *time.Time // set by the mapping stage

	Amount decimal.Decimal            // settlements
	Cols   map[string]decimal.Decimal // payments: amount_field -> value

	// settlement header row
	SettlementStart, SettlementEnd, DepositDate *time.Time
	TotalAmount                                 *decimal.Decimal
	Currency                                    string

	Raw     map[string]string
	RawLine string
	Errors  []FieldError
}

// Reader yields rows until io.EOF.
type Reader interface {
	Next() (*Row, error)
	Skipped() []Skipped
}

// Skipped records a physical line that was intentionally not ingested as a row.
type Skipped struct {
	LineNo int
	Reason string
}
