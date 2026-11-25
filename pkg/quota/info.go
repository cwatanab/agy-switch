package quota

import (
	"time"

	"github.com/ZhangNy301/agy-switch/pkg/profile"
)

type QuotaInfo struct {
	Tier                 string         `json:"tier"`
	Gemini5hFraction     float64        `json:"gemini_5h_fraction"`
	Gemini5hReset        string         `json:"gemini_5h_reset,omitempty"`
	Gemini5hDisabled     bool           `json:"gemini_5h_disabled,omitempty"`
	GeminiWeeklyFraction float64        `json:"gemini_weekly_fraction"`
	GeminiWeeklyReset    string         `json:"gemini_weekly_reset,omitempty"`
	HasGeminiWeekly      bool           `json:"has_gemini_weekly,omitempty"`
	GeminiWeeklyDisabled bool           `json:"gemini_weekly_disabled,omitempty"`
	Claude5hFraction     float64        `json:"claude_5h_fraction"`
	Claude5hReset        string         `json:"claude_5h_reset,omitempty"`
	Claude5hDisabled     bool           `json:"claude_5h_disabled,omitempty"`
	ClaudeWeeklyFraction float64        `json:"claude_weekly_fraction"`
	ClaudeWeeklyReset    string         `json:"claude_weekly_reset,omitempty"`
	HasClaudeWeekly      bool           `json:"has_claude_weekly,omitempty"`
	ClaudeWeeklyDisabled bool           `json:"claude_weekly_disabled,omitempty"`
	WeeklyFraction       float64        `json:"weekly_fraction"`
	WeeklyReset          string         `json:"weekly_reset,omitempty"`
	HasGemini            bool           `json:"has_gemini"`
	HasClaude            bool           `json:"has_claude"`
	Project              string         `json:"project,omitempty"`
	Email                string         `json:"email,omitempty"`
	UpdatedAt            time.Time      `json:"updated_at"`
	RefreshedToken       *profile.Token `json:"-"`
}
