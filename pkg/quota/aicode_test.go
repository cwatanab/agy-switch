package quota

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ZhangNy301/agy-switch/pkg/config"
	"github.com/ZhangNy301/agy-switch/pkg/profile"
)

func pbTag(field uint64, wire uint8) []byte {
	var buf [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(buf[:], (field<<3)|uint64(wire))
	return buf[:n]
}

func pbVarint(field uint64, v uint64) []byte {
	var buf [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(buf[:], v)
	return append(pbTag(field, 0), buf[:n]...)
}

func pbBytes(field uint64, data []byte) []byte {
	var buf [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(buf[:], uint64(len(data)))
	out := append(pbTag(field, 2), buf[:n]...)
	return append(out, data...)
}

func pbString(field uint64, s string) []byte {
	return pbBytes(field, []byte(s))
}

func pbFloat32(field uint64, v float32) []byte {
	out := pbTag(field, 5)
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], math.Float32bits(v))
	return append(out, b[:]...)
}

func pbBucket(id, desc, window string, frac *float32, disabled bool, resetUnix int64) []byte {
	var b bytes.Buffer
	b.Write(pbString(1, id))
	if desc != "" {
		b.Write(pbString(3, desc))
	}
	if window != "" {
		b.Write(pbString(4, window))
	}
	if frac != nil {
		b.Write(pbFloat32(5, *frac))
	}
	if disabled {
		b.Write(pbVarint(6, 1))
	}
	if resetUnix > 0 {
		b.Write(pbBytes(7, pbVarint(1, uint64(resetUnix))))
	}
	return b.Bytes()
}

func pbGroup(displayName string, buckets ...[]byte) []byte {
	var b bytes.Buffer
	b.Write(pbString(1, displayName))
	for _, bk := range buckets {
		b.Write(pbBytes(3, bk))
	}
	return b.Bytes()
}

func grpcFrame(compressed bool, msg []byte) []byte {
	payload := msg
	var flag byte
	if compressed {
		flag = 1
		var gz bytes.Buffer
		w := gzip.NewWriter(&gz)
		_, _ = w.Write(msg)
		_ = w.Close()
		payload = gz.Bytes()
	}
	var hdr [5]byte
	hdr[0] = flag
	binary.BigEndian.PutUint32(hdr[1:5], uint32(len(payload)))
	return append(hdr[:], payload...)
}

func f32Ptr(v float32) *float32 { return &v }

func TestDecodeAICodeFetchQuotaStatus(t *testing.T) {
	resetW := int64(1796086062)
	reset5 := int64(1795602908)

	// Pro tier with 5h hit and weekly remaining (description says weekly does not currently apply)
	summaryMsg := bytes.Join([][]byte{
		pbString(1, "summary desc"),
		pbBytes(2, pbGroup("Gemini Models",
			pbBucket("gemini-weekly", "You have hit your 5-hour limit, so the weekly limit does not currently apply.", "weekly", f32Ptr(0.45), false, resetW),
			pbBucket("gemini-5h", "You have hit your 5-hour limit", "5h", nil, false, reset5),
		)),
		pbBytes(2, pbGroup("Claude and GPT models",
			pbBucket("3p-weekly", "", "weekly", f32Ptr(1.0), false, resetW),
			pbBucket("3p-5h", "", "5h", f32Ptr(0.8), true, reset5),
		)),
	}, nil)
	top := pbBytes(2, summaryMsg)

	for _, gz := range []bool{false, true} {
		sum, err := decodeAICodeFetchQuotaStatus(grpcFrame(gz, top))
		if err != nil {
			t.Fatalf("gz=%v decode failed: %v", gz, err)
		}
		info := &QuotaInfo{}
		applyAICodeSummary(info, sum)

		if !info.HasGemini || info.Gemini5hFraction != 0 || info.Gemini5hDisabled {
			t.Fatalf("unexpected gemini 5h: %+v", info)
		}
		if !info.HasGeminiWeekly || math.Abs(info.GeminiWeeklyFraction-0.45) > 0.001 || info.GeminiWeeklyDisabled {
			t.Fatalf("expected GeminiWeeklyFraction ~0.45 and not disabled, got %+v", info)
		}
		if !info.HasClaude || !info.Claude5hDisabled || math.Abs(info.Claude5hFraction-0.8) > 0.001 {
			t.Fatalf("unexpected claude 5h: %+v", info)
		}
		if !info.HasClaudeWeekly || info.ClaudeWeeklyFraction != 1.0 {
			t.Fatalf("unexpected claude weekly: %+v", info)
		}
	}
}

func TestClient_Fetch_AICodeGRPCAndFallback(t *testing.T) {
	resetW := time.Now().Add(5 * 24 * time.Hour).Unix()
	reset5 := time.Now().Add(4 * time.Hour).Unix()
	frame := grpcFrame(false, pbBytes(2, bytes.Join([][]byte{
		pbBytes(2, pbGroup("Gemini Models",
			pbBucket("gemini-weekly", "", "weekly", f32Ptr(0.44), false, resetW),
			pbBucket("gemini-5h", "", "5h", f32Ptr(0.55), false, reset5),
		)),
		pbBytes(2, pbGroup("Claude and GPT models",
			pbBucket("3p-weekly", "", "weekly", f32Ptr(1.0), false, resetW),
			pbBucket("3p-5h", "", "5h", f32Ptr(1.0), false, reset5),
		)),
	}, nil)))

	var tokenCalls atomic.Int32
	cloudSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			tokenCalls.Add(1)
			fmt.Fprint(w, `{"access_token":"fresh-at","expires_in":3600}`)
		case "/v1internal:loadCodeAssist":
			if r.Header.Get("Authorization") != "Bearer fresh-at" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"paidTier": map[string]interface{}{"id": "g1-pro-tier"}})
		case "/v1internal:retrieveUserQuotaSummary":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"groups": []interface{}{
					map[string]interface{}{
						"displayName": "Claude",
						"buckets":     []interface{}{map[string]interface{}{"window": "weekly", "remainingFraction": 0.9}},
					},
				},
			})
		default:
			_ = json.NewEncoder(w).Encode(map[string]interface{}{})
		}
	}))
	defer cloudSrv.Close()

	var failAICode atomic.Bool
	aiSrv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fresh-at" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if failAICode.Load() {
			w.Header().Set("Content-Type", "application/grpc")
			w.Header().Set("Grpc-Status", "13")
			w.Header().Set("Grpc-Message", "internal error")
			return
		}
		w.Header().Set("Content-Type", "application/grpc")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(frame)
	}))
	aiSrv.EnableHTTP2 = true
	aiSrv.StartTLS()
	defer aiSrv.Close()

	client := NewClientWithOptions(&config.Paths{DataDir: t.TempDir()}, Options{
		HTTPClient: aiSrv.Client(),
		OAuthURL:   cloudSrv.URL + "/token",
		OAuthPair:  &OAuthPair{ClientID: "cid", ClientSecret: "csec"},
	})
	client.SetBaseURL(cloudSrv.URL)
	client.SetAICodeURL(aiSrv.URL)

	// 1. AICode succeeds + triggers 401 refresh automatically
	info, err := client.Fetch(&profile.Token{AccessToken: "stale-at", RefreshToken: "rt", Email: "pro@test.local"}, true)
	if err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}
	if tokenCalls.Load() != 1 || info.Tier != "Pro" {
		t.Fatalf("expected 1 token refresh and Pro tier, got calls=%d tier=%s", tokenCalls.Load(), info.Tier)
	}
	if !info.HasGeminiWeekly || math.Abs(info.GeminiWeeklyFraction-0.44) > 0.001 || !info.HasGemini || math.Abs(info.Gemini5hFraction-0.55) > 0.001 {
		t.Fatalf("expected both Gemini 5h and weekly from AICode, got %+v", info)
	}

	// 2. AICode gRPC failure falls back cleanly to CloudCode
	failAICode.Store(true)
	info2, err := client.Fetch(&profile.Token{AccessToken: "fresh-at", RefreshToken: "rt", Email: "fallback@test.local"}, true)
	if err != nil {
		t.Fatalf("fallback Fetch failed: %v", err)
	}
	if !info2.HasClaudeWeekly || math.Abs(info2.ClaudeWeeklyFraction-0.9) > 0.001 {
		t.Fatalf("expected CloudCode fallback quota, got %+v", info2)
	}
}

