package mapping

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"amazonrecon/internal/ingest"
)

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func TestNormalize(t *testing.T) {
	cases := map[string]string{
		"Order":             "ORDER",
		"  Service fee ":    "SERVICE_FEE",
		"other-transaction": "OTHER-TRANSACTION",
		"FBA Inventory Reimbursement - Customer Return":           "FBA_INVENTORY_REIMBURSEMENT_-_CUSTOMER_RETURN",
		"Damaged:Warehouse":                                       "DAMAGED:WAREHOUSE",
		"Fulfilment by Amazon (FBA) Inventory Storage Fee":        "FULFILMENT_BY_AMAZON_(FBA)_INVENTORY_STORAGE_FEE",
		"Multi-Channel Fulfilment Inventory Reimbursement – Lost": "MULTI-CHANNEL_FULFILMENT_INVENTORY_REIMBURSEMENT_-_LOST",
		"a \t  b": "A_B",
		"any":     Wildcard,
	}
	for in, want := range cases {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
	// config key and source value converge on the same key
	if Normalize("MULTI-CHANNEL_FULFILMENT_INVENTORY_REIMBURSEMENT_–_LOST") != Normalize("Multi-Channel Fulfilment Inventory Reimbursement - Lost") {
		t.Error("en dash in config key should match hyphen in source")
	}
}

func ts(s string) *time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return &t
}

func TestRefDate(t *testing.T) {
	p := &ingest.Row{Source: ingest.SourcePayments, PostedAt: ts("2026-07-10T04:00:00Z"), ReleaseAt: ts("2026-07-17T21:10:51Z")}
	if got := RefDate(p).Format("2006-01-02"); got != "2026-07-17" {
		t.Errorf("payments uses release date (UTC): %s", got)
	}
	p.ReleaseAt = nil
	if got := RefDate(p).Format("2006-01-02"); got != "2026-07-10" {
		t.Errorf("payments falls back to date/time: %s", got)
	}
	s := &ingest.Row{Source: ingest.SourceSettlements, PostedDate: ts("2026-07-17T00:00:00Z"), PostedAt: ts("2026-07-18T01:00:00Z")}
	if got := RefDate(s).Format("2006-01-02"); got != "2026-07-17" {
		t.Errorf("settlements use posted-date: %s", got)
	}
	if RefDate(&ingest.Row{Source: ingest.SourcePayments}) != nil {
		t.Error("no dates -> nil")
	}
}

func TestTemplateBuild(t *testing.T) {
	r := &ingest.Row{Source: ingest.SourceSettlements, OrderID: "111-1", SKU: "SKU-A", SettlementID: "S1",
		Description: "To account ending with: 334", TxnType: "other-transaction", RefDate: ts("2026-07-17T00:00:00Z")}
	cases := []struct {
		tmpl, want string
		empty      []string
	}{
		{"txn_ref+sku+date", "111-1+SKU-A+2026-07-17", nil},
		{"GENERAL ADJUSTMENT+sku+settlement_id+date", "GENERAL ADJUSTMENT+SKU-A+S1+2026-07-17", nil},
		{"TRANSFER+description+settlement_id+date", "TRANSFER+TO_ACCOUNT_ENDING_WITH:_334+S1+2026-07-17", nil},
		{"ADJUSTMENT_OTHER+settlement_id+date+record_type+settlement_id", "ADJUSTMENT_OTHER+S1+2026-07-17+OTHER-TRANSACTION+S1", nil},
		{"shipment_id+INBOUND_DEFECT_FEE+settlement_id+date", "+INBOUND_DEFECT_FEE+S1+2026-07-17", []string{"shipment_id"}},
	}
	for _, c := range cases {
		got, empty := ParseTemplate(c.tmpl).Build(r)
		if got != c.want || len(empty) != len(c.empty) {
			t.Errorf("%s -> %q (empty %v), want %q (empty %v)", c.tmpl, got, empty, c.want, c.empty)
		}
	}
}

func payRule(id int, typ, desc, field, ref, pos, neg string) Rule {
	return Rule{ID: id, TxnType: typ, Key1: desc, AmountField: field, RecordRef: ref, Positive: pos, Negative: neg}
}

func payRow(typ, desc string, cols map[string]string) *ingest.Row {
	r := &ingest.Row{Source: ingest.SourcePayments, Kind: ingest.KindData, TxnType: typ, Description: desc,
		OrderID: "O1", SKU: "K1", SettlementID: "S1", RefDate: ts("2026-07-17T00:00:00Z"), Cols: map[string]decimal.Decimal{}}
	for _, f := range ingest.PaymentAmountFields {
		r.Cols[f] = decimal.Zero
	}
	for k, v := range cols {
		r.Cols[k] = d(v)
	}
	return r
}

