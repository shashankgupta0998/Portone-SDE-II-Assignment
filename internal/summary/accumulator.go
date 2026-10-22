// Package summary accumulates the Summary sheet totals while rows are being ingested,
// so the summary exists the moment ingestion finishes (and is cross-checked against SQL later).
package summary

import (
	"sort"

	"github.com/shopspring/decimal"

	"amazonrecon/internal/ingest"
	"amazonrecon/internal/mapping"
)

// Key identifies one streamed total. Settlement id and transaction status are kept so the
// reconciliation scope can be applied after streaming without re-reading rows.
type Key struct {
	Source       string
	SettlementID string
	TxnStatus    string
	SummaryField string
}

// Total is the running sum for a key.
type Total struct {
	Key
	Amount     decimal.Decimal
	Components int
}

// Accumulator is not safe for concurrent use; ingestion is a single stream.
type Accumulator struct {
	totals map[Key]*Total
}

// New returns an empty accumulator.
func New() *Accumulator { return &Accumulator{totals: make(map[Key]*Total)} }

// Add folds the routed components of one row into the totals.
func (a *Accumulator) Add(r *ingest.Row, cs []mapping.Component) {
	for _, c := range cs {
		if c.Status != mapping.StatusRouted {
			continue
		}
		k := Key{Source: r.Source, SettlementID: r.SettlementID, TxnStatus: r.TxnStatus, SummaryField: c.SummaryField}
		t := a.totals[k]
		if t == nil {
			t = &Total{Key: k}
			a.totals[k] = t
		}
		t.Amount = t.Amount.Add(c.Amount)
		t.Components++
	}
}

// Rows returns the totals in a stable order.
func (a *Accumulator) Rows() []Total {
	out := make([]Total, 0, len(a.totals))
	for _, t := range a.totals {
		out = append(out, *t)
	}
	sort.Slice(out, func(i, j int) bool {
		x, y := out[i].Key, out[j].Key
		if x.Source != y.Source {
			return x.Source < y.Source
		}
		if x.SettlementID != y.SettlementID {
			return x.SettlementID < y.SettlementID
		}
		if x.TxnStatus != y.TxnStatus {
			return x.TxnStatus < y.TxnStatus
		}
		return x.SummaryField < y.SummaryField
	})
	return out
}

// Field returns the sum for one source and summary field across the given settlement ids.
func (a *Accumulator) Field(source, field string, scope map[string]bool) decimal.Decimal {
	sum := decimal.Zero
	for k, t := range a.totals {
		if k.Source == source && k.SummaryField == field && scope[k.SettlementID] {
			sum = sum.Add(t.Amount)
		}
	}
	return sum
}
