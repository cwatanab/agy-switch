package quota

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ZhangNy301/agy-switch/pkg/config"
	"github.com/ZhangNy301/agy-switch/pkg/profile"
)

func TestOAuthCredentialsConfiguration(t *testing.T) {
	t.Setenv("AGY_SWITCH_OAUTH_CLIENT_ID", "")
	t.Setenv("AGY_SWITCH_OAUTH_CLIENT_SECRET", "")
	paths := &config.Paths{DataDir: t.TempDir()}
	filePair := &OAuthPair{ClientID: "file-client", ClientSecret: "file-secret"}
	if err := SaveOAuthPair(paths, filePair); err != nil {
		t.Fatal(err)
	}
	client := NewClient(paths)
	pair, err := client.oauthCredentials()
	if err != nil || *pair != *filePair {
		t.Fatalf("file configuration error: %v", err)
	}
	if info, err := os.Stat(OAuthCachePath(paths)); err != nil {
		t.Fatal(err)
	} else if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatalf("configuration permissions=%o, want 600", info.Mode().Perm())
	}
	t.Setenv("AGY_SWITCH_OAUTH_CLIENT_ID", "env-client")
	t.Setenv("AGY_SWITCH_OAUTH_CLIENT_SECRET", "env-secret")
	pair, err = client.oauthCredentials()
	if err != nil || pair.ClientID != "env-client" || pair.ClientSecret != "env-secret" {
		t.Fatalf("environment did not override file: %v", err)
	}
	t.Setenv("AGY_SWITCH_OAUTH_CLIENT_SECRET", "")
	if _, err := client.oauthCredentials(); err == nil {
		t.Fatal("partial environment configuration silently fell back to file")
	}
}

func TestOAuthRefreshUsesConfiguredClient(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if err := r.ParseForm(); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if r.Form.Get("client_id") != "test-client" || r.Form.Get("client_secret") != "test-secret" ||
			r.Form.Get("refresh_token") != "test-refresh" || r.Form.Get("grant_type") != "refresh_token" {
			t.Error("refresh did not send the configured client and account grant")
		}
		fmt.Fprint(w, `{"access_token":"new-access","expires_in":3600}`)
	}))
	defer server.Close()
	pair := &OAuthPair{ClientID: "test-client", ClientSecret: "test-secret"}
	client := NewClientWithOptions(nil, Options{OAuthURL: server.URL, OAuthPair: pair})
	pair.ClientID = "modified-after-construction"
	tok := &profile.Token{RefreshToken: "test-refresh"}
	if err := client.refreshAccessToken(context.Background(), tok); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || tok.AccessToken != "new-access" || time.Until(tok.ExpiresAt) < 59*time.Minute {
		t.Fatal("configured refresh did not update the access token and expiry")
	}
}