func TestClient_Fetch_SkipsLoadCodeAssistWhenTierCachedAndRetriesGRPC14(t *testing.T) {
	resetW := time.Now().Add(5 * 24 * time.Hour).Unix()
	frame := grpcFrame(false, pbBytes(2, bytes.Join([][]byte{
		pbBytes(2, pbGroup("Gemini Models",
			pbBucket("gemini-weekly", "", "weekly", f32Ptr(0.75), false, resetW),
		)),
	}, nil)))

	var loadCalls, aiCalls atomic.Int32
	cloudSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1internal:loadCodeAssist" {
			loadCalls.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"paidTier": map[string]interface{}{"id": "g1-ultra-tier"}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{})
	}))
	defer cloudSrv.Close()

	aiSrv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := aiCalls.Add(1)
		w.Header().Set("Content-Type", "application/grpc")
		if n == 1 {
			w.Header().Set("Grpc-Status", "14")
			w.Header().Set("Grpc-Message", "unavailable")
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(frame)
	}))
	aiSrv.EnableHTTP2 = true
	aiSrv.StartTLS()
	defer aiSrv.Close()

	paths := &config.Paths{DataDir: t.TempDir()}
	client := NewClientWithOptions(paths, Options{HTTPClient: aiSrv.Client()})
	client.SetBaseURL(cloudSrv.URL)
	client.SetAICodeURL(aiSrv.URL)

	// Seed an expired cache entry with known Tier="Pro"
	if err := client.saveCached(t.Context(), "cached@test.local", &QuotaInfo{
		Tier:      "Pro",
		Project:   "proj-1",
		UpdatedAt: time.Now().Add(-time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	// Non-forced expired fetch should reuse cached Tier without calling loadCodeAssist,
	// and retry gRPC status 14 once.
	info, err := client.Fetch(&profile.Token{AccessToken: "at", Email: "cached@test.local"}, false)
	if err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}
	if loadCalls.Load() != 0 || aiCalls.Load() != 2 || info.Tier != "Pro" || !info.HasGeminiWeekly {
		t.Fatalf("unexpected non-forced result: loadCalls=%d aiCalls=%d info=%+v", loadCalls.Load(), aiCalls.Load(), info)
	}

	// Forced refresh should call loadCodeAssist and pick up Ultra tier.
	infoForced, err := client.Fetch(&profile.Token{AccessToken: "at", Email: "cached@test.local"}, true)
	if err != nil {
		t.Fatalf("forced Fetch failed: %v", err)
	}
	if loadCalls.Load() != 1 || infoForced.Tier != "Ultra" {
		t.Fatalf("expected loadCodeAssist call on force=true, got loadCalls=%d tier=%s", loadCalls.Load(), infoForced.Tier)
	}
}
