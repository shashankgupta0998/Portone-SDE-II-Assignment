package mapping

import (
	"strings"

	"amazonrecon/internal/ingest"
)

// Field tokens a record_ref template may reference. Any other token is a literal.
const (
	TokTxnRef          = "txn_ref"
	TokSKU             = "sku"
	TokDate            = "date"
	TokSettlementID    = "settlement_id"
	TokDescription     = "description"
	TokShipmentID      = "shipment_id"
	TokMerchantOrderID = "merchant_order_id"
	TokRecordType      = "record_type"
)

var fieldTokens = map[string]bool{
	TokTxnRef: true, TokSKU: true, TokDate: true, TokSettlementID: true, TokDescription: true,
	TokShipmentID: true, TokMerchantOrderID: true, TokRecordType: true,
}

// Token is one "+"-separated element of a record_ref template.
type Token struct {
	Field   string // set for field tokens
	Literal string // set for literal tokens (kept verbatim, e.g. "GENERAL ADJUSTMENT")
}

// Template is a parsed record_ref.
type Template []Token

// ParseTemplate splits a record_ref on "+" and classifies each token.
func ParseTemplate(s string) Template {
	parts := strings.Split(s, "+")
	t := make(Template, 0, len(parts))
	for _, p := range parts {
		if fieldTokens[p] {
			t = append(t, Token{Field: p})
		} else {
			t = append(t, Token{Literal: p})
		}
	}
	return t
}

// FieldValue resolves a field token against a row. Unknown fields resolve empty.
func FieldValue(r *ingest.Row, field string) string {
	switch field {
	case TokTxnRef:
		return r.OrderID
	case TokSKU:
		return r.SKU
	case TokDate:
		if r.RefDate == nil {
			return ""
		}
		return r.RefDate.Format("2006-01-02")
	case TokSettlementID:
		return r.SettlementID
	case TokDescription:
		return Normalize(r.Description)
	case TokShipmentID:
		return r.ShipmentID
	case TokMerchantOrderID:
		return r.MerchantOrderID
	case TokRecordType:
		return Normalize(r.TxnType)
	}
	return ""
}

// Build renders the template for a row. Field tokens that resolve empty stay empty (the key
// still builds) and are reported in empty so they can be surfaced; no other id is substituted.
func (t Template) Build(r *ingest.Row) (ref string, empty []string) {
	var b strings.Builder
	for i, tok := range t {
		if i > 0 {
			b.WriteByte('+')
		}
		if tok.Field == "" {
			b.WriteString(tok.Literal)
			continue
		}
		v := FieldValue(r, tok.Field)
		if v == "" {
			empty = append(empty, tok.Field)
		}
		b.WriteString(v)
	}
	return b.String(), empty
}
