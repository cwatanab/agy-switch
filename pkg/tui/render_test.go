package tui

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestRenderBar_Alignment(t *testing.T) {
	// Strip ANSI color escape codes to measure visible text width
	stripAnsi := func(str string) string {
		var b strings.Builder
		inEscape := false
		for i := 0; i < len(str); i++ {
			if str[i] == '\x1b' {
				inEscape = true
			} else if inEscape {
				if str[i] == 'm' {
					inEscape = false
				}
			} else {
				b.WriteByte(str[i])
			}
		}
		return b.String()
	}

	b100 := stripAnsi(renderBar(1.0, 8, true))
	b50 := stripAnsi(renderBar(0.5, 8, true))
	b9 := stripAnsi(renderBar(0.09, 8, true))
	b0 := stripAnsi(renderBar(0.0, 8, true))
	bNone := stripAnsi(renderBar(0.0, 8, false))

	// All rendered bars should have the exact same visible rune length (15 runes: 10 for [bar] + 5 for text)
	expectedRunes := utf8.RuneCountInString(b100)
	if utf8.RuneCountInString(b50) != expectedRunes {
		t.Errorf("50%% bar rune mismatch: got %d (%q), want %d (%q)", utf8.RuneCountInString(b50), b50, expectedRunes, b100)
	}
	if utf8.RuneCountInString(b9) != expectedRunes {
		t.Errorf("9%% bar rune mismatch: got %d (%q), want %d (%q)", utf8.RuneCountInString(b9), b9, expectedRunes, b100)
	}
	if utf8.RuneCountInString(b0) != expectedRunes {
		t.Errorf("0%% bar rune mismatch: got %d (%q), want %d (%q)", utf8.RuneCountInString(b0), b0, expectedRunes, b100)
	}
	if utf8.RuneCountInString(bNone) != expectedRunes {
		t.Errorf("no-quota bar rune mismatch: got %d (%q), want %d (%q)", utf8.RuneCountInString(bNone), bNone, expectedRunes, b100)
	}

	// Verify % is at the exact same rune index in all quota strings
	pctIdx100 := utf8.RuneCountInString(b100[:strings.Index(b100, "%")])
	pctIdx50 := utf8.RuneCountInString(b50[:strings.Index(b50, "%")])
	pctIdx9 := utf8.RuneCountInString(b9[:strings.Index(b9, "%")])
	pctIdx0 := utf8.RuneCountInString(b0[:strings.Index(b0, "%")])

	if pctIdx50 != pctIdx100 || pctIdx9 != pctIdx100 || pctIdx0 != pctIdx100 {
		t.Errorf("%% rune index mismatch: 100%%=%d, 50%%=%d, 9%%=%d, 0%%=%d", pctIdx100, pctIdx50, pctIdx9, pctIdx0)
	}
}
