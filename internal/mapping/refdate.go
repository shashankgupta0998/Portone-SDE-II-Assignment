package mapping

import (
	"time"

	"amazonrecon/internal/ingest"
)

// RefDate returns the value of the record_ref "date" token for a row, as a UTC calendar date.
//
// Settlements: posted-date (the date the amount was posted into the settlement).
// Payments: the UTC date of "Transaction Release Date" — the moment the amount was released
// into a settlement, which is what the settlement's posted-date-time records — falling back to
// the UTC date of "date/time" when the release date is empty (Deferred rows).
// This is a matching-logic interpretation (the configs only say "date"); it is documented in
// the README.
func RefDate(r *ingest.Row) *time.Time {
	var t *time.Time
	switch r.Source {
	case ingest.SourceSettlements:
		t = r.PostedDate
		if t == nil {
			t = r.PostedAt
		}
	case ingest.SourcePayments:
		t = r.ReleaseAt
		if t == nil {
			t = r.PostedAt
		}
	}
	if t == nil {
		return nil
	}
	u := t.UTC()
	d := time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
	return &d
}
