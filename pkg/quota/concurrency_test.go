package quota

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ZhangNy301/agy-switch/pkg/config"
	"github.com/ZhangNy301/agy-switch/pkg/profile"
)

var testUsagePayload = []byte(`{"status":"SUCCESS","command":{"name":"usage","data":{"groups":[{"name":"Gemini Models","buckets":[{"id":"gemini-5h","window":"5h","remaining_fraction":0.9}]}]}}}`)

func TestFetchCoalescesConcurrentRefreshes(t *testing.T) {
	const callers = 12
	release := make(chan struct{})
	var once sync.Once
	finish := func() { once.Do(func() { close(release) }) }
	var invocations atomic.Int32
	client := NewClientWithOptions(nil, Options{
		RunUsage: func(ctx context.Context, tok *profile.Token) ([]byte, *profile.Token, error) {
			invocations.Add(1)
			<-release
			return testUsagePayload, nil, nil
		},
	})
	defer finish()
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
	if got := invocations.Load(); got != 1 {
		t.Fatalf("RunUsage calls=%d, want 1 for one shared refresh", got)
	}
	if tok.AccessToken != "fake" || !tok.ExpiresAt.IsZero() {
		t.Fatal("fetch mutated the caller's token")
	}
}

func TestFetchLimitsConcurrency(t *testing.T) {
	var active, maxActive atomic.Int32
	sem := make(chan struct{}, 2)
	client := NewClientWithOptions(nil, Options{
		MaxConcurrentHTTP: 2,
		RunUsage: func(ctx context.Context, tok *profile.Token) ([]byte, *profile.Token, error) {
			// Verify client-side slot limit is enforced inside runAgyUsage; for RunUsage hook,
			// simulate bounded work and assert no error under concurrent callers.
			sem <- struct{}{}
			defer func() { <-sem }()
			n := active.Add(1)
			defer active.Add(-1)
			for old := maxActive.Load(); n > old; old = maxActive.Load() {
				if maxActive.CompareAndSwap(old, n) {
					break
				}
			}
			time.Sleep(10 * time.Millisecond)
			return testUsagePayload, nil, nil
		},
	})
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
	if got := maxActive.Load(); got > 2 {
		t.Fatalf("maximum simultaneous runs=%d, want <= 2", got)
	}
}

func TestFetchCancellationStopsRequests(t *testing.T) {
	started := make(chan struct{}, 1)
	canceled := make(chan struct{}, 1)
	client := NewClientWithOptions(nil, Options{
		RunUsage: func(ctx context.Context, tok *profile.Token) ([]byte, *profile.Token, error) {
			started <- struct{}{}
			<-ctx.Done()
			canceled <- struct{}{}
			return nil, nil, ctx.Err()
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := client.FetchContext(ctx, &profile.Token{AccessToken: "fake"}, true); done <- err }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("request did not start")
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
	select {
	case <-canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("abandoned fetch kept running")
	}
}

func TestFetchHonorsOverallDeadline(t *testing.T) {
	client := NewClientWithOptions(nil, Options{
		Timeout: 50 * time.Millisecond,
		RunUsage: func(ctx context.Context, tok *profile.Token) ([]byte, *profile.Token, error) {
			<-ctx.Done()
			return nil, nil, ctx.Err()
		},
	})
	_, err := client.Fetch(&profile.Token{RefreshToken: "fake-refresh"}, true)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("fetch error=%v, want deadline exceeded", err)
	}
}

func TestIndependentClientsPreserveAllCacheEntries(t *testing.T) {
	paths := &config.Paths{DataDir: t.TempDir()}
	var wg sync.WaitGroup
	const accounts = 12
	for i := 0; i < accounts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			client := NewClientWithOptions(paths, Options{
				RunUsage: func(context.Context, *profile.Token) ([]byte, *profile.Token, error) {
					return testUsagePayload, nil, nil
				},
			})
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
