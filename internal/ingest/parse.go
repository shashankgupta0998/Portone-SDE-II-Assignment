package ingest

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// ParseAmount parses an Amazon money string: optional thousands separators ("-133,756.51"),
// empty means zero. Values are rounded to cents only if they carry more precision (never seen).
func ParseAmount(s string) (decimal.Decimal, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return decimal.Zero, nil
	}
	clean := strings.ReplaceAll(s, ",", "")
	d, err := decimal.NewFromString(clean)
	if err != nil {
		return decimal.Zero, fmt.Errorf("not a number: %q", s)
	}
	if d.Exponent() < -2 {
		return decimal.Zero, fmt.Errorf("more than 2 decimal places: %q", s)
	}
	return d, nil
}

// ParseQuantity parses an optional integer; empty means NULL.
func ParseQuantity(s string) (*int32, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	n, err := strconv.ParseInt(s, 10, 32)
	if err != nil {
		return nil, fmt.Errorf("not an integer: %q", s)
	}
	v := int32(n)
	return &v, nil
}

// ParsePaymentTime parses payments timestamps such as "29 June 2026 5:39:32 pm GMT+9" or
// "1 Aug 2026 6:41:19 pm GMT+9"
// (offset may also be GMT-5, GMT+5:30 or plain GMT). Empty means NULL. Result is in UTC.
func ParsePaymentTime(s string) (*time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	i := strings.LastIndex(s, " GMT")
	if i < 0 {
		return nil, fmt.Errorf("missing GMT offset: %q", s)
	}
	offset, err := parseGMTOffset(s[i+4:])
	if err != nil {
		return nil, fmt.Errorf("bad GMT offset in %q: %w", s, err)
	}
	loc := time.FixedZone("", offset)
	// Amazon writes full month names ("July") and abbreviations ("Aug") in the same file.
	// Month names match case-insensitively; the am/pm marker does not, so try both spellings.
	for _, layout := range []string{
		"2 January 2006 3:04:05 pm", "2 January 2006 3:04:05 PM",
		"2 Jan 2006 3:04:05 pm", "2 Jan 2006 3:04:05 PM",
	} {
		if t, err := time.ParseInLocation(layout, s[:i], loc); err == nil {
			u := t.UTC()
			return &u, nil
		}
	}
	return nil, fmt.Errorf("bad timestamp %q", s)
}

func parseGMTOffset(s string) (int, error) {
	if s == "" {
		return 0, nil
	}
	sign := 1
	switch s[0] {
	case '+':
	case '-':
		sign = -1
	default:
		return 0, fmt.Errorf("expected sign")
	}
	hm := strings.SplitN(s[1:], ":", 2)
	h, err := strconv.Atoi(hm[0])
	if err != nil || h > 14 {
		return 0, fmt.Errorf("bad hours")
	}
	m := 0
	if len(hm) == 2 {
		if m, err = strconv.Atoi(hm[1]); err != nil || m > 59 {
			return 0, fmt.Errorf("bad minutes")
		}
	}
	return sign * (h*3600 + m*60), nil
}

// ParseSettlementTime parses "17.07.2026 07:26:32 UTC" or a bare date "17.07.2026" (00:00 UTC).
// Empty means NULL.
func ParseSettlementTime(s string) (*time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	for _, layout := range []string{"02.01.2006 15:04:05 MST", "02.01.2006"} {
		if t, err := time.Parse(layout, s); err == nil {
			if layout != "02.01.2006" && !strings.HasSuffix(s, " UTC") {
				return nil, fmt.Errorf("unexpected time zone in %q", s)
			}
			u := t.UTC()
			return &u, nil
		}
	}
	return nil, fmt.Errorf("bad date %q", s)
}
