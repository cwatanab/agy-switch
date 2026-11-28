package quota_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
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
	jsonPayload := `spinner...
{"status":"SUCCESS","command":{"name":"usage","data":{"groups":[{"name":"Gemini Models","buckets":[{"id":"gemini-weekly","window":"weekly","remaining_fraction":0.688,"reset_time":"2026-12-03T07:02:37Z","description":"You have hit your 5-hour limit, so the weekly limit does not currently apply."},{"id":"gemini-5h","window":"5h","remaining_fraction":0,"reset_time":"2026-11-28T04:21:31Z","description":"You have hit your 5-hour limit"}]},{"name":"Claude and GPT models","buckets":[{"id":"3p-weekly","window":"weekly","remaining_fraction":1,"reset_time":"1970-01-01T00:00:00Z"},{"id":"3p-5h","window":"5h","remaining_fraction":0.85,"reset_time":"2026-11-28T06:00:00Z"}]}]}}}`

	var calls int
	client := quota.NewClientWithOptions(&config.Paths{DataDir: t.TempDir()}, quota.Options{
		RunUsage: func(ctx context.Context, tok *profile.Token) ([]byte, *profile.Token, error) {
			calls++
			return []byte(jsonPayload), nil, nil
		},
	})

	tok := &profile.Token{
		AccessToken: "test-token",
		Email:       "tester@example.com",
	}

	qInfo, err := client.Fetch(tok, true)
	if err != nil {
		t.Fatalf("fetch failed: %v", err)
	}
	if !qInfo.HasGemini || qInfo.Gemini5hFraction != 0 || qInfo.Gemini5hDisabled {
		t.Fatalf("unexpected Gemini 5h: %+v", qInfo)
	}
	if !qInfo.HasGeminiWeekly || math.Abs(qInfo.GeminiWeeklyFraction-0.688) > 1e-6 || qInfo.GeminiWeeklyDisabled {
		t.Fatalf("unexpected Gemini weekly: %+v", qInfo)
	}
	if !qInfo.HasClaude || math.Abs(qInfo.Claude5hFraction-0.85) > 1e-6 {
		t.Fatalf("unexpected Claude 5h: %+v", qInfo)
	}
	if !qInfo.HasClaudeWeekly || qInfo.ClaudeWeeklyFraction != 1.0 || qInfo.ClaudeWeeklyReset != "" {
		t.Fatalf("expected 1970 reset_time stripped on Claude weekly, got %+v", qInfo)
	}

	// Second fetch without force uses cache without calling RunUsage.
	cachedInfo, err := client.Fetch(tok, false)
	if err != nil {
		t.Fatalf("cached fetch failed: %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected 1 RunUsage call (cache hit on 2nd), got %d", calls)
	}
	if math.Abs(cachedInfo.GeminiWeeklyFraction-0.688) > 1e-6 {
		t.Fatalf("unexpected cached weekly: %+v", cachedInfo)
	}

	// ClearCached forces next fetch to invoke RunUsage.
	if err := client.ClearCached(tok.Email); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Fetch(tok, false); err != nil {
		t.Fatalf("fetch after ClearCached failed: %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected 2 RunUsage calls after ClearCached, got %d", calls)
	}
}

