package quota

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ZhangNy301/agy-switch/pkg/util"
)

type cliUsageResponse struct {
	Status  string `json:"status"`
	Command struct {
		Name string `json:"name"`
		Data struct {
			Description string          `json:"description"`
			Groups      []cliUsageGroup `json:"groups"`
		} `json:"data"`
	} `json:"command"`
}

type cliUsageGroup struct {
	Name        string           `json:"name"`
	Description string           `json:"description"`
	Buckets     []cliUsageBucket `json:"buckets"`
}

type cliUsageBucket struct {
	ID                string  `json:"id"`
	Name              string  `json:"name"`
	Description       string  `json:"description"`
	Window            string  `json:"window"`
	RemainingFraction float64 `json:"remaining_fraction"`
	Disabled          bool    `json:"disabled"`
	ResetTime         string  `json:"reset_time"`
}

func parseCLIUsage(raw []byte) (*cliUsageResponse, error) {
	idx := bytes.IndexByte(raw, '{')
	if idx < 0 {
		return nil, fmt.Errorf("agy /usage output contained no JSON object")
	}
	var resp cliUsageResponse
	if err := json.Unmarshal(raw[idx:], &resp); err != nil {
		return nil, fmt.Errorf("decode agy /usage JSON: %w", err)
	}
	if resp.Status != "" && !strings.EqualFold(resp.Status, "SUCCESS") {
		return nil, fmt.Errorf("agy /usage returned status %q", resp.Status)
	}
	return &resp, nil
}

func normalizeResetTime(raw string) string {
	if raw == "" {
		return ""
	}
	ts, ok := util.ParseTimestamp(raw)
	if !ok || ts.Unix() <= 0 {
		return ""
	}
	return raw
}

func applyCLIUsage(info *QuotaInfo, resp *cliUsageResponse) {
	if info == nil || resp == nil {
		return
	}
	for _, g := range resp.Command.Data.Groups {
		gName := strings.ToLower(g.Name)
		for _, b := range g.Buckets {
			id := strings.ToLower(b.ID)
			window := strings.ToLower(b.Window)
			if window == "" {
				if strings.HasSuffix(id, "-5h") {
					window = "5h"
				} else if strings.HasSuffix(id, "-weekly") {
					window = "weekly"
				}
			}
			reset := normalizeResetTime(b.ResetTime)
			isGemini := strings.Contains(gName, "gemini") || strings.HasPrefix(id, "gemini-")
			isClaude := strings.Contains(gName, "claude") || strings.HasPrefix(id, "3p-")
			disabled := b.Disabled || (window == "5h" && strings.Contains(strings.ToLower(b.Description), "does not currently apply"))

			if isGemini {
				if window == "5h" {
					info.Gemini5hFraction = b.RemainingFraction
					info.Gemini5hReset = reset
					info.Gemini5hDisabled = disabled
					info.HasGemini = true
				} else if window == "weekly" {
					info.GeminiWeeklyFraction = b.RemainingFraction
					info.GeminiWeeklyReset = reset
					info.GeminiWeeklyDisabled = disabled
					info.HasGeminiWeekly = true
					if !info.HasClaudeWeekly {
						info.WeeklyFraction = b.RemainingFraction
						info.WeeklyReset = reset
					}
				}
			} else if isClaude {
				if window == "5h" {
					info.Claude5hFraction = b.RemainingFraction
					info.Claude5hReset = reset
					info.Claude5hDisabled = disabled
					info.HasClaude = true
				} else if window == "weekly" {
					info.ClaudeWeeklyFraction = b.RemainingFraction
					info.ClaudeWeeklyReset = reset
					info.ClaudeWeeklyDisabled = disabled
					info.HasClaudeWeekly = true
					info.WeeklyFraction = b.RemainingFraction
					info.WeeklyReset = reset
				}
			}
		}
	}
}
