package tui

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ZhangNy301/agy-switch/pkg/profile"
	"github.com/ZhangNy301/agy-switch/pkg/quota"
	"github.com/ZhangNy301/agy-switch/pkg/util"
)

const (
	BucketBarWidth  = 12
	BucketCellWidth = BucketBarWidth + 5 // bar + space + "100%"
	ResetReserve    = 9                  // reserved width for " ↻4d21h"
)

var (
	barFilledTable [BucketBarWidth + 1]string
	barEmptyTable  [BucketBarWidth + 1]string
	spacePadTable  [65]string

	renderedCursor      = CursorStyle.Render("▸ ")
	renderedDotActive   = ActiveMarker.Render("●")
	renderedDotInactive = DimStyle.Render("○")
	renderedLabelGemini = DimStyle.Render("Gemini")
	renderedLabelClaude = DimStyle.Render("Claude")

	renderedTierPro     = BadgePro.Render("[Pro]")
	renderedTierUltra   = BadgeUltra.Render("[Ultra]")
	renderedTierFree    = BadgeFree.Render("[Free]")
	renderedTierUnknown = DimStyle.Render("[?]")

	renderedNoQuota         string
	renderedNoQuotaReserve  string
	renderedDisabled        string
	renderedDisabledReserve string
)

func init() {
	for i := 0; i <= BucketBarWidth; i++ {
		barFilledTable[i] = strings.Repeat("█", i)
		barEmptyTable[i] = strings.Repeat("░", i)
	}
	for i := 0; i < len(spacePadTable); i++ {
		spacePadTable[i] = strings.Repeat(" ", i)
	}
	renderedNoQuota = DimStyle.Render(strings.Repeat("─", BucketBarWidth) + "    —")
	renderedNoQuotaReserve = DimStyle.Render(strings.Repeat("─", BucketBarWidth) + "    —" + spaces(ResetReserve))
	renderedDisabled = DimStyle.Render("disabled" + spaces(BucketCellWidth-len("disabled")))
	renderedDisabledReserve = DimStyle.Render("disabled" + spaces(BucketCellWidth+ResetReserve-len("disabled")))
}

func spaces(n int) string {
	if n <= 0 {
		return ""
	}
	if n < len(spacePadTable) {
		return spacePadTable[n]
	}
	return strings.Repeat(" ", n)
}

func renderBar(fraction float64, width int, hasQuota bool) string {
	if !hasQuota {
		return DimStyle.Render("["+strings.Repeat("─", width)+"]") + "    —"
	}
	if fraction < 0 {
		fraction = 0
	}
	if fraction > 1.0 {
		fraction = 1.0
	}
	filled := int(fraction * float64(width))
	if filled > width {
		filled = width
	}
	pct := int(math.Round(fraction * 100))
	bar := strings.Repeat("█", filled) + strings.Repeat("░", width-filled)

	style := CursorStyle
	if pct <= 20 {
		style = BadgeUltra
	} else if pct <= 50 {
		style = BadgePro
	}
	return style.Render("["+bar+"]") + fmt.Sprintf(" %3d%%", pct)
}

func writeBucketCell(sb *strings.Builder, fraction float64, resetTime string, hasQuota bool, disabled bool, reserve int, now time.Time) {
	if disabled {
		if reserve == ResetReserve {
			sb.WriteString(renderedDisabledReserve)
			return
		}
		if reserve == 0 {
			sb.WriteString(renderedDisabled)
			return
		}
		text := "disabled"
		if pad := BucketCellWidth + reserve - len(text); pad > 0 {
			text += spaces(pad)
		}
		sb.WriteString(DimStyle.Render(text))
		return
	}
	if !hasQuota {
		if reserve == ResetReserve {
			sb.WriteString(renderedNoQuotaReserve)
			return
		}
		if reserve == 0 {
			sb.WriteString(renderedNoQuota)
			return
		}
		base := strings.Repeat("─", BucketBarWidth) + "    —"
		if pad := BucketCellWidth + reserve - utf8.RuneCountInString(base); pad > 0 {
			base += spaces(pad)
		}
		sb.WriteString(DimStyle.Render(base))
		return
	}
	if fraction < 0 {
		fraction = 0
	}
	if fraction > 1.0 {
		fraction = 1.0
	}
	filled := int(math.Round(fraction * float64(BucketBarWidth)))
	if filled > BucketBarWidth {
		filled = BucketBarWidth
	}
	pct := int(math.Round(fraction * 100))
	style := CursorStyle
	if pct < 20 {
		style = BadgeUltra
	} else if pct < 50 {
		style = BadgePro
	}
	if filled > 0 {
		sb.WriteString(style.Render(barFilledTable[filled]))
	}
	if filled < BucketBarWidth {
		sb.WriteString(DimStyle.Render(barEmptyTable[BucketBarWidth-filled]))
	}
	sb.WriteByte(' ')
	if pct < 10 {
		sb.WriteString("  ")
	} else if pct < 100 {
		sb.WriteByte(' ')
	}
	sb.WriteString(strconv.Itoa(pct))
	sb.WriteByte('%')

	resetLen := 0
	if fraction < 0.999 {
		if ts, ok := util.ParseTimestamp(resetTime); ok {
			rel := util.FormatRelativeTime(ts, now)
			sb.WriteString(" ↻")
			sb.WriteString(rel)
			resetLen = 2 + utf8.RuneCountInString(rel)
		}
	}
	if reserve > resetLen {
		sb.WriteString(spaces(reserve - resetLen))
	}
}

// RenderBucketCell renders a quota bar, percentage, and optional reset countdown.
func RenderBucketCell(fraction float64, resetTime string, hasQuota bool, disabled bool, reserve int, now time.Time) string {
	var sb strings.Builder
	sb.Grow(64)
	writeBucketCell(&sb, fraction, resetTime, hasQuota, disabled, reserve, now)
	return sb.String()
}