func byField(cs []Component) map[string][]Component {
	m := map[string][]Component{}
	for _, c := range cs {
		m[c.AmountField] = append(m[c.AmountField], c)
	}
	return m
}

func TestEnginePrecedence(t *testing.T) {
	rules := []Rule{
		payRule(1, "SERVICE_FEE", "SUBSCRIPTION", "other", "SERVICE_FEE_SUBSCRIPTION+settlement_id+date", "expenses_amazon_fees", "expenses_amazon_fees"),
		payRule(2, "SERVICE_FEE", "SUBSCRIPTION", "total", "SERVICE_FEE_SUBSCRIPTION+settlement_id+date", "", ""),
		payRule(3, "ORDER", "any", "product_sales", "txn_ref+sku+date", "sales_product_charges", "sales_product_charges"),
		payRule(4, "ORDER", "any", "total", "txn_ref+sku+date", "", ""),
		payRule(5, "", "any", "total", "txn_ref+settlement_id+date", "expenses_amazon_fees", "expenses_amazon_fees"),
		payRule(6, "", "SPECIAL", "total", "SPECIAL+date", "sales_other", "sales_other"),
		payRule(7, "SERVICE_FEE", "any", "total", "SF+date", "expenses_other", "expenses_other"),
	}
	e := NewEngine(ingest.SourcePayments, rules)

	// L1: exact type + exact description beats the type's wildcard rule
	g, lvl := e.Group(payRow("Service fee", "Subscription", nil))
	if lvl != 1 || len(g) != 2 {
		t.Fatalf("L1: level %d, %d rules", lvl, len(g))
	}
	// L2: exact type + any
	if _, lvl := e.Group(payRow("Order", "Some product", nil)); lvl != 2 {
		t.Fatalf("L2: level %d", lvl)
	}
	if _, lvl := e.Group(payRow("Service fee", "Cost of advertising", nil)); lvl != 2 {
		t.Fatalf("L2 for unknown description of a known type: level %d", lvl)
	}
	// L3: fallback + exact description
	if g, lvl := e.Group(payRow("Mystery", "Special", nil)); lvl != 3 || g[0].ID != 6 {
		t.Fatalf("L3: level %d", lvl)
	}
	// L4: fallback + any — only for types with no rule of their own
	if g, lvl := e.Group(payRow("Transfer", "To account ending with: 334", nil)); lvl != 4 || g[0].ID != 5 {
		t.Fatalf("L4: level %d", lvl)
	}
}

func TestEngineMapPayments(t *testing.T) {
	rules := []Rule{
		payRule(1, "ORDER", "any", "product_sales", "txn_ref+sku+date", "sales_product_charges", "sales_product_charges"),
		payRule(2, "ORDER", "any", "total", "txn_ref+sku+date", "", ""),
		payRule(3, "ORDER", "any", "sales_tax_collected", "txn_ref+sku+date", "sales_product_charges", "sales_product_charges"),
		payRule(4, "ORDER", "any", "sales_tax_collected", "txn_ref+sku+date", "sales_shipping", "sales_shipping"),
		payRule(5, "ORDER", "any", "marketplace_withheld_tax", "txn_ref+sku+date", "", ""),
		payRule(6, "", "any", "total", "txn_ref+settlement_id+date", "expenses_amazon_fees", "expenses_amazon_fees"),
		payRule(7, "", "any", "fba_fees", "txn_ref+settlement_id+date", "expenses_fba_fees", "expenses_fba_fees"),
	}
	e := NewEngine(ingest.SourcePayments, rules)
	row := payRow("Order", "Widget", map[string]string{"product_sales": "20", "sales_tax_collected": "2", "fba_fees": "-5", "total": "17"})
	cs := byField(e.Map(row))

	if c := cs["product_sales"][0]; c.Status != StatusRouted || c.SummaryField != "sales_product_charges" ||
		c.RecordRef != "O1+K1+2026-07-17" || c.RuleID != 1 || c.Level != 2 {
		t.Errorf("product_sales = %+v", c)
	}
	if c := cs["total"][0]; c.Status != StatusNotSummarised || c.SummaryField != "" {
		t.Errorf("total with empty bucket = %+v", c)
	}
	// duplicate rules on one column are both applied as written
	if tax := cs["sales_tax_collected"]; len(tax) != 2 || tax[0].SummaryField == tax[1].SummaryField {
		t.Errorf("duplicate rules: %+v", tax)
	}
	// non-zero column with no rule in the matched group is flagged, not sent to the fallback
	if fba := cs["fba_fees"]; len(fba) != 1 || fba[0].Status != StatusUnruled || fba[0].HasRef {
		t.Errorf("fba_fees = %+v", fba)
	}
	// zero columns ruled in the group are kept with status zero
	if c := cs["shipping_credits"]; len(c) != 0 {
		t.Errorf("unruled zero column should produce nothing: %+v", c)
	}
	// rule on a column the file does not have never fires
	if c := cs["marketplace_withheld_tax"]; len(c) != 0 {
		t.Errorf("absent column: %+v", c)
	}

	// no group at all -> unmapped components for each non-zero column
	e2 := NewEngine(ingest.SourcePayments, rules[:2])
	um := e2.Map(payRow("Transfer", "x", map[string]string{"other": "-50", "total": "-50"}))
	if len(um) != 2 || um[0].Status != StatusUnmapped || um[0].HasRef {
		t.Errorf("unmapped = %+v", um)
	}
}

