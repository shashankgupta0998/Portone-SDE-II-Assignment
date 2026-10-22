package mapping

import (
	"strings"

	"github.com/shopspring/decimal"

	"amazonrecon/internal/ingest"
)

// Component statuses.
const (
	StatusRouted        = "routed"         // amount went to a summary field
	StatusNotSummarised = "not_summarised" // rule matched, bucket for this sign is empty
	StatusZero          = "zero"           // rule matched, amount is 0 (no sign, no bucket)
	StatusUnmapped      = "unmapped"       // no rule group matched the row at all
	StatusUnruled       = "unruled_column" // payments: non-zero column with no rule in the matched group
)

// SettlementAmountField is the amount_field recorded for settlement components.
const SettlementAmountField = "amount"

// Rule is one row of either mapping config, as stored in the database.
type Rule struct {
	ID          int
	SrcLine     *int
	TxnType     string // "" = fallback / catch-all
	Key1        string // payments: description; settlements: amount_type
	Key2        string // settlements: amount_description; payments: ""
	AmountField string // payments: amount column; settlements: SettlementAmountField
	RecordRef   string
	Positive    string // to_summary_field_when_positive_amount
	Negative    string // to_summary_field_when_negative_amount

	template Template
}

// Component is one amount of one source row, as produced by one rule (or by no rule).
type Component struct {
	RuleID       int // 0 = no rule
	Level        int // 1..4, 0 = no rule
	AmountField  string
	Amount       decimal.Decimal
	SummaryField string
	RecordRef    string
	HasRef       bool
	Status       string
	EmptyTokens  []string
}

// Engine selects and applies rules for one source.
type Engine struct {
	source string
	rules  []*Rule
	groups map[string][]*Rule
}

func groupKey(parts ...string) string { return strings.Join(parts, "\x1f") }

// NewEngine indexes rules by their normalised keys. Rule order (by id) is kept inside a group.
func NewEngine(source string, rules []Rule) *Engine {
	e := &Engine{source: source, groups: make(map[string][]*Rule)}
	for i := range rules {
		r := rules[i]
		r.template = ParseTemplate(r.RecordRef)
		e.rules = append(e.rules, &r)
		var k string
		if source == ingest.SourcePayments {
			k = groupKey(Normalize(r.TxnType), Normalize(r.Key1))
		} else {
			k = groupKey(Normalize(r.TxnType), Normalize(r.Key1), Normalize(r.Key2))
		}
		e.groups[k] = append(e.groups[k], &r)
	}
	return e
}

// Rules returns the indexed rules.
func (e *Engine) Rules() []*Rule { return e.rules }

// Group returns the rule group for a row and its precedence level:
//
//	1 = exact transaction_type + exact description
//	2 = exact transaction_type + "any"
//	3 = fallback (empty transaction_type) + exact description
//	4 = fallback + "any"
//
// For settlements "description" means (amount_type exact, amount_description exact|any).
// The most specific level with at least one rule wins; nil, 0 when nothing matches.
func (e *Engine) Group(r *ingest.Row) ([]*Rule, int) {
	t := Normalize(r.TxnType)
	d := Normalize(r.Description)
	var keys []string
	if e.source == ingest.SourcePayments {
		keys = []string{groupKey(t, d), groupKey(t, Wildcard), groupKey("", d), groupKey("", Wildcard)}
	} else {
		at := Normalize(r.AmountType)
		keys = []string{groupKey(t, at, d), groupKey(t, at, Wildcard), groupKey("", at, d), groupKey("", at, Wildcard)}
	}
	for i, k := range keys {
		if g := e.groups[k]; len(g) > 0 {
			return g, i + 1
		}
	}
	return nil, 0
}

// Route picks the summary field for an amount by its sign.
func Route(r *Rule, amount decimal.Decimal) (field, status string) {
	switch amount.Sign() {
	case 0:
		return "", StatusZero
	case 1:
		field = r.Positive
	default:
		field = r.Negative
	}
	if strings.TrimSpace(field) == "" {
		return "", StatusNotSummarised
	}
	return strings.TrimSpace(field), StatusRouted
}

// Map turns a data row into components. Every rule of the selected group is applied:
// for payments to its amount_field column, for settlements to the row's single amount.
// Two rules of one group on the same column are both applied (the config says so; the
// double routing is surfaced by diagnostics rather than silently de-duplicated here).
func (e *Engine) Map(r *ingest.Row) []Component {
	if r.Kind != ingest.KindData {
		return nil
	}
	group, level := e.Group(r)
	if e.source == ingest.SourceSettlements {
		if group == nil {
			return []Component{{AmountField: SettlementAmountField, Amount: r.Amount, Status: StatusUnmapped}}
		}
		out := make([]Component, 0, len(group))
		for _, rule := range group {
			out = append(out, e.apply(rule, level, r, SettlementAmountField, r.Amount))
		}
		return out
	}

	if group == nil {
		var out []Component
		for _, f := range ingest.PaymentAmountFields {
			if v, ok := r.Cols[f]; ok && !v.IsZero() {
				out = append(out, Component{AmountField: f, Amount: v, Status: StatusUnmapped})
			}
		}
		if len(out) == 0 {
			out = append(out, Component{AmountField: "total", Amount: r.Cols["total"], Status: StatusUnmapped})
		}
		return out
	}
	out := make([]Component, 0, len(group)+1)
	ruled := make(map[string]bool, len(group))
	for _, rule := range group {
		v, ok := r.Cols[rule.AmountField]
		if !ok {
			continue // column not present in this file (e.g. marketplace_withheld_tax)
		}
		ruled[rule.AmountField] = true
		out = append(out, e.apply(rule, level, r, rule.AmountField, v))
	}
	for _, f := range ingest.PaymentAmountFields {
		if v, ok := r.Cols[f]; ok && !v.IsZero() && !ruled[f] {
			out = append(out, Component{Level: level, AmountField: f, Amount: v, Status: StatusUnruled})
		}
	}
	return out
}

func (e *Engine) apply(rule *Rule, level int, r *ingest.Row, field string, amount decimal.Decimal) Component {
	ref, empty := rule.template.Build(r)
	summaryField, status := Route(rule, amount)
	return Component{
		RuleID: rule.ID, Level: level, AmountField: field, Amount: amount,
		SummaryField: summaryField, RecordRef: ref, HasRef: true, Status: status, EmptyTokens: empty,
	}
}
