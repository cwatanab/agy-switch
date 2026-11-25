package tui_test

import (
	"context"
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
	tea "github.com/charmbracelet/bubbletea"
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

func TestPickerShowsQuotaRefreshError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
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
	}, qc)

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	m = updated.(tui.PickerModel)
	if cmd == nil {
		t.Fatal("expected refresh cmd on 'r' key")
	}
	if view := m.View(); !strings.Contains(view, "Refreshing quotas...") || !strings.Contains(view, "75%") {
		t.Fatalf("unexpected view during refresh: %s", view)
	}

	msg := cmd()
	updated, _ = m.Update(msg)
	m = updated.(tui.PickerModel)
	view := m.View()
	if !strings.Contains(view, "Quota refresh failed") || !strings.Contains(view, "75%") {
		t.Fatalf("expected error and retained cached quota in view, got: %s", view)
	}
}

func TestPickerTitleVersionAndDoubleBlankLine(t *testing.T) {
	orig := tui.Version
	tui.Version = "1.0.6"
	defer func() { tui.Version = orig }()

	m := tui.NewPickerModel([]profile.Profile{
		{Name: "account1", Email: "one@example.com"},
	}, nil)
	lines := strings.Split(m.View(), "\n")
	if len(lines) < 4 {
		t.Fatalf("unexpected view lines: %q", lines)
	}
	if !strings.Contains(lines[0], "✦ agy-switch v1.0.6 — Account Picker") {
		t.Fatalf("title line=%q, want versioned title", lines[0])
	}
	if strings.TrimSpace(lines[1]) != "" || strings.TrimSpace(lines[2]) != "" {
		t.Fatalf("expected two blank lines after title, got line 1=%q line 2=%q", lines[1], lines[2])
	}
	if !strings.Contains(lines[3], "account1") {
		t.Fatalf("expected first profile on line 3, got %q", lines[3])
	}
}

func TestPickerAsyncNetCheckWarning(t *testing.T) {
	m := tui.NewPickerModelWithOptions(
		[]profile.Profile{{Name: "a1", Email: "a1@example.com"}},
		nil,
		tui.PickerOptions{
			NetCheckFn: func(context.Context) (string, string) { return "region", "CN" },
		},
	)
	cmd := m.Init()
	if cmd == nil {
		t.Fatal("expected Init cmd for NetCheckFn")
	}
	updated, _ := m.Update(cmd())
	m = updated.(tui.PickerModel)
	if view := m.View(); !strings.Contains(view, "IP region (CN) is not supported") {
		t.Fatalf("expected async region warning in view, got:\n%s", view)
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

func BenchmarkPickerView(b *testing.B) {
	dir := b.TempDir()
	paths := &config.Paths{DataDir: dir}
	profiles := make([]profile.Profile, 100)
	cache := make(map[string]quota.QuotaInfo, 100)
	for i := range profiles {
		email := fmt.Sprintf("account%d@test.local", i)
		profiles[i] = profile.Profile{Name: fmt.Sprintf("account%d", i), Email: email}
		cache[email] = quota.QuotaInfo{Tier: "Pro", HasGemini: true, Gemini5hFraction: .75, HasClaude: true, Claude5hFraction: 1.0, UpdatedAt: time.Now()}
	}
	data, err := json.Marshal(cache)
	if err != nil {
		b.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "quota_cache.json"), data, 0600); err != nil {
		b.Fatal(err)
	}
	m := tui.NewPickerModel(profiles, quota.NewClient(paths))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if len(m.View()) == 0 {
			b.Fatal("empty view")
		}
	}
}