func TestMissingOAuthConfigurationKeepsCachedQuota(t *testing.T) {
	t.Setenv("AGY_SWITCH_OAUTH_CLIENT_ID", "")
	t.Setenv("AGY_SWITCH_OAUTH_CLIENT_SECRET", "")
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	paths := &config.Paths{DataDir: t.TempDir()}
	client := NewClientWithOptions(paths, Options{OAuthURL: server.URL})
	client.SetBaseURL(server.URL)
	if err := client.saveCached(context.Background(), "user@test.local", &QuotaInfo{Tier: "Pro", UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	info, err := client.Fetch(&profile.Token{Email: "user@test.local", RefreshToken: "test-refresh"}, true)
	if err == nil || !strings.Contains(err.Error(), "OAuth client not configured") {
		t.Fatalf("missing configuration error=%v", err)
	}
	if info == nil || info.Tier != "Pro" || calls.Load() != 0 {
		t.Fatal("missing configuration lost the cached quota or attempted HTTP")
	}
}

func TestOAuthConfigurationRejectsInvalidFile(t *testing.T) {
	for _, data := range []string{`null`, `{}`, `{"client_id":"only-id"}`, `not-json`} {
		paths := &config.Paths{DataDir: t.TempDir()}
		if err := os.WriteFile(OAuthCachePath(paths), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadCachedOAuthPair(paths); err == nil {
			t.Error("accepted incomplete or malformed OAuth configuration")
		}
	}
	paths := &config.Paths{DataDir: filepath.Join(t.TempDir(), "new-directory")}
	pair := &OAuthPair{ClientID: "file-client", ClientSecret: "file-secret"}
	if err := SaveOAuthPair(paths, pair); err != nil {
		t.Fatal(err)
	}
	if err := SaveOAuthPair(paths, &OAuthPair{ClientID: "incomplete"}); err == nil {
		t.Fatal("saved incomplete configuration")
	}
	got, err := LoadCachedOAuthPair(paths)
	if err != nil || *got != *pair {
		t.Fatalf("invalid save changed the original configuration: %v", err)
	}
}

func writeFakeAgyBinary(t *testing.T, dir string, chunks ...struct {
	offset int
	data   string
}) string {
	t.Helper()
	binPath := filepath.Join(dir, "agy")
	buf := bytes.Repeat([]byte{'X'}, 8*1024*1024+1024)
	for _, c := range chunks {
		copy(buf[c.offset:], c.data)
	}
	if err := os.WriteFile(binPath, buf, 0755); err != nil {
		t.Fatal(err)
	}
	return binPath
}

func TestOAuthAutoDiscoveryFromBinary(t *testing.T) {
	t.Setenv("AGY_SWITCH_OAUTH_CLIENT_ID", "")
	t.Setenv("AGY_SWITCH_OAUTH_CLIENT_SECRET", "")

	bogusID := "111111-bogus.apps.googleusercontent.com"
	bogusSecret := "GOCSPX-" + strings.Repeat("a", 28)
	validID := "222222-valid_client.apps.googleusercontent.com"
	validSecret := "GOCSPX-" + strings.Repeat("b", 28)

	binPath := writeFakeAgyBinary(t, t.TempDir(),
		struct {
			offset int
			data   string
		}{offset: 128, data: bogusID + " " + bogusSecret},
		struct {
			offset int
			data   string
		}{offset: 4*1024*1024 - 10, data: validID},
		struct {
			offset int
			data   string
		}{offset: 8*1024*1024 - 10, data: validSecret},
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			if err := r.ParseForm(); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if r.Form.Get("client_id") == validID && r.Form.Get("client_secret") == validSecret {
				fmt.Fprint(w, `{"access_token":"discovered-access","expires_in":3600}`)
				return
			}
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":"invalid_client"}`)
		case "/v1internal:loadCodeAssist":
			if r.Header.Get("Authorization") != "Bearer discovered-access" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			fmt.Fprint(w, `{"paidTier":{"id":"pro-tier"}}`)
		case "/v1internal:retrieveUserQuotaSummary":
			fmt.Fprint(w, `{"groups":[{"displayName":"Gemini","buckets":[{"window":"5h","remainingFraction":0.8}]}]}`)
		default:
			fmt.Fprint(w, `{}`)
		}
	}))
	defer server.Close()

	paths := &config.Paths{DataDir: t.TempDir()}
	client := NewClientWithOptions(paths, Options{
		OAuthURL: server.URL + "/token",
		FindAgy:  func(*config.Paths) (string, error) { return binPath, nil },
	})
	client.SetBaseURL(server.URL)

	info, err := client.Fetch(&profile.Token{Email: "auto@test.local", RefreshToken: "rt-1"}, true)
	if err != nil {
		t.Fatalf("auto-discovery fetch failed: %v", err)
	}
	if info.Tier != "Pro" || !info.HasGemini || info.Gemini5hFraction != 0.8 {
		t.Fatalf("unexpected quota info: %+v", info)
	}

	if fi, err := os.Stat(OAuthCachePath(paths)); err != nil {
		t.Fatal(err)
	} else if runtime.GOOS != "windows" && fi.Mode().Perm() != 0600 {
		t.Fatalf("oauth_client.json permissions=%o, want 0600", fi.Mode().Perm())
	}
	saved, err := LoadCachedOAuthPair(paths)
	if err != nil || saved.ClientID != validID || saved.ClientSecret != validSecret {
		t.Fatalf("cached pair=%+v, err=%v", saved, err)
	}

	// Subsequent client must reuse cached oauth_client.json without scanning again.
	client2 := NewClientWithOptions(paths, Options{
		OAuthURL: server.URL + "/token",
		FindAgy:  func(*config.Paths) (string, error) { return "", errors.New("should not scan") },
	})
	client2.SetBaseURL(server.URL)
	info2, err := client2.Fetch(&profile.Token{Email: "auto2@test.local", RefreshToken: "rt-2"}, true)
	if err != nil || info2.Tier != "Pro" {
		t.Fatalf("second fetch using cached oauth_client.json failed: info=%+v, err=%v", info2, err)
	}
}

