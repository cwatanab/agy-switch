package quota_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ZhangNy301/agy-switch/pkg/config"
	"github.com/ZhangNy301/agy-switch/pkg/profile"
	"github.com/ZhangNy301/agy-switch/pkg/quota"
)

func TestClient_Fetch_SuccessAndCache(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1internal:loadCodeAssist":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"paidTier": map[string]interface{}{
					"id":   "tier-pro-annual",
					"name": "Pro",
				},
			})
		case "/v1internal:retrieveUserQuotaSummary":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"groups": []interface{}{
					map[string]interface{}{
						"displayName": "Gemini",
						"buckets": []interface{}{
							map[string]interface{}{
								"window":            "5h",
								"remainingFraction": 0.85,
								"resetTime":         "2026-11-17T18:00:00Z",
							},
							map[string]interface{}{
								"window":            "weekly",
								"remainingFraction": 0.50,
							},
						},
					},
					map[string]interface{}{
						"displayName": "Claude",
						"buckets": []interface{}{
							map[string]interface{}{
								"window":            "5h",
								"remainingFraction": 0.90,
							},
						},
					},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	paths := &config.Paths{
		DataDir: tmpDir,
	}

	client := quota.NewClient(paths)
	client.SetBaseURL(server.URL)

	tok := &profile.Token{
		AccessToken: "test-token",
		Email:       "tester@example.com",
	}

	qInfo, err := client.Fetch(tok, true)
	if err != nil {
		t.Fatalf("fetch failed: %v", err)
	}

	if qInfo.Tier != "Pro" {
		t.Errorf("expected Pro tier, got %s", qInfo.Tier)
	}
	if qInfo.Gemini5hFraction != 0.85 {
		t.Errorf("expected 0.85 Gemini 5h, got %f", qInfo.Gemini5hFraction)
	}
	if qInfo.WeeklyFraction != 0.50 {
		t.Errorf("expected 0.50 Weekly, got %f", qInfo.WeeklyFraction)
	}
	if qInfo.Claude5hFraction != 0.90 {
		t.Errorf("expected 0.90 Claude 5h, got %f", qInfo.Claude5hFraction)
	}

	// Second fetch without forceRefresh should read from cache
	server.Close() // close server so network would fail
	cachedInfo, err := client.Fetch(tok, false)
	if err != nil {
		t.Fatalf("cached fetch failed: %v", err)
	}
	if cachedInfo.Tier != "Pro" {
		t.Errorf("expected cached Pro tier, got %s", cachedInfo.Tier)
	}
}

func TestClient_Fetch_InvalidJSONHandled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`corrupted-not-json`))
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	paths := &config.Paths{
		DataDir: tmpDir,
	}

	client := quota.NewClient(paths)
	client.SetBaseURL(server.URL)

	tok := &profile.Token{
		AccessToken: "test-token",
		Email:       "corrupt@example.com",
	}

	qInfo, err := client.Fetch(tok, true)
	// Should not panic, returns error or empty tier
	if err == nil && qInfo.Tier != "?" {
		t.Errorf("expected error or unknown tier, got %v", qInfo)
	}
}

func TestClient_Fetch_PerModelRetrieveUserQuota(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1internal:loadCodeAssist":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"paidTier": map[string]interface{}{
					"id":   "g1-pro-tier",
					"name": "Google AI Pro",
				},
			})
		case "/v1internal:retrieveUserQuotaSummary":
			// Real Google API: only Claude & GPT models returned here
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"groups": []interface{}{
					map[string]interface{}{
						"displayName": "Claude and GPT models",
						"buckets": []interface{}{
							map[string]interface{}{
								"window":            "weekly",
								"remainingFraction": 0.99,
							},
						},
					},
				},
			})
		case "/v1internal:retrieveUserQuota":
			// Real Google API: Gemini models returned here!
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"buckets": []interface{}{
					map[string]interface{}{
						"modelId":           "gemini-2.5-pro",
						"remainingFraction": 0.325,
						"resetTime":         "2026-11-17T06:05:00Z",
					},
					map[string]interface{}{
						"modelId":           "claude-sonnet-5-5-medium",
						"remainingFraction": 1.0,
						"resetTime":         "2026-11-17T10:53:30Z",
					},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	paths := &config.Paths{
		DataDir: tmpDir,
	}

	client := quota.NewClient(paths)
	client.SetBaseURL(server.URL)

	tok := &profile.Token{
		AccessToken: "test-token",
		Email:       "gemini-user@example.com",
	}

	qInfo, err := client.Fetch(tok, true)
	if err != nil {
		t.Fatalf("fetch failed: %v", err)
	}

	if qInfo.Tier != "Pro" {
		t.Errorf("expected Pro tier, got %s", qInfo.Tier)
	}
	if !qInfo.HasGemini {
		t.Errorf("expected HasGemini true")
	}
	if qInfo.Gemini5hFraction != 0.325 {
		t.Errorf("expected 0.325 Gemini 5h, got %f", qInfo.Gemini5hFraction)
	}
	if !qInfo.HasClaude {
		t.Errorf("expected HasClaude true")
	}
	if qInfo.Claude5hFraction != 1.0 {
		t.Errorf("expected 1.0 Claude 5h, got %f", qInfo.Claude5hFraction)
	}
	if qInfo.WeeklyFraction != 0.99 {
		t.Errorf("expected 0.99 Weekly, got %f", qInfo.WeeklyFraction)
	}

	// Verify ClearCached
	client.ClearCached("gemini-user@example.com")
	// Subsequent read without forceRefresh should miss cache and attempt fetch, but server is now closed
	server.Close()
	_, err = client.Fetch(tok, false)
	if err == nil {
		t.Errorf("expected error or cache miss after ClearCached, but got none")
	}
}