func tierBadgeLen(tier string) int {
	switch tier {
	case "Pro":
		return len("[Pro]")
	case "Ultra":
		return len("[Ultra]")
	case "Free":
		return len("[Free]")
	case "", "?":
		return len("[?]")
	default:
		return utf8.RuneCountInString(tier) + 2
	}
}

func renderTierBadge(tier string) (styled string, visibleLen int) {
	switch tier {
	case "Pro":
		return renderedTierPro, len("[Pro]")
	case "Ultra":
		return renderedTierUltra, len("[Ultra]")
	case "Free":
		return renderedTierFree, len("[Free]")
	case "", "?":
		return renderedTierUnknown, len("[?]")
	default:
		label := "[" + tier + "]"
		return DimStyle.Render(label), utf8.RuneCountInString(label)
	}
}

// AccountBlockWidths holds column widths for aligning multiple AccountBlock rows.
type AccountBlockWidths struct {
	NameWidth  int
	TierWidth  int
	EmailWidth int
}

// ComputeAccountBlockWidths calculates aligned column widths across profiles.
func ComputeAccountBlockWidths(profiles []profile.Profile) AccountBlockWidths {
	w := AccountBlockWidths{NameWidth: 14, TierWidth: len("[?]")}
	for _, p := range profiles {
		if n := utf8.RuneCountInString(p.Name); n > w.NameWidth {
			w.NameWidth = n
		}
		if tLen := tierBadgeLen(p.Tier); tLen > w.TierWidth {
			w.TierWidth = tLen
		}
		if e := utf8.RuneCountInString(p.Email); e > w.EmailWidth {
			w.EmailWidth = e
		}
	}
	return w
}

// WriteAccountBlock writes a 3-line account block directly into sb with minimal allocations.
func WriteAccountBlock(sb *strings.Builder, p profile.Profile, q *quota.QuotaInfo, widths AccountBlockWidths, active bool, cursor bool, now time.Time) {
	if cursor {
		sb.WriteString(renderedCursor)
	} else {
		sb.WriteString("  ")
	}

	tier := p.Tier
	if q != nil && q.Tier != "" && q.Tier != "?" {
		tier = q.Tier
	}
	tierBadge, tierLen := renderTierBadge(tier)

	lastActive := "never"
	if !p.ActivatedAt.IsZero() {
		lastActive = util.FormatRelativeTime(p.ActivatedAt, now)
	}

	var hb strings.Builder
	hb.Grow(128)
	if active {
		hb.WriteString(renderedDotActive)
	} else {
		hb.WriteString(renderedDotInactive)
	}
	hb.WriteByte(' ')
	if active {
		hb.WriteString(ActiveMarker.Render(p.Name))
	} else {
		hb.WriteString(p.Name)
	}
	hb.WriteString(spaces(widths.NameWidth - utf8.RuneCountInString(p.Name)))
	hb.WriteByte(' ')
	hb.WriteString(tierBadge)
	hb.WriteString(spaces(widths.TierWidth - tierLen))
	hb.WriteString("  ")
	hb.WriteString(DimStyle.Render(p.Email))
	hb.WriteString(spaces(widths.EmailWidth - utf8.RuneCountInString(p.Email)))
	hb.WriteString("  ")
	hb.WriteString(DimStyle.Render("· active " + lastActive))

	if cursor {
		sb.WriteString(SelectedStyle.Render(hb.String()))
	} else {
		sb.WriteString(NormalStyle.Render(hb.String()))
	}
	sb.WriteByte('\n')

	sb.WriteString("      ")
	sb.WriteString(renderedLabelGemini)
	sb.WriteString("  5h ")
	if q != nil {
		writeBucketCell(sb, q.Gemini5hFraction, q.Gemini5hReset, q.HasGemini, q.Gemini5hDisabled, ResetReserve, now)
	} else {
		writeBucketCell(sb, 0, "", false, false, ResetReserve, now)
	}
	sb.WriteString("  week ")
	if q != nil {
		writeBucketCell(sb, q.GeminiWeeklyFraction, q.GeminiWeeklyReset, q.HasGeminiWeekly, q.GeminiWeeklyDisabled, 0, now)
	} else {
		writeBucketCell(sb, 0, "", false, false, 0, now)
	}
	sb.WriteByte('\n')

	sb.WriteString("      ")
	sb.WriteString(renderedLabelClaude)
	sb.WriteString("  5h ")
	if q != nil {
		writeBucketCell(sb, q.Claude5hFraction, q.Claude5hReset, q.HasClaude, q.Claude5hDisabled, ResetReserve, now)
	} else {
		writeBucketCell(sb, 0, "", false, false, ResetReserve, now)
	}
	sb.WriteString("  week ")
	if q != nil {
		writeBucketCell(sb, q.ClaudeWeeklyFraction, q.ClaudeWeeklyReset, q.HasClaudeWeekly, q.ClaudeWeeklyDisabled, 0, now)
	} else {
		writeBucketCell(sb, 0, "", false, false, 0, now)
	}
	sb.WriteByte('\n')
}

// FormatAccountBlock renders a 3-line account block shared by the interactive picker and `ls`.
func FormatAccountBlock(p profile.Profile, q *quota.QuotaInfo, widths AccountBlockWidths, active bool, cursor bool, now time.Time) []string {
	var sb strings.Builder
	sb.Grow(300)
	WriteAccountBlock(&sb, p, q, widths, active, cursor, now)
	return strings.Split(strings.TrimSuffix(sb.String(), "\n"), "\n")
}
