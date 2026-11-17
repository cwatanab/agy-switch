package quota

import "time"

type QuotaInfo struct {
	Tier             string    `json:"tier"`
	Gemini5hFraction float64   `json:"gemini_5h_fraction"`
	Gemini5hReset    string    `json:"gemini_5h_reset,omitempty"`
	Claude5hFraction float64   `json:"claude_5h_fraction"`
	Claude5hReset    string    `json:"claude_5h_reset,omitempty"`
	WeeklyFraction   float64   `json:"weekly_fraction"`
	WeeklyReset      string    `json:"weekly_reset,omitempty"`
	HasGemini        bool      `json:"has_gemini"`
	HasClaude        bool      `json:"has_claude"`
	UpdatedAt        time.Time `json:"updated_at"`
}
