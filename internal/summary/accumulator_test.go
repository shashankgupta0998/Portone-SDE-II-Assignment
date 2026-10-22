package summary

import (
	"testing"

	"github.com/shopspring/decimal"

	"amazonrecon/internal/ingest"
	"amazonrecon/internal/mapping"
)

func TestAccumulator(t *testing.T) {
	a := New()
	row := &ingest.Row{Source: ingest.SourcePayments, SettlementID: "S1", TxnStatus: "Released"}
	a.Add(row, []mapping.Component{
		{Amount: decimal.RequireFromString("10.10"), SummaryField: "sales_product_charges", Status: mapping.StatusRouted},
		{Amount: decimal.RequireFromString("5"), Status: mapping.StatusNotSummarised},
		{Amount: decimal.RequireFromString("-2.05"), SummaryField: "expenses_amazon_fees", Status: mapping.StatusRouted},
	})
	a.Add(row, []mapping.Component{{Amount: decimal.RequireFromString("0.20"), SummaryField: "sales_product_charges", Status: mapping.StatusRouted}})
	other := &ingest.Row{Source: ingest.SourcePayments, SettlementID: "S0", TxnStatus: "Released"}
	a.Add(other, []mapping.Component{{Amount: decimal.RequireFromString("99"), SummaryField: "sales_product_charges", Status: mapping.StatusRouted}})

	scope := map[string]bool{"S1": true}
	if got := a.Field(ingest.SourcePayments, "sales_product_charges", scope); got.String() != "10.3" {
		t.Errorf("in-scope total = %s", got)
	}
	rows := a.Rows()
	if len(rows) != 3 {
		t.Fatalf("rows = %+v", rows)
	}
	if rows[0].SettlementID != "S0" || rows[2].Components != 2 {
		t.Errorf("order/count: %+v", rows)
	}
}