func TestOAuthAutoDiscoveryReplacesStaleCachedPair(t *testing.T) {
	t.Setenv("AGY_SWITCH_OAUTH_CLIENT_ID", "")
	t.Setenv("AGY_SWITCH_OAUTH_CLIENT_SECRET", "")

	stalePair := &OAuthPair{ClientID: "000000-stale.apps.googleusercontent.com", ClientSecret: "GOCSPX-" + strings.Repeat("z", 28)}
	validID := "333333-fresh.apps.googleusercontent.com"
	validSecret := "GOCSPX-" + strings.Repeat("c", 28)

	binPath := writeFakeAgyBinary(t, t.TempDir(),
		struct {
			offset int
			data   string
		}{offset: 1024, data: validID + "\x00" + validSecret},
	)

	paths := &config.Paths{DataDir: t.TempDir()}
	if err := SaveOAuthPair(paths, stalePair); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			_ = r.ParseForm()
			if r.Form.Get("client_id") == validID && r.Form.Get("client_secret") == validSecret {
				fmt.Fprint(w, `{"access_token":"fresh-token","expires_in":3600}`)
				return
			}
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/v1internal:loadCodeAssist" {
			fmt.Fprint(w, `{"paidTier":{"id":"pro-tier"}}`)
			return
		}
		fmt.Fprint(w, `{}`)
	}))
	defer server.Close()

	client := NewClientWithOptions(paths, Options{
		OAuthURL: server.URL + "/token",
		FindAgy:  func(*config.Paths) (string, error) { return binPath, nil },
	})
	client.SetBaseURL(server.URL)

	if _, err := client.Fetch(&profile.Token{Email: "stale@test.local", RefreshToken: "rt"}, true); err != nil {
		t.Fatalf("fetch with stale cached pair failed: %v", err)
	}
	got, err := LoadCachedOAuthPair(paths)
	if err != nil || got.ClientID != validID || got.ClientSecret != validSecret {
		t.Fatalf("stale cached pair was not replaced: got=%+v, err=%v", got, err)
	}
}

func TestFetchRefreshesOn401WhenExpiryMissing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			fmt.Fprint(w, `{"access_token":"fresh","expires_in":3600}`)
			return
		}
		if r.Header.Get("Authorization") != "Bearer fresh" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/v1internal:loadCodeAssist" {
			fmt.Fprint(w, `{"paidTier":{"id":"pro-tier"}}`)
			return
		}
		fmt.Fprint(w, `{}`)
	}))
	defer server.Close()

	client := NewClientWithOptions(nil, Options{
		OAuthURL:  server.URL + "/token",
		OAuthPair: &OAuthPair{ClientID: "cid", ClientSecret: "csec"},
	})
	client.SetBaseURL(server.URL)

	info, err := client.Fetch(&profile.Token{AccessToken: "expired", RefreshToken: "rt"}, true)
	if err != nil {
		t.Fatalf("401 refresh fallback failed: %v", err)
	}
	if info.Tier != "Pro" {
		t.Fatalf("tier=%q, want Pro", info.Tier)
	}
}

func BenchmarkScanOAuthStrings(b *testing.B) {
	dir := b.TempDir()
	binPath := filepath.Join(dir, "agy")
	buf := bytes.Repeat([]byte{'X'}, 8*1024*1024)
	copy(buf[4*1024*1024-10:], "222222-valid_client.apps.googleusercontent.com")
	copy(buf[6*1024*1024:], "GOCSPX-"+strings.Repeat("b", 28))
	if err := os.WriteFile(binPath, buf, 0755); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ids, secrets, err := ScanOAuthStrings(binPath)
		if err != nil || len(ids) == 0 || len(secrets) == 0 {
			b.Fatalf("scan failed: ids=%v secrets=%v err=%v", ids, secrets, err)
		}
	}
}

func TestOAuthRefreshUpdatesIDTokenEmailAndRevoke(t *testing.T) {
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"email":"real-owner@example.com"}`))
	idToken := "hdr." + payload + ".sig"

	var revokedToken string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		switch r.URL.Path {
		case "/token":
			fmt.Fprintf(w, `{"access_token":"fresh-at","refresh_token":"rotated-rt","expires_in":3600,"id_token":%q}`, idToken)
		case "/revoke":
			revokedToken = r.Form.Get("token")
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := NewClientWithOptions(nil, Options{
		OAuthURL:  server.URL + "/token",
		RevokeURL: server.URL + "/revoke",
		OAuthPair: &OAuthPair{ClientID: "cid", ClientSecret: "csec"},
	})
	tok := &profile.Token{RefreshToken: "old-rt", Email: "stale@example.com"}
	if err := client.ForceRefreshToken(context.Background(), tok); err != nil {
		t.Fatalf("ForceRefreshToken failed: %v", err)
	}
	if tok.Email != "real-owner@example.com" || tok.RefreshToken != "rotated-rt" || tok.IDToken != idToken {
		t.Fatalf("token identity not updated: %+v", tok)
	}
	if err := client.RevokeToken(context.Background(), tok.RefreshToken); err != nil {
		t.Fatalf("RevokeToken failed: %v", err)
	}
	if revokedToken != "rotated-rt" {
		t.Fatalf("revokedToken = %q, want rotated-rt", revokedToken)
	}
}
