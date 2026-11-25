package tui

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/ZhangNy301/agy-switch/pkg/profile"
	"github.com/ZhangNy301/agy-switch/pkg/quota"
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

func TestPickerView_TierAndColumnAlignment(t *testing.T) {
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

	profiles := []profile.Profile{
		{Name: "cwatana1", Tier: "Free", Email: "cwatana1@gmail.com", ActivatedAt: time.Now().Add(-5 * time.Minute)},
		{Name: "cwatana2", Tier: "Pro", Email: "short@a.co"},
		{Name: "cwatana3", Tier: "Ultra", Email: "longer-address@example.com"},
		{Name: "cwatana4", Tier: "", Email: "cwatana4@gmail.com"},
	}
	m := NewPickerModelWithOptions(profiles, nil, PickerOptions{ActiveName: "cwatana2"})
	if m.Cursor() != 1 {
		t.Fatalf("expected cursor on active profile cwatana2 (index 1), got %d", m.Cursor())
	}
	for i := range profiles {
		m.quotas[i] = &quota.QuotaInfo{
			HasGemini:            true,
			Gemini5hFraction:     0.5,
			Gemini5hReset:        time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339),
			HasGeminiWeekly:      true,
			GeminiWeeklyFraction: 0.75,
			HasClaude:            true,
			Claude5hFraction:     1.0,
			Claude5hDisabled:     i == 0,
			HasClaudeWeekly:      true,
			ClaudeWeeklyFraction: 0.4,
		}
	}

	view := m.View()
	plainView := stripAnsi(view)
	if !strings.Contains(plainView, "● cwatana2") || !strings.Contains(plainView, "disabled") || !strings.Contains(plainView, "week") {
		t.Fatalf("expected active marker, disabled, and weekly quota in view:\n%s", plainView)
	}

	lines := strings.Split(view, "\n")
	if len(lines) < 3+len(profiles)*4 {
		t.Fatalf("unexpected lines: %q", lines)
	}
	var emailCols []int
	for i, p := range profiles {
		plain := stripAnsi(lines[3+i*4])
		emailIdx := strings.Index(plain, p.Email)
		if emailIdx < 0 {
			t.Fatalf("missing email in header line %d: %q", i, plain)
		}
		emailCols = append(emailCols, utf8.RuneCountInString(plain[:emailIdx]))
	}
	for i := 1; i < len(profiles); i++ {
		if emailCols[i] != emailCols[0] {
			t.Fatalf("email column misaligned at row %d: got %d, want %d (all=%v)", i, emailCols[i], emailCols[0], emailCols)
		}
	}
}
