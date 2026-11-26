package quota_test

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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

func TestClient_Fetch_DoesNotOverwriteCacheWithIncompleteQuota(t *testing.T) {
	t.Run("preserves valid cache when quota endpoints fail", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/v1internal:loadCodeAssist":
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"paidTier": map[string]interface{}{"id": "starter", "name": "Antigravity Starter Quota"},
				})
			default:
				w.WriteHeader(http.StatusInternalServerError)
			}
		}))
		defer server.Close()

		paths := &config.Paths{DataDir: t.TempDir()}
		// Seed valid cache via an initial full response.
		seedServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/v1internal:loadCodeAssist":
				_ = json.NewEncoder(w).Encode(map[string]interface{}{"paidTier": map[string]interface{}{"id": "pro-tier"}})
			case "/v1internal:retrieveUserQuotaSummary":
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"groups": []interface{}{map[string]interface{}{
						"displayName": "Gemini",
						"buckets":     []interface{}{map[string]interface{}{"window": "5h", "remainingFraction": 0.75}},
					}},
				})
			default:
				_ = json.NewEncoder(w).Encode(map[string]interface{}{})
			}
		}))
		defer seedServer.Close()

		client := quota.NewClient(paths)
		client.SetBaseURL(seedServer.URL)
		tok := &profile.Token{AccessToken: "tok", Email: "user@example.com"}
		if _, err := client.Fetch(tok, true); err != nil {
			t.Fatalf("seed fetch failed: %v", err)
		}

		client.SetBaseURL(server.URL)
		info, err := client.Fetch(tok, true)
		if err == nil {
			t.Fatal("expected error when quota endpoints fail")
		}
		if info == nil || !info.HasGemini || info.Gemini5hFraction != 0.75 {
			t.Fatalf("expected preserved cached quota, got %+v", info)
		}
		cached, ok := client.Cached(tok.Email)
		if !ok || !cached.HasGemini || cached.Gemini5hFraction != 0.75 || cached.Tier != "Pro" {
			t.Fatalf("disk cache overwritten with incomplete quota: %+v", cached)
		}
	})

	t.Run("preserves valid cache when quota endpoints return empty", func(t *testing.T) {
		paths := &config.Paths{DataDir: t.TempDir()}
		seedServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/v1internal:loadCodeAssist":
				_ = json.NewEncoder(w).Encode(map[string]interface{}{"paidTier": map[string]interface{}{"id": "pro-tier"}})
			case "/v1internal:retrieveUserQuotaSummary":
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"groups": []interface{}{map[string]interface{}{
						"displayName": "Gemini",
						"buckets":     []interface{}{map[string]interface{}{"window": "5h", "remainingFraction": 0.75}},
					}},
				})
			default:
				_ = json.NewEncoder(w).Encode(map[string]interface{}{})
			}
		}))
		defer seedServer.Close()

		emptyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1internal:loadCodeAssist" {
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"paidTier": map[string]interface{}{"id": "starter", "name": "Antigravity Starter Quota"},
				})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{})
		}))
		defer emptyServer.Close()

		client := quota.NewClient(paths)
		client.SetBaseURL(seedServer.URL)
		tok := &profile.Token{AccessToken: "tok", Email: "user@example.com"}
		if _, err := client.Fetch(tok, true); err != nil {
			t.Fatalf("seed fetch failed: %v", err)
		}

		client.SetBaseURL(emptyServer.URL)
		info, err := client.Fetch(tok, true)
		if err == nil {
			t.Fatal("expected error when quota response drops model usage data")
		}
		if info == nil || info.Gemini5hFraction != 0.75 {
			t.Fatalf("expected preserved cached quota, got %+v", info)
		}
		if cached, ok := client.Cached(tok.Email); !ok || cached.Gemini5hFraction != 0.75 {
			t.Fatalf("disk cache was overwritten: %+v", cached)
		}
	})

	t.Run("does not cache tier-only result when quota endpoints fail on empty cache", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1internal:loadCodeAssist" {
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"paidTier": map[string]interface{}{"id": "starter", "name": "Antigravity Starter Quota"},
				})
				return
			}
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer server.Close()

		paths := &config.Paths{DataDir: t.TempDir()}
		client := quota.NewClient(paths)
		client.SetBaseURL(server.URL)
		tok := &profile.Token{AccessToken: "tok", Email: "nocache@example.com"}
		if _, err := client.FetchContext(context.Background(), tok, true); err == nil {
			t.Fatal("expected error when quota endpoints fail")
		}
		if cached, ok := client.Cached(tok.Email); ok {
			t.Fatalf("expected no cached entry, got %+v", cached)
		}
	})
}