func TestClient_Fetch_Disabled5hFlag(t *testing.T) {
	jsonPayload := `{"status":"SUCCESS","command":{"name":"usage","data":{"groups":[{"name":"Gemini Models","buckets":[{"id":"gemini-5h","window":"5h","remaining_fraction":1,"description":"You have hit your weekly limit, the 5-hour limit does not currently apply"},{"id":"gemini-weekly","window":"weekly","remaining_fraction":0,"reset_time":"2026-12-03T07:00:00Z"}]},{"name":"Claude and GPT models","buckets":[{"id":"3p-5h","window":"5h","remaining_fraction":0.8,"disabled":true},{"id":"3p-weekly","window":"weekly","remaining_fraction":0.5,"reset_time":"2026-12-03T07:00:00Z"}]}]}}}`
	client := quota.NewClientWithOptions(&config.Paths{DataDir: t.TempDir()}, quota.Options{
		RunUsage: func(context.Context, *profile.Token) ([]byte, *profile.Token, error) {
			return []byte(jsonPayload), nil, nil
		},
	})
	info, err := client.Fetch(&profile.Token{AccessToken: "tok", Email: "disabled@example.com"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Gemini5hDisabled || !info.Claude5hDisabled {
		t.Fatalf("expected Gemini5hDisabled and Claude5hDisabled true, got %+v", info)
	}
	if info.GeminiWeeklyDisabled || info.ClaudeWeeklyDisabled {
		t.Fatalf("expected weekly not disabled, got %+v", info)
	}
}

func TestClient_Fetch_DoesNotOverwriteCacheWithIncompleteQuota(t *testing.T) {
	validJSON := `{"status":"SUCCESS","command":{"name":"usage","data":{"groups":[{"name":"Gemini Models","buckets":[{"id":"gemini-5h","window":"5h","remaining_fraction":0.75}]}]}}}`

	t.Run("preserves valid cache when RunUsage fails", func(t *testing.T) {
		paths := &config.Paths{DataDir: t.TempDir()}
		var fail bool
		client := quota.NewClientWithOptions(paths, quota.Options{
			RunUsage: func(context.Context, *profile.Token) ([]byte, *profile.Token, error) {
				if fail {
					return nil, nil, errors.New("agy failed")
				}
				return []byte(validJSON), nil, nil
			},
		})
		tok := &profile.Token{AccessToken: "tok", Email: "user@example.com"}
		if _, err := client.Fetch(tok, true); err != nil {
			t.Fatalf("seed fetch failed: %v", err)
		}

		fail = true
		info, err := client.Fetch(tok, true)
		if err == nil {
			t.Fatal("expected error when RunUsage fails")
		}
		if info == nil || !info.HasGemini || info.Gemini5hFraction != 0.75 {
			t.Fatalf("expected preserved cached quota, got %+v", info)
		}
		cached, ok := client.Cached(tok.Email)
		if !ok || !cached.HasGemini || cached.Gemini5hFraction != 0.75 {
			t.Fatalf("disk cache overwritten: %+v", cached)
		}
	})

	t.Run("preserves valid cache when groups are empty", func(t *testing.T) {
		paths := &config.Paths{DataDir: t.TempDir()}
		var empty bool
		client := quota.NewClientWithOptions(paths, quota.Options{
			RunUsage: func(context.Context, *profile.Token) ([]byte, *profile.Token, error) {
				if empty {
					return []byte(`{"status":"SUCCESS","command":{"name":"usage","data":{"groups":[]}}}`), nil, nil
				}
				return []byte(validJSON), nil, nil
			},
		})
		tok := &profile.Token{AccessToken: "tok", Email: "user@example.com"}
		if _, err := client.Fetch(tok, true); err != nil {
			t.Fatalf("seed fetch failed: %v", err)
		}

		empty = true
		info, err := client.Fetch(tok, true)
		if err == nil {
			t.Fatal("expected error when /usage returns no quota data")
		}
		if info == nil || info.Gemini5hFraction != 0.75 {
			t.Fatalf("expected preserved cached quota, got %+v", info)
		}
	})

	t.Run("does not cache on failure when cache is empty", func(t *testing.T) {
		paths := &config.Paths{DataDir: t.TempDir()}
		client := quota.NewClientWithOptions(paths, quota.Options{
			RunUsage: func(context.Context, *profile.Token) ([]byte, *profile.Token, error) {
				return []byte(`not-json`), nil, nil
			},
		})
		tok := &profile.Token{AccessToken: "tok", Email: "nocache@example.com"}
		if _, err := client.Fetch(tok, true); err == nil {
			t.Fatal("expected error on invalid JSON")
		}
		if cached, ok := client.Cached(tok.Email); ok {
			t.Fatalf("expected no cached entry, got %+v", cached)
		}
	})
}

func TestClient_Fetch_PropagatesRefreshedToken(t *testing.T) {
	validJSON := `{"status":"SUCCESS","command":{"name":"usage","data":{"groups":[{"name":"Gemini Models","buckets":[{"id":"gemini-5h","window":"5h","remaining_fraction":0.5}]}]}}}`
	var callbackTok *profile.Token
	client := quota.NewClientWithOptions(&config.Paths{DataDir: t.TempDir()}, quota.Options{
		RunUsage: func(context.Context, *profile.Token) ([]byte, *profile.Token, error) {
			return []byte(validJSON), &profile.Token{
				AccessToken:  "fresh-at",
				RefreshToken: "fresh-rt",
				Email:        "user@example.com",
				ExpiresAt:    time.Now().Add(time.Hour),
			}, nil
		},
		OnTokenRefreshed: func(tok *profile.Token) {
			callbackTok = tok
		},
	})

	orig := &profile.Token{AccessToken: "old-at", RefreshToken: "old-rt", Email: "user@example.com"}
	info, err := client.Fetch(orig, true)
	if err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}
	if orig.AccessToken != "old-at" {
		t.Fatal("Fetch mutated caller's token")
	}
	if info.RefreshedToken == nil || info.RefreshedToken.AccessToken != "fresh-at" {
		t.Fatalf("expected RefreshedToken in QuotaInfo, got %+v", info.RefreshedToken)
	}
	if callbackTok == nil || callbackTok.AccessToken != "fresh-at" {
		t.Fatalf("expected OnTokenRefreshed callback, got %+v", callbackTok)
	}
}

func TestClient_RunAgyUsage_IsolatedHomeAndTokenRefresh(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix shell fixture")
	}
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args.txt")
	homeFile := filepath.Join(dir, "home.txt")
	permFile := filepath.Join(dir, "perm.txt")
	script := filepath.Join(dir, "mock-agy")
	scriptBody := `#!/bin/sh
printf '%s\n' "$@" > "$TEST_ARGS_FILE"
printf '%s' "$HOME" > "$TEST_HOME_FILE"
TOK="$HOME/.gemini/antigravity-cli/antigravity-oauth-token"
ls -l "$TOK" > "$TEST_PERM_FILE"
cat <<'JSON' > "$TOK"
{"auth_method":"consumer","id_token_email":"iso@example.com","token":{"access_token":"refreshed-by-agy","refresh_token":"rt-1","expiry":"2099-01-01T00:00:00Z"}}
JSON
echo '{"status":"SUCCESS","command":{"name":"usage","data":{"groups":[{"name":"Gemini Models","buckets":[{"id":"gemini-5h","window":"5h","remaining_fraction":0.42},{"id":"gemini-weekly","window":"weekly","remaining_fraction":0.84}]}]}}}'
`
	if err := os.WriteFile(script, []byte(scriptBody), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEST_ARGS_FILE", argsFile)
	t.Setenv("TEST_HOME_FILE", homeFile)
	t.Setenv("TEST_PERM_FILE", permFile)

	// Set a sentinel live HOME to prove runAgyUsage does not touch it.
	realHome := filepath.Join(dir, "real-home")
	if err := os.MkdirAll(realHome, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", realHome)

	paths := &config.Paths{DataDir: filepath.Join(dir, "data")}
	client := quota.NewClientWithOptions(paths, quota.Options{
		FindAgy: func(*config.Paths) (string, error) { return script, nil },
		ProxyEnv: func() ([]string, error) {
			return []string{"HTTPS_PROXY=http://127.0.0.1:8888"}, nil
		},
	})

	tok := &profile.Token{AccessToken: "old-at", RefreshToken: "rt-1", Email: "iso@example.com"}
	info, err := client.Fetch(tok, true)
	if err != nil {
		t.Fatalf("Fetch with mock agy failed: %v", err)
	}
	if math.Abs(info.Gemini5hFraction-0.42) > 1e-6 || math.Abs(info.GeminiWeeklyFraction-0.84) > 1e-6 {
		t.Fatalf("unexpected fractions: %+v", info)
	}
	if info.RefreshedToken == nil || info.RefreshedToken.AccessToken != "refreshed-by-agy" {
		t.Fatalf("expected refreshed token from isolated file, got %+v", info.RefreshedToken)
	}

	gotArgs, err := os.ReadFile(argsFile)
	if err != nil || string(gotArgs) != "--output-format\njson\n-p\n/usage\n" {
		t.Fatalf("unexpected agy args: %q (%v)", gotArgs, err)
	}
	gotHome, err := os.ReadFile(homeFile)
	if err != nil || string(gotHome) == realHome {
		t.Fatalf("agy used real HOME instead of isolated temp dir: %q", gotHome)
	}
	if _, err := os.Stat(string(gotHome)); !os.IsNotExist(err) {
		t.Fatalf("expected isolated temp home %q to be cleaned up", gotHome)
	}
	if gotPerm, err := os.ReadFile(permFile); err != nil || !strings.HasPrefix(string(gotPerm), "-rw-------") {
		t.Fatalf("expected 0600 token permissions, got %q (%v)", gotPerm, err)
	}
}

type mockKeyringStore struct {
	mu         sync.Mutex
	path       string
	writeCalls atomic.Int32
}

func (m *mockKeyringStore) ReadLive() ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return os.ReadFile(m.path)
}

func (m *mockKeyringStore) WriteLive(data []byte) error {
	m.writeCalls.Add(1)
	m.mu.Lock()
	defer m.mu.Unlock()
	return os.WriteFile(m.path, data, 0600)
}

func (m *mockKeyringStore) DeleteLive() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	err := os.Remove(m.path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func TestClient_RunAgyUsage_KeyringSwapSerializesAndRestores(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix shell fixture")
	}
	dir := t.TempDir()
	keyringPath := filepath.Join(dir, "keyring-live.json")
	origLive := `{"auth_method":"consumer","id_token_email":"live@example.com","token":{"access_token":"at-live","refresh_token":"rt-live","expiry":"2099-01-01T00:00:00Z"}}`
	if err := os.WriteFile(keyringPath, []byte(origLive), 0600); err != nil {
		t.Fatal(err)
	}
	store := &mockKeyringStore{path: keyringPath}

	script := filepath.Join(dir, "mock-agy-keyring")
	scriptBody := `#!/bin/sh
# Simulate agy reading OS keyring instead of $HOME token file.
DATA=$(cat "$TEST_KEYRING_FILE")
case "$DATA" in
  *rt-1*)
    cat <<'JSON' > "$TEST_KEYRING_FILE"
{"auth_method":"consumer","id_token_email":"a1@example.com","token":{"access_token":"refreshed-at-1","refresh_token":"rt-1","expiry":"2099-01-01T00:00:00Z"}}
JSON
    echo '{"status":"SUCCESS","command":{"name":"usage","data":{"groups":[{"name":"Gemini Models","buckets":[{"id":"gemini-5h","window":"5h","remaining_fraction":0.11},{"id":"gemini-weekly","window":"weekly","remaining_fraction":0.21}]}]}}}'
    ;;
  *rt-2*)
    echo '{"status":"SUCCESS","command":{"name":"usage","data":{"groups":[{"name":"Gemini Models","buckets":[{"id":"gemini-5h","window":"5h","remaining_fraction":0.22},{"id":"gemini-weekly","window":"weekly","remaining_fraction":0.32}]}]}}}'
    ;;
  *rt-live*)
    echo '{"status":"SUCCESS","command":{"name":"usage","data":{"groups":[{"name":"Gemini Models","buckets":[{"id":"gemini-5h","window":"5h","remaining_fraction":0.99},{"id":"gemini-weekly","window":"weekly","remaining_fraction":0.88}]}]}}}'
    ;;
  *)
    echo '{"status":"ERROR"}'
    exit 1
    ;;
esac
`
	if err := os.WriteFile(script, []byte(scriptBody), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEST_KEYRING_FILE", keyringPath)

	useSwap := true
	paths := &config.Paths{DataDir: filepath.Join(dir, "data")}
	client := quota.NewClientWithOptions(paths, quota.Options{
		TokenStore:     store,
		UseKeyringSwap: &useSwap,
		FindAgy:        func(*config.Paths) (string, error) { return script, nil },
	})

	tokLive := &profile.Token{AccessToken: "at-live", RefreshToken: "rt-live", Email: "live@example.com"}
	infoLiveOnly, err := client.Fetch(tokLive, true)
	if err != nil {
		t.Fatalf("Fetch live account failed: %v", err)
	}
	if math.Abs(infoLiveOnly.Gemini5hFraction-0.99) > 1e-6 {
		t.Fatalf("unexpected live fraction: %+v", infoLiveOnly)
	}
	if writes := store.writeCalls.Load(); writes != 0 {
		t.Fatalf("expected 0 keyring writes when matching live grant, got %d", writes)
	}

	tok1 := &profile.Token{AccessToken: "at-1", RefreshToken: "rt-1", Email: "a1@example.com"}
	tok2 := &profile.Token{AccessToken: "at-2", RefreshToken: "rt-2", Email: "a2@example.com"}

	var wg sync.WaitGroup
	var res1, res2, res3 *quota.QuotaInfo
	var err1, err2, err3 error
	for i, fn := range []func(){
		func() { res1, err1 = client.Fetch(tok1, true) },
		func() { res2, err2 = client.Fetch(tok2, true) },
		func() { res3, err3 = client.Fetch(tokLive, true) },
	} {
		_ = i
		wg.Add(1)
		go func(run func()) {
			defer wg.Done()
			run()
		}(fn)
	}
	wg.Wait()

	if err1 != nil || err2 != nil || err3 != nil {
		t.Fatalf("concurrent keyring-swapped fetches failed: %v / %v / %v", err1, err2, err3)
	}
	if math.Abs(res1.Gemini5hFraction-0.11) > 1e-6 || math.Abs(res2.Gemini5hFraction-0.22) > 1e-6 || math.Abs(res3.Gemini5hFraction-0.99) > 1e-6 {
		t.Fatalf("accounts mixed up quotas: r1=%.2f r2=%.2f r3=%.2f", res1.Gemini5hFraction, res2.Gemini5hFraction, res3.Gemini5hFraction)
	}
	if res1.RefreshedToken == nil || res1.RefreshedToken.AccessToken != "refreshed-at-1" {
		t.Fatalf("expected refreshed token captured before keyring restore, got %+v", res1.RefreshedToken)
	}

	afterLive, err := store.ReadLive()
	if err != nil || string(afterLive) != origLive {
		t.Fatalf("live keyring was not restored: got %q (%v)", string(afterLive), err)
	}
}

func TestClient_RevokeToken(t *testing.T) {
	var gotBody string
	var statusToReturn = http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(statusToReturn)
	}))
	defer srv.Close()

	client := quota.NewClientWithOptions(&config.Paths{DataDir: t.TempDir()}, quota.Options{
		RevokeURL: srv.URL,
	})
	if err := client.RevokeToken(context.Background(), "rt-xyz"); err != nil {
		t.Fatalf("RevokeToken failed: %v", err)
	}
	if gotBody != "token=rt-xyz" {
		t.Fatalf("unexpected revoke payload: %q", gotBody)
	}

	statusToReturn = http.StatusBadRequest
	if err := client.RevokeToken(context.Background(), "rt-xyz"); err == nil {
		t.Fatal("expected error on HTTP 400 from revoke endpoint")
	}

	if err := client.RevokeToken(context.Background(), ""); err == nil {
		t.Fatal("expected error on empty refresh token")
	}
}

// Verify QuotaInfo JSON roundtrip preserves expected fields.
func TestQuotaInfo_JSONRoundTrip(t *testing.T) {
	in := quota.QuotaInfo{
		Tier:                 "Pro",
		Gemini5hFraction:     0.25,
		GeminiWeeklyFraction: 0.75,
		HasGemini:            true,
		HasGeminiWeekly:      true,
		Email:                "a@b.com",
		UpdatedAt:            time.Now().UTC().Truncate(time.Second),
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out quota.QuotaInfo
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out.Gemini5hFraction != 0.25 || out.GeminiWeeklyFraction != 0.75 || !out.HasGeminiWeekly {
		t.Fatalf("roundtrip mismatch: %+v", out)
	}
}
