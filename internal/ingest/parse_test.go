package ingest

import (
	"testing"
	"time"
)

func TestParseAmount(t *testing.T) {
	cases := []struct {
		in, want string
		err      bool
	}{
		{"14.90", "14.9", false},
		{"-133,756.51", "-133756.51", false},
		{"", "0", false},
		{" 0 ", "0", false},
		{"-0.41", "-0.41", false},
		{"abc", "", true},
		{"1.234", "", true},
	}
	for _, c := range cases {
		got, err := ParseAmount(c.in)
		if c.err {
			if err == nil {
				t.Errorf("ParseAmount(%q): expected error", c.in)
			}
			continue
		}
		if err != nil || got.String() != c.want {
			t.Errorf("ParseAmount(%q) = %s, %v; want %s", c.in, got, err, c.want)
		}
	}
}

func TestParseQuantity(t *testing.T) {
	if q, err := ParseQuantity(""); err != nil || q != nil {
		t.Fatalf("empty quantity should be NULL, got %v %v", q, err)
	}
	if q, err := ParseQuantity("3"); err != nil || *q != 3 {
		t.Fatalf("got %v %v", q, err)
	}
	if _, err := ParseQuantity("x"); err == nil {
		t.Fatal("expected error")
	}
}

func TestParsePaymentTime(t *testing.T) {
	cases := map[string]string{
		"29 June 2026 5:39:32 pm GMT+9": "2026-06-29T08:39:32Z",
		"18 July 2026 6:10:51 am GMT+9": "2026-07-17T21:10:51Z", // crosses midnight into the previous UTC day
		"1 Aug 2026 6:41:19 pm GMT+9":   "2026-08-01T09:41:19Z", // abbreviated month
		"3 Aug 2026 12:38:18 pm GMT+9":  "2026-08-03T03:38:18Z", // 12 pm is noon
		"1 July 2026 12:05:00 am GMT":   "2026-07-01T00:05:00Z",
		"1 July 2026 1:00:00 PM GMT-5":  "2026-07-01T18:00:00Z",
	}
	for in, want := range cases {
		got, err := ParsePaymentTime(in)
		if err != nil {
			t.Errorf("%q: %v", in, err)
			continue
		}
		if got.Format(time.RFC3339) != want {
			t.Errorf("%q = %s, want %s", in, got.Format(time.RFC3339), want)
		}
	}
	if got, err := ParsePaymentTime(""); err != nil || got != nil {
		t.Errorf("empty should be NULL")
	}
	for _, bad := range []string{"29 June 2026", "yesterday GMT+9", "29 June 2026 5:39:32 pm GMT+X"} {
		if _, err := ParsePaymentTime(bad); err == nil {
			t.Errorf("%q: expected error", bad)
		}
	}
}

func TestParseSettlementTime(t *testing.T) {
	got, err := ParseSettlementTime("17.07.2026 07:26:32 UTC")
	if err != nil || got.Format(time.RFC3339) != "2026-07-17T07:26:32Z" {
		t.Fatalf("got %v %v", got, err)
	}
	got, err = ParseSettlementTime("02.08.2026")
	if err != nil || got.Format(time.RFC3339) != "2026-08-02T00:00:00Z" {
		t.Fatalf("got %v %v", got, err)
	}
	if _, err := ParseSettlementTime("2026-08-02"); err == nil {
		t.Fatal("expected error")
	}
}