func TestClient_Fetch_RetriesQuotaWithProjectOn403(t *testing.T) {
	var forbiddenCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1internal:loadCodeAssist":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"cloudaicompanionProject": "proj-123",
				"paidTier": map[string]interface{}{
					"id":   "free-tier",
					"name": "Antigravity Starter Quota",
				},
			})
		case "/v1internal:retrieveUserQuotaSummary":
			var body map[string]interface{}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["project"] != "proj-123" {
				forbiddenCount.Add(1)
				w.WriteHeader(http.StatusForbidden)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"groups": []interface{}{
					map[string]interface{}{
						"displayName": "Claude and GPT models",
						"buckets": []interface{}{
							map[string]interface{}{"window": "weekly", "remainingFraction": 0.9},
						},
					},
				},
			})
		case "/v1internal:retrieveUserQuota":
			var body map[string]interface{}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["project"] != "proj-123" {
				forbiddenCount.Add(1)
				w.WriteHeader(http.StatusForbidden)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"buckets": []interface{}{
					map[string]interface{}{"modelId": "gemini-2.5-pro", "remainingFraction": 0.4},
					map[string]interface{}{"modelId": "claude-sonnet-4-6", "remainingFraction": 1.0},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	paths := &config.Paths{DataDir: t.TempDir()}
	client := quota.NewClient(paths)
	client.SetBaseURL(server.URL)

	tok := &profile.Token{AccessToken: "tok", Email: "starter@example.com"}
	info, err := client.Fetch(tok, true)
	if err != nil {
		t.Fatalf("fetch failed: %v", err)
	}
	if info.Tier != "Free" {
		t.Fatalf("Tier=%q, want Free", info.Tier)
	}
	if !info.HasGemini || info.Gemini5hFraction != 0.4 {
		t.Fatalf("unexpected Gemini quota: %+v", info)
	}
	if !info.HasClaude || info.Claude5hFraction != 1.0 {
		t.Fatalf("unexpected Claude quota: %+v", info)
	}
	if info.Project != "proj-123" {
		t.Fatalf("Project=%q, want proj-123", info.Project)
	}

	forbiddenCount.Store(0)
	if _, err := client.Fetch(tok, true); err != nil {
		t.Fatalf("second forced fetch failed: %v", err)
	}
	if got := forbiddenCount.Load(); got != 0 {
		t.Fatalf("expected cached project to avoid 403 retries on second fetch, got %d 403s", got)
	}
}

func TestClient_Fetch_SeparateWeeklyAndDisabledBuckets(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1internal:loadCodeAssist":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"paidTier": map[string]interface{}{"id": "pro-tier"}})
		case "/v1internal:retrieveUserQuotaSummary":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"groups": []interface{}{
					map[string]interface{}{
						"displayName": "Gemini",
						"buckets": []interface{}{
							map[string]interface{}{
								"window":            "5h",
								"remainingFraction": 1.0,
								"description":       "You have hit your weekly limit, the 5-hour limit does not currently apply",
							},
							map[string]interface{}{
								"window":            "weekly",
								"remainingFraction": 0.0,
								"resetTime":         "2026-11-30T00:00:00Z",
							},
						},
					},
					map[string]interface{}{
						"displayName": "Claude",
						"buckets": []interface{}{
							map[string]interface{}{
								"window":            "5h",
								"remainingFraction": 0.65,
								"resetTime":         "2026-11-25T18:00:00Z",
							},
							map[string]interface{}{
								"window":            "weekly",
								"remainingFraction": 0.42,
								"resetTime":         "2026-12-01T00:00:00Z",
							},
						},
					},
				},
			})
		default:
			_ = json.NewEncoder(w).Encode(map[string]interface{}{})
		}
	}))
	defer server.Close()

	client := quota.NewClient(&config.Paths{DataDir: t.TempDir()})
	client.SetBaseURL(server.URL)
	info, err := client.Fetch(&profile.Token{AccessToken: "tok", Email: "weekly@example.com"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Gemini5hDisabled || !info.HasGeminiWeekly || info.GeminiWeeklyFraction != 0.0 {
		t.Fatalf("unexpected Gemini weekly/disabled state: %+v", info)
	}
	if !info.HasClaudeWeekly || info.ClaudeWeeklyFraction != 0.42 || info.ClaudeWeeklyReset != "2026-12-01T00:00:00Z" {
		t.Fatalf("unexpected Claude weekly state: %+v", info)
	}
}

func TestClient_Fetch_ClassifiesWeeklyVs5hModelBuckets(t *testing.T) {
	now := time.Now().UTC()
	weeklyReset := now.Add(6 * 24 * time.Hour).Format(time.RFC3339)
	fiveHourReset := now.Add(3 * time.Hour).Format(time.RFC3339)

	t.Run("free tier weekly buckets go to weekly without populating 5h", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/v1internal:loadCodeAssist":
				_ = json.NewEncoder(w).Encode(map[string]interface{}{"currentTier": map[string]interface{}{"id": "free-tier"}})
			case "/v1internal:retrieveUserQuotaSummary":
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"groups": []interface{}{
						map[string]interface{}{
							"displayName": "Claude and GPT models",
							"buckets":     []interface{}{map[string]interface{}{"window": "weekly", "remainingFraction": 1.0, "resetTime": weeklyReset}},
						},
					},
				})
			case "/v1internal:retrieveUserQuota":
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"buckets": []interface{}{
						map[string]interface{}{"modelId": "gemini-2.5-flash", "remainingFraction": 0.25, "resetTime": weeklyReset},
						map[string]interface{}{"modelId": "claude-sonnet-4-6", "remainingFraction": 0.80, "resetTime": weeklyReset},
					},
				})
			default:
				http.NotFound(w, r)
			}
		}))
		defer server.Close()

		client := quota.NewClient(&config.Paths{DataDir: t.TempDir()})
		client.SetBaseURL(server.URL)
		info, err := client.Fetch(&profile.Token{AccessToken: "tok", Email: "free@example.com"}, true)
		if err != nil {
			t.Fatal(err)
		}
		if info.HasGemini {
			t.Fatalf("expected HasGemini=false for weekly-only resetTime, got %+v", info)
		}
		if !info.HasGeminiWeekly || info.GeminiWeeklyFraction != 0.25 || info.GeminiWeeklyReset != weeklyReset {
			t.Fatalf("expected GeminiWeekly populated, got %+v", info)
		}
		if info.HasClaude {
			t.Fatalf("expected HasClaude=false for weekly-only resetTime, got %+v", info)
		}
		if !info.HasClaudeWeekly || info.ClaudeWeeklyFraction != 1.0 {
			t.Fatalf("expected ClaudeWeekly from summary preserved, got %+v", info)
		}
	})

	t.Run("pro tier 5h buckets go to 5h", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/v1internal:loadCodeAssist":
				_ = json.NewEncoder(w).Encode(map[string]interface{}{"paidTier": map[string]interface{}{"id": "g1-pro-tier"}})
			case "/v1internal:retrieveUserQuotaSummary":
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"groups": []interface{}{
						map[string]interface{}{
							"displayName": "Claude and GPT models",
							"buckets": []interface{}{
								map[string]interface{}{"window": "5h", "remainingFraction": 1.0, "resetTime": fiveHourReset},
								map[string]interface{}{"window": "weekly", "remainingFraction": 0.9, "resetTime": weeklyReset},
							},
						},
					},
				})
			case "/v1internal:retrieveUserQuota":
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"buckets": []interface{}{
						map[string]interface{}{"modelId": "gemini-2.5-flash", "remainingFraction": 0.4, "resetTime": fiveHourReset},
					},
				})
			default:
				http.NotFound(w, r)
			}
		}))
		defer server.Close()

		client := quota.NewClient(&config.Paths{DataDir: t.TempDir()})
		client.SetBaseURL(server.URL)
		info, err := client.Fetch(&profile.Token{AccessToken: "tok", Email: "pro@example.com"}, true)
		if err != nil {
			t.Fatal(err)
		}
		if !info.HasGemini || info.Gemini5hFraction != 0.4 || info.HasGeminiWeekly {
			t.Fatalf("unexpected Pro Gemini quota classification: %+v", info)
		}
	})
}

