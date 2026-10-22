package report

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
	"github.com/xuri/excelize/v2"
)

// Payments raw columns shown in the sample's DS1 block (A..Q), by sample header -> raw header.
// "Date" is the record_ref date; "account type" and "tax collection model" are not in this file.
var paymentCols = []struct{ header, raw string }{
	{"Date", ""}, {"date/time", "date/time"}, {"Transaction Status", "Transaction status"},
	{"Transaction Release Date", "Transaction Release Date"}, {"settlement id", "settlement ID"},
	{"type", "type"}, {"order id", "order ID"}, {"sku", "sku"}, {"description", "description"},
	{"Quantity", "quantity"}, {"marketplace", "marketplace"}, {"account type", ""},
	{"fulfillment", "fulfilment"}, {"order city", "order city"}, {"order state", "order state"},
	{"order postal", "order postal"}, {"tax collection model", ""},
}

// Settlement raw columns shown in the sample's DS2 block (AN..BH).
var settlementCols = []struct{ header, raw string }{
	{"settlement-id", "settlement-id"}, {"settlement-start-date", "settlement-start-date"},
	{"settlement-end-date", "settlement-end-date"}, {"deposit-date", "deposit-date"},
	{"total-amount", "total-amount"}, {"currency", "currency"}, {"transaction-type", "transaction-type"},
	{"order-id", "order-id"}, {"merchant-order-id", "merchant-order-id"}, {"adjustment-id", "adjustment-id"},
	{"shipment-id", "shipment-id"}, {"marketplace-name", "marketplace-name"}, {"fulfillment-id", "fulfillment-id"},
	{"posted-date", "posted-date"}, {"posted-date-time", "posted-date-time"}, {"order-item-code", "order-item-code"},
	{"merchant-order-item-id", "merchant-order-item-id"}, {"merchant-adjustment-item-id", "merchant-adjustment-item-id"},
	{"sku", "sku"}, {"quantity", "quantity-purchased"}, {"promotion-id", "promotion-id"},
}

// settlement header fields copied onto every DS2 row from the settlement's header row.
var settlementHeaderFields = map[string]bool{
	"settlement-start-date": true, "settlement-end-date": true, "deposit-date": true, "total-amount": true, "currency": true,
}

var auditHeaders = []string{
	"record_ref", "DS1 rows", "DS1 source lines", "DS2 rows", "DS2 source lines",
	"DS2 amount-type / amount-description", "DS1 rule ids", "DS2 rule ids",
	"DS1 fields not on Summary", "DS2 fields not on Summary", "Why not reconciled",
}

var statusLabels = map[string]string{
	"reconciled":              "Reconciled",
	"unreconciled_payment":    "Unreconciled Payment",
	"unreconciled_settlement": "Unreconciled Settlement",
	"out_of_scope_payment":    "Out of scope (other settlement)",
}

const maxCell = 32000 // Excel's hard limit is 32,767 characters

type bucketMap map[string]map[string]decimal.Decimal // record_ref -> field -> amount

func loadBuckets(ctx context.Context, pool *pgxpool.Pool, runID int64) (map[string]bucketMap, error) {
	out := map[string]bucketMap{}
	rows, err := pool.Query(ctx, `SELECT side, record_ref, summary_field, amount::text FROM recon_record_buckets WHERE run_id = $1`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var side, ref, field, amt string
		if err := rows.Scan(&side, &ref, &field, &amt); err != nil {
			return nil, err
		}
		if out[side] == nil {
			out[side] = bucketMap{}
		}
		if out[side][ref] == nil {
			out[side][ref] = map[string]decimal.Decimal{}
		}
		out[side][ref][field] = dec(amt)
	}
	return out, rows.Err()
}

