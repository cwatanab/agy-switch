package quota

import (
	"fmt"
	"strings"
)

func applyTier(info *QuotaInfo, lc map[string]interface{}) {
	if paid, ok := lc["paidTier"].(map[string]interface{}); ok {
		pid := strings.ToLower(fmt.Sprintf("%v", paid["id"]))
		if strings.Contains(pid, "ultra") {
			info.Tier = "Ultra"
		} else if strings.Contains(pid, "pro") {
			info.Tier = "Pro"
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
					window := fmt.Sprintf("%v", bMap["window"])
					var frac float64
					if f, ok := bMap["remainingFraction"].(float64); ok {
						frac = f
					}
					reset := fmt.Sprintf("%v", bMap["resetTime"])

					if strings.Contains(name, "gemini") {
						if window == "5h" {
							info.Gemini5hFraction = frac
							info.Gemini5hReset = reset
							info.HasGemini = true
						} else if window == "weekly" {
							info.WeeklyFraction = frac
							info.WeeklyReset = reset
						}
					} else if strings.Contains(name, "claude") {
						if window == "5h" {
							info.Claude5hFraction = frac
							info.Claude5hReset = reset
							info.HasClaude = true
						} else if window == "weekly" {
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
	if uq != nil {
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
				reset := fmt.Sprintf("%v", bMap["resetTime"])

				if strings.Contains(modelID, "gemini") {
					if !info.HasGemini || strings.Contains(modelID, "2.5-pro") {
						info.Gemini5hFraction = frac
						info.Gemini5hReset = reset
						info.HasGemini = true
					}
				} else if strings.Contains(modelID, "claude") {
					if !info.HasClaude || strings.Contains(modelID, "sonnet") {
						info.Claude5hFraction = frac
						info.Claude5hReset = reset
						info.HasClaude = true
					}
				}
			}
		}
	}

}
