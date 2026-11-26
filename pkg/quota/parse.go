package quota

import (
	"fmt"
	"strings"
	"time"

	"github.com/ZhangNy301/agy-switch/pkg/util"
)

func applyTier(info *QuotaInfo, lc map[string]interface{}) {
	if paid, ok := lc["paidTier"].(map[string]interface{}); ok {
		pid := strings.ToLower(fmt.Sprintf("%v", paid["id"]))
		if strings.Contains(pid, "ultra") {
			info.Tier = "Ultra"
		} else if strings.Contains(pid, "pro") {
			info.Tier = "Pro"
		} else if strings.Contains(pid, "free") {
			info.Tier = "Free"
		} else if strings.Contains(pid, "standard") {
			info.Tier = "Standard"
		} else if name, nOk := paid["name"].(string); nOk && name != "" {
			info.Tier = name
		}
	} else if curr, ok := lc["currentTier"].(map[string]interface{}); ok {
		cid := strings.ToLower(fmt.Sprintf("%v", curr["id"]))
		if cid == "free-tier" {
			info.Tier = "Free"
		} else if cid == "standard-tier" {
			info.Tier = "Standard"
		}
	}
}

func applySummary(info *QuotaInfo, qs map[string]interface{}) {
	if qs != nil {
		if groups, ok := qs["groups"].([]interface{}); ok {
			for _, g := range groups {
				groupMap, ok := g.(map[string]interface{})
				if !ok {
					continue
				}
				name := strings.ToLower(fmt.Sprintf("%v", groupMap["displayName"]))
				buckets, ok := groupMap["buckets"].([]interface{})
				if !ok {
					continue
				}
				for _, b := range buckets {
					bMap, ok := b.(map[string]interface{})
					if !ok {
						continue
					}
					window := strings.ToLower(fmt.Sprintf("%v", bMap["window"]))
					bucketID := strings.ToLower(fmt.Sprintf("%v", bMap["bucketId"]))
					if window == "" || window == "<nil>" {
						if strings.HasSuffix(bucketID, "-5h") {
							window = "5h"
						} else if strings.HasSuffix(bucketID, "-weekly") {
							window = "weekly"
						}
					}
					var frac float64
					if f, ok := bMap["remainingFraction"].(float64); ok {
						frac = f
					}
					reset, _ := bMap["resetTime"].(string)
					desc, _ := bMap["description"].(string)
					boolDisabled, _ := bMap["disabled"].(bool)
					disabled := boolDisabled || (window == "5h" && strings.Contains(strings.ToLower(desc), "does not currently apply"))

					isGemini := strings.Contains(name, "gemini") || strings.HasPrefix(bucketID, "gemini-")
					isClaude := strings.Contains(name, "claude") || strings.HasPrefix(bucketID, "3p-")

					if isGemini {
						if window == "5h" {
							info.Gemini5hFraction = frac
							info.Gemini5hReset = reset
							info.Gemini5hDisabled = disabled
							info.HasGemini = true
						} else if window == "weekly" {
							info.GeminiWeeklyFraction = frac
							info.GeminiWeeklyReset = reset
							info.GeminiWeeklyDisabled = disabled
							info.HasGeminiWeekly = true
							info.WeeklyFraction = frac
							info.WeeklyReset = reset
						}
					} else if isClaude {
						if window == "5h" {
							info.Claude5hFraction = frac
							info.Claude5hReset = reset
							info.Claude5hDisabled = disabled
							info.HasClaude = true
						} else if window == "weekly" {
							info.ClaudeWeeklyFraction = frac
							info.ClaudeWeeklyReset = reset
							info.ClaudeWeeklyDisabled = disabled
							info.HasClaudeWeekly = true
							info.WeeklyFraction = frac
							info.WeeklyReset = reset
						}
					}
				}
			}
		}
	}

}

func applyModels(info *QuotaInfo, uq map[string]interface{}) {
	applyModelsAt(info, uq, time.Now())
}

func applyModelsAt(info *QuotaInfo, uq map[string]interface{}, now time.Time) {
	if uq == nil {
		return
	}
	summaryHasGemini := info.HasGemini
	summaryHasGeminiWeekly := info.HasGeminiWeekly
	summaryHasClaude := info.HasClaude
	summaryHasClaudeWeekly := info.HasClaudeWeekly

	if buckets, ok := uq["buckets"].([]interface{}); ok {
		for _, b := range buckets {
			bMap, ok := b.(map[string]interface{})
			if !ok {
				continue
			}
			modelID := strings.ToLower(fmt.Sprintf("%v", bMap["modelId"]))
			var frac float64
			if f, ok := bMap["remainingFraction"].(float64); ok {
				frac = f
			}
			reset, _ := bMap["resetTime"].(string)
			ts, hasTS := util.ParseTimestamp(reset)
			isWeekly := hasTS && ts.Sub(now) > 24*time.Hour

			if strings.Contains(modelID, "gemini") {
				if isWeekly {
					if !summaryHasGeminiWeekly && (!info.HasGeminiWeekly || strings.Contains(modelID, "2.5-pro")) {
						info.GeminiWeeklyFraction = frac
						info.GeminiWeeklyReset = reset
						info.HasGeminiWeekly = true
						if !info.HasClaudeWeekly {
							info.WeeklyFraction = frac
							info.WeeklyReset = reset
						}
					}
				} else if !summaryHasGemini && (!info.HasGemini || strings.Contains(modelID, "2.5-pro")) {
					info.Gemini5hFraction = frac
					info.Gemini5hReset = reset
					info.HasGemini = true
				}
			} else if strings.Contains(modelID, "claude") {
				if isWeekly {
					if !summaryHasClaudeWeekly && (!info.HasClaudeWeekly || strings.Contains(modelID, "sonnet")) {
						info.ClaudeWeeklyFraction = frac
						info.ClaudeWeeklyReset = reset
						info.HasClaudeWeekly = true
						info.WeeklyFraction = frac
						info.WeeklyReset = reset
					}
				} else if !summaryHasClaude && (!info.HasClaude || strings.Contains(modelID, "sonnet")) {
					info.Claude5hFraction = frac
					info.Claude5hReset = reset
					info.HasClaude = true
				}
			}
		}
	}
}
