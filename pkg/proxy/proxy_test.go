package proxy_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ZhangNy301/agy-switch/pkg/config"
	"github.com/ZhangNy301/agy-switch/pkg/proxy"
)

func TestStore_SaveListSetActiveDelete(t *testing.T) {
	tmpDir := t.TempDir()
	paths := &config.Paths{
		DataDir: tmpDir,
	}

	store := proxy.NewStore(paths)

	cfg := proxy.Config{
		Name:     "sg-proxy",
		Server:   "1.2.3.4",
		Port:     1080,
		Protocol: "socks5h",
		Active:   false,
	}

	if err := store.Save(cfg); err != nil {
		t.Fatalf("save failed: %v", err)
	}

	list, err := store.List()
	if err != nil {
		t.Fatalf("list failed: %v", err)
	}
	if len(list) != 1 || list[0].Name != "sg-proxy" {
		t.Fatalf("unexpected list: %+v", list)
	}

	if err := store.SetActive("sg-proxy", true); err != nil {
		t.Fatalf("set active failed: %v", err)
	}

	active, err := store.Active()
	if err != nil {
		t.Fatalf("get active failed: %v", err)
	}
	if active == nil || active.Name != "sg-proxy" {
		t.Fatalf("expected active sg-proxy, got %+v", active)
	}

	if err := store.Delete("sg-proxy"); err != nil {
		t.Fatalf("delete failed: %v", err)
	}
	listAfter, _ := store.List()
	if len(listAfter) != 0 {
		t.Errorf("expected empty list after delete, got %v", listAfter)
	}
}

func TestRaceProtocol_TimeoutUnreachable(t *testing.T) {
	// Attempt to race against a port that refuses connection
	start := time.Now()
	_, err := proxy.RaceProtocol("127.0.0.1", 54321, "", "")
	elapsed := time.Since(start)

	if err == nil {
		t.Errorf("expected error for unreachable server, got nil")
	}
	if elapsed > 5*time.Second {
		t.Errorf("race took %v, expected under 5s", elapsed)
	}
}

func TestProbeGeo_MockServer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"country": "US",
		})
	}))
	defer server.Close()

	cc, blocked, err := proxy.ProbeGeoWithURL("", server.URL)
	if err != nil {
		t.Fatalf("probe failed: %v", err)
	}
	if cc != "US" {
		t.Errorf("expected US, got %s", cc)
	}
	if blocked {
		t.Errorf("expected not blocked for US")
	}
}

func TestProbeGeo_BlockedCountry(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"country": "CN",
		})
	}))
	defer server.Close()

	cc, blocked, err := proxy.ProbeGeoWithURL("", server.URL)
	if err != nil {
		t.Fatalf("probe failed: %v", err)
	}
	if cc != "CN" {
		t.Errorf("expected CN, got %s", cc)
	}
	if !blocked {
		t.Errorf("expected blocked for CN")
	}
}
