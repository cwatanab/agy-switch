package util

import (
	"testing"
	"time"
)

func TestParseTimestampAndFormatRelativeTime(t *testing.T) {
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)

	if ts, ok := ParseTimestamp("2026-11-29T09:00:00.123456789Z"); !ok || ts.Year() != 2026 {
		t.Fatalf("expected valid timestamp, got ok=%v ts=%v", ok, ts)
	}
	if _, ok := ParseTimestamp("not-a-timestamp"); ok {
		t.Fatal("expected invalid timestamp to fail")
	}

	cases := []struct {
		target time.Time
		want   string
	}{
		{time.Time{}, "?"},
		{now.Add(20 * time.Second), "now"},
		{now.Add(-20 * time.Second), "just now"},
		{now.Add(3 * time.Minute), "3m"},
		{now.Add(-3 * time.Minute), "3m ago"},
		{now.Add(2*time.Hour + 13*time.Minute), "2h13m"},
		{now.Add(4*24*time.Hour + 21*time.Hour), "4d21h"},
		{now.Add(-4*24*time.Hour - 21*time.Hour), "4d21h ago"},
	}
	for _, tc := range cases {
		if got := FormatRelativeTime(tc.target, now); got != tc.want {
			t.Errorf("FormatRelativeTime(%v) = %q, want %q", tc.target, got, tc.want)
		}
	}
}

func TestParseValidityAndFormatExpiry(t *testing.T) {
	loc := time.FixedZone("JST", 9*3600)
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, loc)

	for _, empty := range []string{"", "never", "NONE", "-"} {
		got, err := ParseValidity(empty, now)
		if err != nil || got != 0 {
			t.Fatalf("ParseValidity(%q) = (%d, %v), want (0, nil)", empty, got, err)
		}
	}

	if got, err := ParseValidity("30d", now); err != nil || got != now.Unix()+30*86400 {
		t.Fatalf("ParseValidity(30d) = (%d, %v)", got, err)
	}
	if got, err := ParseValidity("12H", now); err != nil || got != now.Unix()+12*3600 {
		t.Fatalf("ParseValidity(12H) = (%d, %v)", got, err)
	}
	if got, err := ParseValidity("90m", now); err != nil || got != now.Unix()+90*60 {
		t.Fatalf("ParseValidity(90m) = (%d, %v)", got, err)
	}

	gotDate, err := ParseValidity("2026-12-01", now)
	if err != nil {
		t.Fatalf("ParseValidity(date) error: %v", err)
	}
	wantDate := time.Date(2026, 12, 1, 23, 59, 59, 0, loc).Unix()
	if gotDate != wantDate {
		t.Fatalf("ParseValidity(date) = %d, want %d", gotDate, wantDate)
	}

	if _, err := ParseValidity("bogus", now); err == nil {
		t.Fatal("expected error for bogus validity")
	}

	if got := FormatExpiry(0, now); got != "never" {
		t.Fatalf("FormatExpiry(0) = %q, want never", got)
	}
	if got := FormatExpiry(now.Add(48*time.Hour).Unix(), now); got != "in 2d" {
		t.Fatalf("FormatExpiry(+48h) = %q, want 'in 2d'", got)
	}
	if got := FormatExpiry(now.Add(-2*time.Hour).Unix(), now); got != "expired 2h ago" {
		t.Fatalf("FormatExpiry(-2h) = %q, want 'expired 2h ago'", got)
	}
}
