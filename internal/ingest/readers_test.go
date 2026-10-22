package ingest

import (
	"errors"
	"io"
	"os"
	"strings"
	"testing"
)

func readAll(t *testing.T, r Reader) []*Row {
	t.Helper()
	var rows []*Row
	for {
		row, err := r.Next()
		if errors.Is(err, io.EOF) {
			return rows
		}
		if err != nil {
			t.Fatal(err)
		}
		rows = append(rows, row)
	}
}

func TestPaymentsReaderFixture(t *testing.T) {
	f, err := os.Open("../../testdata/payments.csv")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	pr, err := NewPaymentsReader(f)
	if err != nil {
		t.Fatal(err)
	}
	rows := readAll(t, pr)
	if len(rows) != 7 {
		t.Fatalf("rows = %d, want 7", len(rows))
	}
	if len(pr.Skipped()) != 2 || pr.Skipped()[0].LineNo != 1 {
		t.Fatalf("preamble skipped = %+v", pr.Skipped())
	}
	first := rows[0]
	if first.LineNo != 4 {
		t.Errorf("first data row line = %d, want 4 (BOM line 1, preamble 1-2, header 3)", first.LineNo)
	}
	if first.Description != "Widget, large" || first.Raw["description"] != "Widget, large" {
		t.Errorf("quoted comma not kept: %q", first.Description)
	}
	if first.Cols["total"].String() != "13" || first.Cols["fba_fees"].String() != "-5" {
		t.Errorf("amounts = %v", first.Cols)
	}
	if !strings.HasPrefix(first.RawLine, `"10 July 2026`) {
		t.Errorf("raw line = %q", first.RawLine)
	}
	transfer := rows[6]
	if transfer.Cols["total"].String() != "-1050" {
		t.Errorf("thousands separator: %v", transfer.Cols["total"])
	}
	if rows[3].Quantity != nil || rows[3].ReleaseAt != nil {
		t.Errorf("empty quantity / release date should be NULL")
	}
}

func TestPaymentsReaderBadCellIsReportedNotDropped(t *testing.T) {
	in := `"date/time","settlement ID","type","order ID","sku","description","total"` + "\n" +
		`"not a date","S1","Order","1","A","d","abc"` + "\n"
	pr, err := NewPaymentsReader(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	rows := readAll(t, pr)
	if len(rows) != 1 || rows[0].Kind != KindError || len(rows[0].Errors) != 2 {
		t.Fatalf("got %+v", rows)
	}
	if rows[0].LineNo != 2 {
		t.Errorf("line = %d", rows[0].LineNo)
	}
}

func TestPaymentsReaderMultilineQuotedField(t *testing.T) {
	in := `"date/time","settlement ID","type","order ID","sku","description","total"` + "\n" +
		`"1 July 2026 1:00:00 pm GMT+9","S1","Order","1","A","two` + "\n" + `lines","1.00"` + "\n" +
		`"1 July 2026 1:00:00 pm GMT+9","S1","Order","2","A","x","2.00"` + "\n"
	pr, err := NewPaymentsReader(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	rows := readAll(t, pr)
	if len(rows) != 2 || rows[0].Description != "two\nlines" || rows[1].LineNo != 4 {
		t.Fatalf("got %d rows; first %q; second line %d", len(rows), rows[0].Description, rows[1].LineNo)
	}
}

func TestPaymentsReaderMissingHeader(t *testing.T) {
	if _, err := NewPaymentsReader(strings.NewReader("\"just\",\"text\"\n")); err == nil {
		t.Fatal("expected error")
	}
}

func TestSettlementsReaderFixture(t *testing.T) {
	f, err := os.Open("../../testdata/settlements.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sr, err := NewSettlementsReader(f)
	if err != nil {
		t.Fatal(err)
	}
	rows := readAll(t, sr)
	if len(rows) != 10 {
		t.Fatalf("rows = %d", len(rows))
	}
	h := rows[0]
	if h.Kind != KindSettlementHeader || h.TotalAmount.String() != "-1.97" || h.LineNo != 2 || h.Currency != "AUD" {
		t.Fatalf("header row = %+v", h)
	}
	d := rows[1]
	if d.Kind != KindData || d.Amount.String() != "20" || d.AmountType != "ItemPrice" || d.Description != "Principal" ||
		d.PostedDate.Format("2006-01-02") != "2026-07-17" || *d.Quantity != 1 {
		t.Fatalf("data row = %+v", d)
	}
	if rows[3].Quantity != nil {
		t.Error("empty quantity-purchased should be NULL")
	}
}
