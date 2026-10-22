package report

import (
	"fmt"
	"sort"
	"strings"

	"github.com/shopspring/decimal"
	"github.com/xuri/excelize/v2"
)

var sections = []string{"Sales", "Refunds", "Expenses"}

const paidToAmazon = "Paid To Amazon"

func cell(col string, row int) string { return fmt.Sprintf("%s%d", col, row) }

func money(d decimal.Decimal) float64 { return d.InexactFloat64() }

// writeSummary reproduces the layout of amazon_sample_output_report.xlsx (rows 1-36:
// section header rows carry their subtotal, blank merged rows between sections, Paid To Amazon
// on row 32) and appends a tie-out block below it.
func writeSummary(f *excelize.File, st styles, run RunInfo, d *summaryData) error {
	const sh = SheetSummary
	set := func(c string, v any, style int) error {
		if err := f.SetCellValue(sh, c, v); err != nil {
			return err
		}
		if style != 0 {
			return f.SetCellStyle(sh, c, c, style)
		}
		return nil
	}
	merge := func(row int) error { return f.MergeCell(sh, cell("A", row), cell("E", row)) }
	values := func(row int, p, s decimal.Decimal, style int) error {
		for _, x := range []struct {
			col string
			v   decimal.Decimal
		}{{"C", p}, {"D", s}, {"E", p.Sub(s)}} {
			if err := set(cell(x.col, row), money(x.v), style); err != nil {
				return err
			}
		}
		return nil
	}

	deferredCol := func(row int, v decimal.Decimal, style int) error { return set(cell("F", row), money(v), style) }
	for _, h := range []struct{ c, v string }{{"C1", "Payments"}, {"D1", "Settlements"}, {"E1", "Payments - Settlements"},
		{"F1", "Memo: Payments Deferred (not in C)"}} {
		if err := set(h.c, h.v, st.bold); err != nil {
			return err
		}
	}
	if err := merge(3); err != nil {
		return err
	}

	pay, settle := d.routed["payments"], d.routed["settlements"]
	totalP, totalS := decimal.Zero, decimal.Zero
	row := 4
	for _, sec := range sections {
		headerRow := row
		row++
		subP, subS, subD := decimal.Zero, decimal.Zero, decimal.Zero
		for _, l := range d.lines {
			if l.Section != sec {
				continue
			}
			if err := set(cell("B", row), l.Label, st.indent4); err != nil {
				return err
			}
			if err := values(row, pay[l.Field], settle[l.Field], st.money); err != nil {
				return err
			}
			if err := deferredCol(row, d.deferredBy[l.Field], st.money); err != nil {
				return err
			}
			subP, subS, subD = subP.Add(pay[l.Field]), subS.Add(settle[l.Field]), subD.Add(d.deferredBy[l.Field])
			row++
		}
		if err := set(cell("B", headerRow), sec, st.indent2Bold); err != nil {
			return err
		}
		if err := values(headerRow, subP, subS, st.moneyBold); err != nil {
			return err
		}
		if err := deferredCol(headerRow, subD, st.moneyBold); err != nil {
			return err
		}
		totalP, totalS = totalP.Add(subP), totalS.Add(subS)
		if err := merge(row); err != nil {
			return err
		}
		row++
	}
	// Sample rows 30-36: spacer, merged, Paid To Amazon (row 32), merged, spacer, merged, merged.
	row++ // row 30 is a spacer; 31 is merged
	if err := merge(row); err != nil {
		return err
	}
	row++ // row 32
	for _, l := range d.lines {
		if l.Section != paidToAmazon {
			continue
		}
		if err := set(cell("B", row), l.Label, st.indent4); err != nil {
			return err
		}
		if err := values(row, pay[l.Field], settle[l.Field], st.money); err != nil {
			return err
		}
		if err := deferredCol(row, d.deferredBy[l.Field], st.money); err != nil {
			return err
		}
		totalP, totalS = totalP.Add(pay[l.Field]), totalS.Add(settle[l.Field])
		row++
	}
	for _, r := range []int{row, row + 2, row + 3} {
		if err := merge(r); err != nil {
			return err
		}
	}
	row += 5

	// Tie-out block: everything below is a database sum, labelled with what it is.
	onSheet := map[string]bool{}
	for _, l := range d.lines {
		onSheet[l.Field] = true
	}
	var extra []string
	for _, src := range []string{"payments", "settlements"} {
		for f := range d.routed[src] {
			if !onSheet[f] && !contains(extra, f) {
				extra = append(extra, f)
			}
		}
	}
	sort.Strings(extra)

	if err := set(cell("B", row), "Tie-out to source totals and Amazon's settlement total", st.bold); err != nil {
		return err
	}
	row++
	if err := set(cell("B", row), "Net of Summary lines (Sales + Refunds + Expenses + Paid To Amazon)", 0); err != nil {
		return err
	}
	if err := values(row, totalP, totalS, st.moneyBold); err != nil {
		return err
	}
	netLinesRow := row
	routedP, routedS := totalP, totalS
	row++
	for _, fld := range extra {
		if err := set(cell("B", row), "Routed to a field with no Summary line: "+fld, 0); err != nil {
			return err
		}
		if err := values(row, pay[fld], settle[fld], st.money); err != nil {
			return err
		}
		routedP, routedS = routedP.Add(pay[fld]), routedS.Add(settle[fld])
		row++
	}
	if err := set(cell("B", row), "Not routed to any summary field (derived: net amount - routed; itemised on Exceptions)", 0); err != nil {
		return err
	}
	if err := values(row, d.net.payments.Sub(routedP), d.net.settlements.Sub(routedS), st.money); err != nil {
		return err
	}
	row++
	if err := set(cell("B", row), "Net amount per source (payments: sum of total; settlements: sum of amount)", st.bold); err != nil {
		return err
	}
	if err := values(row, d.net.payments, d.net.settlements, st.moneyBold); err != nil {
		return err
	}
	row++
	if err := set(cell("B", row), "Amazon settlement total-amount (settlement header row)", 0); err != nil {
		return err
	}
	if err := set(cell("C", row), money(d.headerTotal), st.money); err != nil {
		return err
	}
	if err := set(cell("D", row), money(d.headerTotal), st.money); err != nil {
		return err
	}
	row++
	if err := set(cell("B", row), fmt.Sprintf("Net of Summary lines (row %d) - Amazon total-amount", netLinesRow), st.bold); err != nil {
		return err
	}
	if err := values(row, totalP.Sub(d.headerTotal), totalS.Sub(d.headerTotal), st.moneyBold); err != nil {
		return err
	}
	row++
	if err := set(cell("B", row), "Net amount per source - Amazon total-amount", st.bold); err != nil {
		return err
	}
	if err := values(row, d.net.payments.Sub(d.headerTotal), d.net.settlements.Sub(d.headerTotal), st.moneyBold); err != nil {
		return err
	}
	row += 2
	memos := []struct {
		label string
		v     decimal.Decimal
	}{
		{"Memo: in-scope payment rows with Transaction status = Deferred (sum of total; not released into this settlement, excluded from the Payments column, per line in column F)", d.deferred},
		{fmt.Sprintf("Memo: payment rows of other settlement ids, %d rows (sum of total, excluded; see Out of Scope Payments)", d.outOfScopeN), d.outOfScope},
	}
	for _, m := range memos {
		if err := set(cell("B", row), m.label, 0); err != nil {
			return err
		}
		if err := set(cell("C", row), money(m.v), st.money); err != nil {
			return err
		}
		row++
	}
	row++
	note := fmt.Sprintf("Run %d (%s). Scope: settlement %s. Payments column = payment records of this settlement id with Transaction status Released; "+
		"Settlements column = settlement records only. "+
		"Values are sums of mapped components streamed during ingestion; no manual adjustments.",
		run.ID, run.Label, strings.Join(run.Scope, ", "))
	if err := set(cell("B", row), note, st.note); err != nil {
		return err
	}

	if err := f.SetColWidth(sh, "A", "A", 4); err != nil {
		return err
	}
	if err := f.SetColWidth(sh, "B", "B", 70); err != nil {
		return err
	}
	return f.SetColWidth(sh, "C", "F", 22)
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