func TestSignRouting(t *testing.T) {
	r := &Rule{Positive: "sales_inventory_reimbursements", Negative: "expenses_reversed_reimbursements"}
	if f, s := Route(r, d("12.50")); f != "sales_inventory_reimbursements" || s != StatusRouted {
		t.Errorf("positive: %s %s", f, s)
	}
	if f, s := Route(r, d("-1")); f != "expenses_reversed_reimbursements" || s != StatusRouted {
		t.Errorf("negative: %s %s", f, s)
	}
	if f, s := Route(r, decimal.Zero); f != "" || s != StatusZero {
		t.Errorf("zero: %s %s", f, s)
	}
	half := &Rule{Positive: "sales_other"}
	if f, s := Route(half, d("-3")); f != "" || s != StatusNotSummarised {
		t.Errorf("empty negative bucket: %s %s", f, s)
	}
}

func TestEngineMapSettlements(t *testing.T) {
	rules := []Rule{
		{ID: 1, TxnType: "ORDER", Key1: "ITEMPRICE", Key2: "PRINCIPAL", AmountField: SettlementAmountField, RecordRef: "txn_ref+sku+date", Positive: "sales_product_charges", Negative: "sales_product_charges"},
		{ID: 2, TxnType: "REFUND", Key1: "ITEMWITHHELDTAX", Key2: "any", AmountField: SettlementAmountField, RecordRef: "txn_ref+sku+date", Positive: "refunded_expenses", Negative: "refunded_expenses"},
		{ID: 3, TxnType: "OTHER-TRANSACTION", Key1: "OTHER-TRANSACTION", Key2: "SUBSCRIPTION_FEE", AmountField: SettlementAmountField, RecordRef: "SERVICE_FEE_SUBSCRIPTION+settlement_id+date", Positive: "expenses_amazon_fees", Negative: "expenses_amazon_fees"},
	}
	e := NewEngine(ingest.SourceSettlements, rules)
	mk := func(typ, at, ad, amt string) *ingest.Row {
		return &ingest.Row{Source: ingest.SourceSettlements, Kind: ingest.KindData, TxnType: typ, AmountType: at, Description: ad,
			Amount: d(amt), OrderID: "O1", SKU: "K1", SettlementID: "S1", RefDate: ts("2026-07-17T00:00:00Z")}
	}
	if cs := e.Map(mk("Order", "ItemPrice", "Principal", "19.67")); len(cs) != 1 || cs[0].Level != 1 || cs[0].SummaryField != "sales_product_charges" {
		t.Errorf("exact: %+v", cs)
	}
	if cs := e.Map(mk("Refund", "ItemWithheldTax", "LowValueGoodsTax-Principal", "1")); len(cs) != 1 || cs[0].Level != 2 {
		t.Errorf("amount_description any: %+v", cs)
	}
	if cs := e.Map(mk("other-transaction", "other-transaction", "Subscription Fee", "-16.97")); cs[0].RecordRef != "SERVICE_FEE_SUBSCRIPTION+S1+2026-07-17" {
		t.Errorf("normalised match: %+v", cs)
	}
	if cs := e.Map(mk("Order", "ItemPrice", "Tax", "1")); len(cs) != 1 || cs[0].Status != StatusUnmapped {
		t.Errorf("unmapped: %+v", cs)
	}
	if cs := e.Map(&ingest.Row{Source: ingest.SourceSettlements, Kind: ingest.KindSettlementHeader}); cs != nil {
		t.Errorf("header rows are not mapped")
	}
}
