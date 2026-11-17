package quota

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ZhangNy301/agy-switch/pkg/config"
	"github.com/ZhangNy301/agy-switch/pkg/profile"
)

func writeTestQuota(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/v1internal:loadCodeAssist" {
		fmt.Fprint(w, `{"paidTier":{"id":"pro-tier"}}`)
		return
	}
	fmt.Fprint(w, `{}`)
}

func TestFetchCoalescesConcurrentRefreshes(t *testing.T) {
	const callers = 12
	release := make(chan struct{})
	var once sync.Once
	finish := func() { once.Do(func() { close(release) }) }
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); <-release; writeTestQuota(w, r) }))
	defer server.Close()
	defer finish()
	client := NewClient(nil)
	client.SetBaseURL(server.URL)
	tok := &profile.Token{AccessToken: "fake", Email: "same@test.local"}
	results := make(chan error, callers)
	for i := 0; i < callers; i++ {
		go func() { _, err := client.Fetch(tok, true); results <- err }()
	}
	deadline := time.Now().Add(2 * time.Second)
	ready := false
	for time.Now().Before(deadline) {
		client.flightMu.Lock()
		for _, f := range client.flights {
			ready = f.waiters == callers
		}
		client.flightMu.Unlock()
		if ready {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !ready {
		t.Fatal("concurrent callers did not join the shared fetch")
	}
	finish()
	for i := 0; i < callers; i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if got := requests.Load(); got != 3 {
		t.Fatalf("HTTP requests=%d, want 3 for one shared refresh", got)
	}
	if tok.AccessToken != "fake" || !tok.ExpiresAt.IsZero() {
		t.Fatal("fetch mutated the caller's token")
	}
}

func TestFetchParallelizesEndpoints(t *testing.T) {
	started := make(chan struct{}, 3)
	release := make(chan struct{})
	var once sync.Once
	finish := func() { once.Do(func() { close(release) }) }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { started <- struct{}{}; <-release; writeTestQuota(w, r) }))
	defer server.Close()
	defer finish()
	client := NewClient(nil)
	client.SetBaseURL(server.URL)
	done := make(chan error, 1)
	go func() { _, err := client.Fetch(&profile.Token{AccessToken: "fake"}, true); done <- err }()
	for i := 0; i < 3; i++ {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("endpoints were not requested concurrently")
		}
	}
	finish()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestFetchLimitsHTTPConcurrency(t *testing.T) {
	var active, maxActive atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := active.Add(1)
		defer active.Add(-1)
		for old := maxActive.Load(); n > old; old = maxActive.Load() {
			if maxActive.CompareAndSwap(old, n) {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
		writeTestQuota(w, r)
	}))
	defer server.Close()
	client := NewClientWithOptions(nil, Options{MaxConcurrentHTTP: 2})
	client.SetBaseURL(server.URL)
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := client.Fetch(&profile.Token{AccessToken: fmt.Sprint(i), Email: fmt.Sprintf("%d@test.local", i)}, true)
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if got := maxActive.Load(); got != 2 {
		t.Fatalf("maximum simultaneous HTTP requests=%d, want 2", got)
	}
}

func TestFetchCancellationStopsRequests(t *testing.T) {
	started := make(chan struct{}, 3)
	canceled := make(chan struct{}, 3)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		started <- struct{}{}
		<-r.Context().Done()
		canceled <- struct{}{}
	}))
	defer server.Close()
	client := NewClient(nil)
	client.SetBaseURL(server.URL)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := client.FetchContext(ctx, &profile.Token{AccessToken: "fake"}, true); done <- err }()
	for i := 0; i < 3; i++ {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("request did not start")
		}
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("fetch error=%v, want context cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("fetch did not cancel")
	}
	for i := 0; i < 3; i++ {
		select {
		case <-canceled:
		case <-time.After(2 * time.Second):
			t.Fatal("abandoned HTTP request kept running")
		}
	}
}

func TestOAuthRefreshHonorsOverallDeadline(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
	}))
	defer server.Close()
	client := NewClientWithOptions(nil, Options{Timeout: 50 * time.Millisecond, OAuthURL: server.URL,
		OAuthPair: &OAuthPair{ClientID: "test-client", ClientSecret: "test-secret"}})
	_, err := client.Fetch(&profile.Token{RefreshToken: "fake-refresh"}, true)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("refresh error=%v, want deadline exceeded", err)
	}
}