func TestClient_Fetch_DailyCloudCodeAndCacheAndBoolDisabled(t *testing.T) {
	now := time.Now().UTC()
	weeklyReset := now.Add(5 * 24 * time.Hour).Format(time.RFC3339)
	fiveHourReset := now.Add(4 * time.Hour).Format(time.RFC3339)

	t.Run("daily CloudCode shape without gemini-2.5-pro and skipping loadCodeAssist on non-forced expired refresh", func(t *testing.T) {
		var loadCalls, summaryCalls, modelsCalls atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/v1internal:loadCodeAssist":
				loadCalls.Add(1)
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"cloudaicompanionProject": "aicode-consumers",
					"paidTier":                map[string]interface{}{"id": "g1-ultra-tier"},
				})
			case "/v1internal:retrieveUserQuotaSummary":
				summaryCalls.Add(1)
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"groups": []interface{}{
						map[string]interface{}{
							"displayName": "Claude and GPT models",
							"buckets": []interface{}{
								map[string]interface{}{"bucketId": "3p-weekly", "window": "weekly", "remainingFraction": 1.0},
								map[string]interface{}{"bucketId": "3p-5h", "window": "5h", "remainingFraction": 1.0},
							},
						},
					},
				})
			case "/v1internal:retrieveUserQuota":
				modelsCalls.Add(1)
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"buckets": []interface{}{
						map[string]interface{}{"modelId": "claude-sonnet-5-5-medium", "remainingFraction": 1.0, "resetTime": fiveHourReset},
						map[string]interface{}{"modelId": "gemini-2.5-flash", "remainingFraction": 0.06663115, "resetTime": weeklyReset},
						map[string]interface{}{"modelId": "gemini-4-hydrogen-sonar", "remainingFraction": 0.06663115, "resetTime": weeklyReset},
					},
				})
			default:
				http.NotFound(w, r)
			}
		}))
		defer server.Close()

		tmpDir := t.TempDir()
		expiredCache, err := json.Marshal(map[string]quota.QuotaInfo{
			"daily@example.com": {
				Tier:      "Pro",
				Project:   "aicode-consumers",
				Email:     "daily@example.com",
				UpdatedAt: now.Add(-time.Hour),
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(tmpDir, "quota_cache.json"), expiredCache, 0600); err != nil {
			t.Fatal(err)
		}

		client := quota.NewClient(&config.Paths{DataDir: tmpDir})
		client.SetBaseURL(server.URL)
		tok := &profile.Token{AccessToken: "tok", Email: "daily@example.com"}

		// Non-forced expired cache fetch should skip loadCodeAssist (2 HTTP calls total).
		info, err := client.Fetch(tok, false)
		if err != nil {
			t.Fatalf("non-forced Fetch failed: %v", err)
		}
		if loadCalls.Load() != 0 || summaryCalls.Load() != 1 || modelsCalls.Load() != 1 {
			t.Fatalf("unexpected call counts for non-forced expired fetch: load=%d summary=%d models=%d", loadCalls.Load(), summaryCalls.Load(), modelsCalls.Load())
		}
		if info.Tier != "Pro" || info.Project != "aicode-consumers" {
			t.Fatalf("expected cached Tier=Pro and Project=aicode-consumers, got %+v", info)
		}
		if info.HasGemini {
			t.Fatalf("expected HasGemini=false when only weekly Gemini resetTime present, got %+v", info)
		}
		if !info.HasGeminiWeekly || math.Abs(info.GeminiWeeklyFraction-0.06663115) > 1e-6 || info.GeminiWeeklyReset != weeklyReset {
			t.Fatalf("expected GeminiWeeklyFraction ~0.06663115, got %+v", info)
		}
		if !info.HasClaude || info.Claude5hFraction != 1.0 || !info.HasClaudeWeekly || info.ClaudeWeeklyFraction != 1.0 {
			t.Fatalf("unexpected Claude quota: %+v", info)
		}

		// Forced refresh should call loadCodeAssist (3 HTTP calls total) and update Tier.
		loadCalls.Store(0)
		summaryCalls.Store(0)
		modelsCalls.Store(0)
		infoForced, err := client.Fetch(tok, true)
		if err != nil {
			t.Fatalf("forced Fetch failed: %v", err)
		}
		if loadCalls.Load() != 1 || summaryCalls.Load() != 1 || modelsCalls.Load() != 1 {
			t.Fatalf("unexpected call counts for forced fetch: load=%d summary=%d models=%d", loadCalls.Load(), summaryCalls.Load(), modelsCalls.Load())
		}
		if infoForced.Tier != "Ultra" {
			t.Fatalf("expected updated Tier=Ultra on forced fetch, got %q", infoForced.Tier)
		}
	})

	t.Run("boolean disabled in summary sets 5h disabled flags", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/v1internal:loadCodeAssist":
				_ = json.NewEncoder(w).Encode(map[string]interface{}{"paidTier": map[string]interface{}{"id": "g1-pro-tier"}})
			case "/v1internal:retrieveUserQuotaSummary":
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"groups": []interface{}{
						map[string]interface{}{
							"displayName": "Gemini Models",
							"buckets": []interface{}{
								map[string]interface{}{"window": "5h", "remainingFraction": 1.0, "disabled": true},
								map[string]interface{}{"window": "weekly", "remainingFraction": 0.0, "resetTime": weeklyReset},
							},
						},
						map[string]interface{}{
							"displayName": "Claude and GPT models",
							"buckets": []interface{}{
								map[string]interface{}{"window": "5h", "remainingFraction": 0.8, "disabled": true},
								map[string]interface{}{"window": "weekly", "remainingFraction": 0.0, "resetTime": weeklyReset},
							},
						},
					},
				})
			default:
				_ = json.NewEncoder(w).Encode(map[string]interface{}{})
			}
		}))
		defer server.Close()

		client := quota.NewClient(&config.Paths{DataDir: t.TempDir()})
		client.SetBaseURL(server.URL)
		info, err := client.Fetch(&profile.Token{AccessToken: "tok", Email: "disabled@example.com"}, true)
		if err != nil {
			t.Fatal(err)
		}
		if !info.Gemini5hDisabled || !info.Claude5hDisabled {
			t.Fatalf("expected Gemini5hDisabled and Claude5hDisabled true, got %+v", info)
		}
	})

	t.Run("sends updated User-Agent and matches bucketId-based Gemini/Claude groups", func(t *testing.T) {
		var mu sync.Mutex
		var seenUAs []string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			seenUAs = append(seenUAs, r.Header.Get("User-Agent"))
			mu.Unlock()
			switch r.URL.Path {
			case "/v1internal:loadCodeAssist":
				_ = json.NewEncoder(w).Encode(map[string]interface{}{"paidTier": map[string]interface{}{"id": "g1-pro-tier"}})
			case "/v1internal:retrieveUserQuotaSummary":
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"groups": []interface{}{
						map[string]interface{}{
							"displayName": "Google Models",
							"buckets": []interface{}{
								map[string]interface{}{"bucketId": "gemini-5h", "remainingFraction": 1.0, "resetTime": fiveHourReset},
								map[string]interface{}{"bucketId": "gemini-weekly", "remainingFraction": 0.41, "resetTime": weeklyReset},
							},
						},
						map[string]interface{}{
							"displayName": "Third Party Models",
							"buckets": []interface{}{
								map[string]interface{}{"bucketId": "3p-5h", "remainingFraction": 1.0, "resetTime": fiveHourReset},
								map[string]interface{}{"bucketId": "3p-weekly", "remainingFraction": 1.0, "resetTime": weeklyReset},
							},
						},
					},
				})
			default:
				_ = json.NewEncoder(w).Encode(map[string]interface{}{})
			}
		}))
		defer server.Close()

		client := quota.NewClient(&config.Paths{DataDir: t.TempDir()})
		client.SetBaseURL(server.URL)
		info, err := client.Fetch(&profile.Token{AccessToken: "tok", Email: "bucketid@example.com"}, true)
		if err != nil {
			t.Fatal(err)
		}
		if len(seenUAs) == 0 {
			t.Fatal("expected User-Agent header on outgoing requests")
		}
		for _, ua := range seenUAs {
			if !strings.HasPrefix(ua, "antigravity/1.5.") {
				t.Fatalf("expected User-Agent starting with antigravity/1.5., got %q", ua)
			}
		}
		if !info.HasGemini || info.Gemini5hFraction != 1.0 {
			t.Fatalf("expected Gemini 5h=1.0, got %+v", info)
		}
		if !info.HasGeminiWeekly || math.Abs(info.GeminiWeeklyFraction-0.41) > 1e-6 {
			t.Fatalf("expected Gemini weekly=0.41, got %+v", info)
		}
		if !info.HasClaude || info.Claude5hFraction != 1.0 || !info.HasClaudeWeekly || info.ClaudeWeeklyFraction != 1.0 {
			t.Fatalf("expected Claude 5h=1.0 and weekly=1.0, got %+v", info)
		}
	})
}
