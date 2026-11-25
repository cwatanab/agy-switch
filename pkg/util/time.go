package util

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var relativeDurationRegex = regexp.MustCompile(`(?i)^(\d+)\s*([dhm])$`)
var dateOnlyRegex = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// ParseTimestamp parses an RFC3339 or RFC3339Nano timestamp string.
func ParseTimestamp(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, true
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, true
	}
	return time.Time{}, false
}

// FormatRelativeTime formats a timestamp relative to now in compact form
// (e.g. "3m", "4d21h", "2h13m", "just now", "now", or "... ago" for past times).
func FormatRelativeTime(t time.Time, now time.Time) string {
	if t.IsZero() {
		return "?"
	}
	secs := int64(t.Sub(now).Seconds())
	past := secs < 0
	if past {
		secs = -secs
	}
	d := secs / 86400
	secs %= 86400
	h := secs / 3600
	secs %= 3600
	m := secs / 60

	var out string
	switch {
	case d > 0:
		if h > 0 {
			out = fmt.Sprintf("%dd%dh", d, h)
		} else {
			out = fmt.Sprintf("%dd", d)
		}
	case h > 0:
		if m > 0 {
			out = fmt.Sprintf("%dh%dm", h, m)
		} else {
			out = fmt.Sprintf("%dh", h)
		}
	case m > 0:
		out = fmt.Sprintf("%dm", m)
	case past:
		return "just now"
	default:
		return "now"
	}
	if past {
		return out + " ago"
	}
	return out
}

// ParseValidity parses a validity duration ("30d", "12h", "90m") or date/datetime
// ("2026-10-01", "2026-10-01 08:00", RFC3339) into a Unix timestamp in seconds.
// Empty, "never", "none", and "-" return 0, nil.
func ParseValidity(s string, now time.Time) (int64, error) {
	s = strings.TrimSpace(s)
	switch strings.ToLower(s) {
	case "", "never", "none", "-":
		return 0, nil
	}
	if m := relativeDurationRegex.FindStringSubmatch(s); m != nil {
		n, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("cannot parse %q — use 30d / 12h / YYYY-MM-DD", s)
		}
		var mult int64
		switch strings.ToLower(m[2]) {
		case "d":
			mult = 86400
		case "h":
			mult = 3600
		case "m":
			mult = 60
		}
		return now.Unix() + n*mult, nil
	}
	if dateOnlyRegex.MatchString(s) {
		t, err := time.ParseInLocation("2006-01-02 15:04:05", s+" 23:59:59", now.Location())
		if err == nil {
			return t.Unix(), nil
		}
	}
	for _, layout := range []string{"2006-01-02 15:04", "2006-01-02 15:04:05", "2006-01-02T15:04", "2006-01-02T15:04:05"} {
		if t, err := time.ParseInLocation(layout, s, now.Location()); err == nil {
			return t.Unix(), nil
		}
	}
	if t, ok := ParseTimestamp(s); ok {
		return t.Unix(), nil
	}
	return 0, fmt.Errorf("cannot parse %q — use 30d / 12h / YYYY-MM-DD", s)
}

// FormatExpiry formats an optional Unix expiration timestamp relative to now.
func FormatExpiry(expiresAt int64, now time.Time) string {
	if expiresAt <= 0 {
		return "never"
	}
	t := time.Unix(expiresAt, 0).UTC()
	if now.Unix() >= expiresAt {
		return "expired " + FormatRelativeTime(t, now)
	}
	return "in " + FormatRelativeTime(t, now)
}
