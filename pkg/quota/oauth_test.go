package quota

import (
	"context"
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
