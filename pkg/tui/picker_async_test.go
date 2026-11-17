package tui_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ZhangNy301/agy-switch/pkg/config"
	"github.com/ZhangNy301/agy-switch/pkg/profile"
	"github.com/ZhangNy301/agy-switch/pkg/quota"
	"github.com/ZhangNy301/agy-switch/pkg/tui"
)

func TestPickerUsesCacheWithoutNetwork(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		fmt.Fprint(w, `{}`)
	}))
	defer server.Close()
	dir := t.TempDir()
	qc := quota.NewClient(&config.Paths{DataDir: dir})
	qc.SetBaseURL(server.URL)
	cache, err := json.Marshal(map[string]quota.QuotaInfo{
		"old@test.local": {Tier: "Pro", HasGemini: true, Gemini5hFraction: .75, UpdatedAt: time.Now().Add(-time.Hour)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "quota_cache.json"), cache, 0600); err != nil {
		t.Fatal(err)
	}
	m := tui.NewPickerModel([]profile.Profile{
		{Name: "cached", Email: "old@test.local", Token: profile.Token{Email: "old@test.local", AccessToken: "fake"}},
		{Name: "uncached", Token: profile.Token{Email: "new@test.local", AccessToken: "fake"}},
	}, qc)
	if requests.Load() != 0 {
		t.Fatal("picker made a network request before startup")
	}
	if view := m.View(); !strings.Contains(view, "75%") || !strings.Contains(view, "[Pro]") {
		t.Fatal("stale cache was not rendered")
	}
	if cmd := m.Init(); cmd == nil {
		t.Fatal("background fetch was not scheduled")
	}
}

func BenchmarkPickerStartup(b *testing.B) {
	for _, count := range []int{10, 100} {
		b.Run(fmt.Sprintf("profiles=%d", count), func(b *testing.B) {
			dir := b.TempDir()
			qc := quota.NewClient(&config.Paths{DataDir: dir})
			profiles := make([]profile.Profile, count)
			cache := make(map[string]quota.QuotaInfo, count)
			for i := range profiles {
				email := fmt.Sprintf("account%d@test.local", i)
				profiles[i] = profile.Profile{Name: fmt.Sprintf("account%d", i), Email: email}
				cache[email] = quota.QuotaInfo{Tier: "Pro", HasGemini: true, Gemini5hFraction: .75, UpdatedAt: time.Now()}
			}
			data, err := json.Marshal(cache)
			if err != nil {
				b.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "quota_cache.json"), data, 0600); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = tui.NewPickerModel(profiles, qc)
			}
		})
	}
}

// BenchmarkPickerColdStartup includes construction and the first cache read of a
// new client, rather than reusing an already decoded in-memory cache.
func BenchmarkPickerColdStartup(b *testing.B) {
	dir := b.TempDir()
	paths := &config.Paths{DataDir: dir}
	profiles := make([]profile.Profile, 100)
	cache := make(map[string]quota.QuotaInfo, 100)
	for i := range profiles {
		email := fmt.Sprintf("account%d@test.local", i)
		profiles[i] = profile.Profile{Name: fmt.Sprint(i), Email: email}
		cache[email] = quota.QuotaInfo{Tier: "Pro", HasGemini: true, Gemini5hFraction: .75, UpdatedAt: time.Now()}
	}
	data, err := json.Marshal(cache)
	if err != nil {
		b.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "quota_cache.json"), data, 0600); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = tui.NewPickerModel(profiles, quota.NewClient(paths))
	}
}