func TestFetchRetriesOnlyTransientStatus(t *testing.T) {
	for _, status := range []int{http.StatusServiceUnavailable, http.StatusUnauthorized} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1internal:loadCodeAssist" && calls.Add(1) == 1 {
					w.WriteHeader(status)
					return
				}
				writeTestQuota(w, r)
			}))
			defer server.Close()
			client := NewClient(nil)
			client.SetBaseURL(server.URL)
			_, _ = client.Fetch(&profile.Token{AccessToken: "fake"}, true)
			want := int32(1)
			if status == http.StatusServiceUnavailable {
				want = 2
			}
			if got := calls.Load(); got != want {
				t.Fatalf("attempts=%d, want %d", got, want)
			}
		})
	}
}

func TestIndependentClientsPreserveAllCacheEntries(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(writeTestQuota))
	defer server.Close()
	paths := &config.Paths{DataDir: t.TempDir()}
	var wg sync.WaitGroup
	const accounts = 12
	for i := 0; i < accounts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			client := NewClient(paths)
			client.SetBaseURL(server.URL)
			_, err := client.Fetch(&profile.Token{AccessToken: "fake", Email: fmt.Sprintf("%d@test.local", i)}, true)
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	data, err := os.ReadFile(filepath.Join(paths.DataDir, "quota_cache.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cache map[string]QuotaInfo
	if err := json.Unmarshal(data, &cache); err != nil {
		t.Fatal(err)
	}
	if len(cache) != accounts {
		t.Fatalf("saved accounts=%d, want %d", len(cache), accounts)
	}
}

func TestCachedSnapshotObservesExternalUpdates(t *testing.T) {
	paths := &config.Paths{DataDir: t.TempDir()}
	first, second := NewClient(paths), NewClient(paths)
	if err := first.saveCached(context.Background(), "first", &QuotaInfo{Tier: "Pro", UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	snapshot := first.CachedAll()
	if err := second.saveCached(context.Background(), "second", &QuotaInfo{Tier: "Free", UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if got := len(first.CachedAll()); got != 2 {
		t.Fatalf("snapshot has %d accounts after external update, want 2", got)
	}
	if len(snapshot) != 1 {
		t.Fatal("an earlier snapshot was mutated")
	}
	if err := second.ClearCached("first"); err != nil {
		t.Fatal(err)
	}
	if _, ok := first.Cached("first"); ok {
		t.Fatal("external cache deletion was not observed")
	}
}

// BenchmarkQuotaFetchLatency compares the same three HTTP reads with 100ms of
// server latency per endpoint. It does not contact the Google API.
func BenchmarkQuotaFetchLatency(b *testing.B) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { time.Sleep(100 * time.Millisecond); writeTestQuota(w, r) }))
	defer server.Close()
	for _, mode := range []string{"sequential", "parallel"} {
		b.Run(mode, func(b *testing.B) {
			client := NewClient(nil)
			client.SetBaseURL(server.URL)
			tok := &profile.Token{AccessToken: "fake"}
			load := map[string]interface{}{"metadata": map[string]interface{}{"ideType": "ANTIGRAVITY", "pluginType": "GEMINI"}}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if mode == "parallel" {
					if _, err := client.Fetch(tok, true); err != nil {
						b.Fatal(err)
					}
					continue
				}
				tier, err := client.postJSON(context.Background(), tok.AccessToken, "/v1internal:loadCodeAssist", load)
				if err != nil {
					b.Fatal(err)
				}
				summary, err := client.postJSON(context.Background(), tok.AccessToken, "/v1internal:retrieveUserQuotaSummary", nil)
				if err != nil {
					b.Fatal(err)
				}
				models, err := client.postJSON(context.Background(), tok.AccessToken, "/v1internal:retrieveUserQuota", map[string]interface{}{})
				if err != nil {
					b.Fatal(err)
				}
				info := &QuotaInfo{Tier: "?", UpdatedAt: time.Now()}
				applyTier(info, tier)
				applySummary(info, summary)
				applyModels(info, models)
			}
		})
	}
}