// writeConsolidated writes one row per record_ref with the payment side (DS1) and settlement
// side (DS2) next to each other, then the reconciliation status and DS1-DS2 differences, in the
// sample's column layout (A..DA), followed by audit columns (record_ref, source lines, rules).
// Order: reconciled, unreconciled payments, unreconciled settlements.
func writeConsolidated(ctx context.Context, pool *pgxpool.Pool, f *excelize.File, st styles, sheet string,
	run RunInfo, d *summaryData, outOfScope bool) error {
	statuses := []string{"reconciled", "unreconciled_payment", "unreconciled_settlement"}
	paySide := "payments"
	if outOfScope {
		statuses = []string{"out_of_scope_payment"}
		paySide = "out_of_scope_payments"
	}
	buckets, err := loadBuckets(ctx, pool, run.ID)
	if err != nil {
		return err
	}
	sw, err := f.NewStreamWriter(sheet)
	if err != nil {
		return err
	}
	fields := d.consolidated
	nb := len(fields)
	onSheet := map[string]bool{}
	for _, l := range fields {
		onSheet[l.Field] = true
	}

	// Column widths must be set before rows are written.
	total := len(paymentCols) + 1 + nb + len(settlementCols) + 1 + nb + 2 + nb + len(auditHeaders)
	if err := sw.SetColWidth(1, total, 16); err != nil {
		return err
	}

	if err := sw.SetPanes(&excelize.Panes{Freeze: true, YSplit: 10, TopLeftCell: "A11", ActivePane: "bottomLeft"}); err != nil {
		return err
	}

	// Rows 1-6: title block as in the sample.
	title := "      Data From Amazon"
	if outOfScope {
		title = "      Data From Amazon — payment rows of settlements other than " + strings.Join(run.Scope, ", ")
	}
	if err := sw.SetRow("A1", []any{excelize.Cell{StyleID: st.bold, Value: title}}); err != nil {
		return err
	}
	if err := sw.SetRow("A3", []any{excelize.Cell{StyleID: st.bold, Value: "DS1 ="}, excelize.Cell{StyleID: st.bold, Value: "Payment Report"}}); err != nil {
		return err
	}
	if err := sw.SetRow("A4", []any{excelize.Cell{StyleID: st.bold, Value: "DS2 ="}, excelize.Cell{StyleID: st.bold, Value: "Settlement Report"}}); err != nil {
		return err
	}
	if err := sw.SetRow("A5", []any{excelize.Cell{StyleID: st.note, Value: fmt.Sprintf(
		"Run %d (%s), scope settlement %s. One row per record_ref; DS1/DS2 descriptive columns show the first source row of the record, amounts are sums over all its rows (see source lines).",
		run.ID, run.Label, strings.Join(run.Scope, ", "))}}); err != nil {
		return err
	}

	// Rows 7-10: the sample's four header rows.
	var h7, h8, h9, h10 []any
	hc := func(v string) any { return excelize.Cell{StyleID: st.header, Value: v} }
	addBlock := func(group string, n int, h8v string, h9v func(i int) string, h10v func(i int) string) {
		for i := 0; i < n; i++ {
			h7 = append(h7, hc(group))
			h8 = append(h8, hc(h8v))
			h9 = append(h9, hc(h9v(i)))
			h10 = append(h10, hc(h10v(i)))
		}
	}
	empty := func(int) string { return "" }
	section := func(i int) string { return fields[i].Section }
	label := func(i int) string { return fields[i].ConsolidatedLabel }
	addBlock("Payment Report", len(paymentCols), "", empty, func(i int) string { return paymentCols[i].header })
	addBlock("Payment Report", 1, "", empty, func(int) string { return "Total" })
	addBlock("Payment Report", nb, "Summary", section, label)
	addBlock("Settlement Report", len(settlementCols), "", empty, func(i int) string { return settlementCols[i].header })
	addBlock("Settlement Report", 1, "", empty, func(int) string { return "Total" })
	addBlock("Settlement Report", nb, "Summary", section, label)
	addBlock("", 1, "", empty, func(int) string { return "Reconciliation Status" })
	addBlock("", 1, "", empty, func(int) string { return "total (DS1-DS2)" })
	addBlock("", nb, "Summary Mismatch", section, func(i int) string { return fields[i].ConsolidatedLabel + " (DS1-DS2)" })
	addBlock("Audit", len(auditHeaders), "", empty, func(i int) string { return auditHeaders[i] })
	for i, h := range [][]any{h7, h8, h9, h10} {
		if err := sw.SetRow(cell("A", 7+i), h); err != nil {
			return err
		}
	}

	rows, err := pool.Query(ctx, `
SELECT rr.record_ref, rr.status, rr.pay_rows, rr.set_rows,
       COALESCE(rr.pay_total::text, ''), COALESCE(rr.set_total::text, ''), rr.diff_total::text,
       p.raw, COALESCE(to_char(p.ref_date, 'YYYY-MM-DD'), ''), s.raw, COALESCE(s.settlement_id, ''),
       COALESCE(rr.pay_lines, '{}'), COALESCE(rr.set_lines, '{}'),
       COALESCE(rr.pay_rule_ids, '{}'), COALESCE(rr.set_rule_ids, '{}'), COALESCE(rr.set_amount_types, ''),
       COALESCE(p.txn_status, '')
FROM recon_records rr
LEFT JOIN source_rows p ON p.id = rr.pay_first_row_id
LEFT JOIN source_rows s ON s.id = rr.set_first_row_id
WHERE rr.run_id = $1 AND rr.status = ANY($2::text[])
ORDER BY array_position($2::text[], rr.status), rr.record_ref`, run.ID, statuses)
	if err != nil {
		return err
	}
	defer rows.Close()

	rowNum := 11
	for rows.Next() {
		var ref, status, payTotal, setTotal, diff, refDate, sid, setTypes, payStatus string
		var payRows, setRows int
		var praw, sraw []byte
		var payLines, setLines, payRules, setRules []int32
		if err := rows.Scan(&ref, &status, &payRows, &setRows, &payTotal, &setTotal, &diff, &praw, &refDate, &sraw, &sid,
			&payLines, &setLines, &payRules, &setRules, &setTypes, &payStatus); err != nil {
			return err
		}
		pr, sr := map[string]string{}, map[string]string{}
		if len(praw) > 0 {
			if err := json.Unmarshal(praw, &pr); err != nil {
				return err
			}
		}
		if len(sraw) > 0 {
			if err := json.Unmarshal(sraw, &sr); err != nil {
				return err
			}
		}
		hasPay, hasSet := payRows > 0, setRows > 0
		pb, sb := buckets[paySide][ref], buckets["settlements"][ref]

		vals := make([]any, 0, total)
		for _, c := range paymentCols {
			switch {
			case !hasPay:
				vals = append(vals, nil)
			case c.header == "Date":
				vals = append(vals, refDate)
			case c.header == "Quantity":
				vals = append(vals, numberOrText(pr[c.raw]))
			case c.raw == "":
				vals = append(vals, nil)
			default:
				vals = append(vals, pr[c.raw])
			}
		}
		vals = append(vals, moneyCell(st, hasPay, payTotal))
		vals = appendBuckets(vals, st, fields, pb, hasPay)
		for _, c := range settlementCols {
			v := sr[c.raw]
			if settlementHeaderFields[c.raw] && hasSet {
				v = d.headerByID[sid][c.raw]
			}
			switch {
			case !hasSet:
				vals = append(vals, nil)
			case c.raw == "quantity-purchased" || c.raw == "total-amount":
				vals = append(vals, numberOrText(v))
			default:
				vals = append(vals, v)
			}
		}
		vals = append(vals, moneyCell(st, hasSet, setTotal))
		vals = appendBuckets(vals, st, fields, sb, hasSet)
		vals = append(vals, statusLabels[status], excelize.Cell{StyleID: st.money, Value: money(dec(diff))})
		for _, l := range fields {
			vals = append(vals, excelize.Cell{StyleID: st.money, Value: money(pb[l.Field].Sub(sb[l.Field]))})
		}
		vals = append(vals,
			ref,
			payRows,
			lineList(d.fileNames["payments"], payLines),
			setRows,
			lineList(d.fileNames["settlements"], setLines),
			setTypes,
			intList(payRules),
			intList(setRules),
			offSheet(pb, onSheet),
			offSheet(sb, onSheet),
			unreconciledReason(status, payStatus, dec(payTotal), pb),
		)
		if err := sw.SetRow(cell("A", rowNum), vals); err != nil {
			return err
		}
		rowNum++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := sw.MergeCell("A1", "J1"); err != nil {
		return err
	}
	return sw.Flush()
}

// unreconciledReason classifies a record that has no counterpart, from its own data only.
func unreconciledReason(status, payStatus string, payTotal decimal.Decimal, pb map[string]decimal.Decimal) string {
	switch status {
	case "unreconciled_settlement":
		return "No payment record with this record_ref"
	case "unreconciled_payment":
		routed := false
		for _, v := range pb {
			if !v.IsZero() {
				routed = true
			}
		}
		switch {
		case payStatus == "Deferred":
			return "Deferred: not yet released into a settlement (timing)"
		case payTotal.IsZero() && !routed:
			return "Zero-value line: no money moved, so no settlement line exists"
		case !routed:
			return "Amount not routed to any summary field by its rule (e.g. payout transfer); no settlement line"
		default:
			return "No settlement record with this record_ref"
		}
	}
	return ""
}

func appendBuckets(vals []any, st styles, fields []SummaryLine, b map[string]decimal.Decimal, present bool) []any {
	for _, l := range fields {
		if !present {
			vals = append(vals, nil)
			continue
		}
		vals = append(vals, excelize.Cell{StyleID: st.money, Value: money(b[l.Field])})
	}
	return vals
}

func moneyCell(st styles, present bool, v string) any {
	if !present || v == "" {
		return nil
	}
	return excelize.Cell{StyleID: st.money, Value: money(dec(v))}
}

func numberOrText(s string) any {
	if s == "" {
		return nil
	}
	if n, err := strconv.ParseFloat(strings.ReplaceAll(s, ",", ""), 64); err == nil {
		return n
	}
	return s
}

func lineList(file string, lines []int32) string {
	if len(lines) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(file)
	b.WriteString(":")
	for i, l := range lines {
		s := strconv.Itoa(int(l))
		if i > 0 {
			s = "," + s
		}
		if b.Len()+len(s) > maxCell-40 {
			fmt.Fprintf(&b, " ... +%d more", len(lines)-i)
			break
		}
		b.WriteString(s)
	}
	return b.String()
}

func intList(xs []int32) string {
	parts := make([]string, len(xs))
	for i, x := range xs {
		parts[i] = strconv.Itoa(int(x))
	}
	return strings.Join(parts, ",")
}

func offSheet(b map[string]decimal.Decimal, onSheet map[string]bool) string {
	var keys []string
	for k := range b {
		if !onSheet[k] {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + b[k].StringFixed(2)
	}
	return strings.Join(parts, "; ")
}
