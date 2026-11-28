package proxy_test

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
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

func TestStore_RenameUpdateAndProbe(t *testing.T) {
	paths := &config.Paths{DataDir: t.TempDir()}
	store := proxy.NewStore(paths)

	if err := store.Save(proxy.Config{Name: "p1", Server: "1.2.3.4", Port: 1080, Protocol: "socks5h", Active: true}); err != nil {
		t.Fatal(err)
	}
	if err := store.Rename("p1", "p1-renamed"); err != nil {
		t.Fatalf("rename failed: %v", err)
	}
	active, err := store.Active()
	if err != nil || active == nil || active.Name != "p1-renamed" {
		t.Fatalf("active not updated on rename: %+v, err=%v", active, err)
	}
	if err := store.SaveProbe("p1-renamed", &proxy.ProbeResult{At: 100, OK: true, CC: "JP", City: "Tokyo", Kind: "residential/ISP"}); err != nil {
		t.Fatalf("save probe failed: %v", err)
	}
	updated := proxy.Config{Name: "p2", Server: "1.2.3.4", Port: 1080, Protocol: "socks5h", Username: "alice", Password: "secret"}
	if err := store.Update("p1-renamed", updated); err != nil {
		t.Fatalf("update failed: %v", err)
	}
	got, err := store.Get("p2")
	if err != nil || !got.Active || got.Check == nil || got.Check.CC != "JP" {
		t.Fatalf("unexpected updated proxy: %+v, err=%v", got, err)
	}
	if masked := got.RawURL(true); masked != "socks5h://al****:****@1.2.3.4:1080" {
		t.Fatalf("unexpected masked URL: %s", masked)
	}
}

func TestFromEnvIgnoresBridgeUser(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://agy-switch:secret@127.0.0.1:4321")
	if got := proxy.FromEnv(); got != nil {
		t.Fatalf("expected bridge URL to be ignored, got %+v", got)
	}
	t.Setenv("HTTPS_PROXY", "https://user1:pass1@proxy.example.com:8443")
	got := proxy.FromEnv()
	if got == nil || got.Host != "proxy.example.com" || got.Port != 8443 || got.Protocol != "https" || got.Username != "user1" {
		t.Fatalf("unexpected FromEnv result: %+v", got)
	}
}

func TestProbeProxyWithOptions(t *testing.T) {
	targetSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer targetSrv.Close()

	ipapiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"status":      "success",
			"query":       "1.2.3.4",
			"countryCode": "jp",
			"city":        "Tokyo",
			"isp":         "Example ISP",
			"hosting":     false,
			"mobile":      false,
			"proxy":       false,
		})
	}))
	defer ipapiSrv.Close()

	// Use an HTTP proxy server that directly serves requests via targetSrv/ipapiSrv?
	// Even simpler: test IPWHOIS fallback by pointing ipapi to 500 and ipwhois to mock,
	// with a local HTTP CONNECT/forward proxy mock.
	ipwhoisSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"success":      true,
			"ip":           "5.6.7.8",
			"country_code": "sg",
			"city":         "Singapore",
			"connection": map[string]interface{}{
				"asn": 15169,
				"isp": "Google LLC",
			},
		})
	}))
	defer ipwhoisSrv.Close()

	// Simple HTTP proxy handler (plain HTTP GET forwarding)
	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp, err := http.Get(r.URL.String())
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
	defer proxySrv.Close()

	host, portStr, err := net.SplitHostPort(strings.TrimPrefix(proxySrv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatal(err)
	}
	cfg := proxy.Config{Name: "local", Host: host, Port: port, Protocol: "http"}

	res := proxy.ProbeProxyWithOptions(context.Background(), cfg, proxy.ProbeOptions{
		TargetURL: targetSrv.URL,
		IPAPIURL:  ipapiSrv.URL,
	})
	if !res.OK || res.CC != "JP" || res.City != "Tokyo" || res.Kind != "residential/ISP" {
		t.Fatalf("unexpected ip-api probe result: %+v", res)
	}

	badAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer badAPI.Close()

	res2 := proxy.ProbeProxyWithOptions(context.Background(), cfg, proxy.ProbeOptions{
		TargetURL:  targetSrv.URL,
		IPAPIURL:   badAPI.URL,
		IPWhoisURL: ipwhoisSrv.URL,
	})
	if !res2.OK || res2.CC != "SG" || res2.Kind != "hosting" {
		t.Fatalf("unexpected ipwho.is fallback result: %+v", res2)
	}
}

func TestCheckNetwork_CachingAndStates(t *testing.T) {
	paths := &config.Paths{DataDir: t.TempDir()}
	store := proxy.NewStore(paths)

	var hits int
	targetSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
	}))
	defer targetSrv.Close()

	now := time.Now()
	state, _ := proxy.CheckNetwork(context.Background(), paths, store, proxy.NetCheckOptions{
		TargetURL:  targetSrv.URL,
		HTTPClient: targetSrv.Client(),
		Now:        now,
	})
	if state != proxy.NetStateOK || hits != 1 {
		t.Fatalf("unexpected CheckNetwork result: state=%s hits=%d", state, hits)
	}

	// Second call within TTL uses cache
	state2, _ := proxy.CheckNetwork(context.Background(), paths, store, proxy.NetCheckOptions{
		TargetURL:  targetSrv.URL,
		HTTPClient: targetSrv.Client(),
		Now:        now.Add(2 * time.Minute),
	})
	if state2 != proxy.NetStateOK || hits != 1 {
		t.Fatalf("expected cached ok result, got state=%s hits=%d", state2, hits)
	}

	// Changing active proxy changes NetTag and invalidates cache
	_ = store.Save(proxy.Config{Name: "us", Host: "10.0.0.1", Port: 8080, Protocol: "http", Active: true})
	targetSrv.Close()
	state3, _ := proxy.CheckNetwork(context.Background(), paths, store, proxy.NetCheckOptions{
		TargetURL:  targetSrv.URL,
		HTTPClient: targetSrv.Client(),
		Now:        now.Add(2 * time.Minute),
	})
	if state3 != proxy.NetStateBlocked {
		t.Fatalf("expected NetStateBlocked after proxy change to closed target, got %s", state3)
	}
}

func TestStore_CacheInvalidatesOnExternalWriteAndConcurrentReads(t *testing.T) {
	paths := &config.Paths{DataDir: t.TempDir()}
	store := proxy.NewStore(paths)

	if err := store.Save(proxy.Config{Name: "p1", Server: "1.1.1.1", Port: 8080, Protocol: "http", Active: true}); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				act, err := store.Active()
				if err != nil || act == nil || act.Name != "p1" {
					t.Errorf("unexpected concurrent Active(): %+v, err=%v", act, err)
					return
				}
			}
		}()
	}
	wg.Wait()

	// External modification with distinct mtime and size must invalidate cache.
	dbPath := filepath.Join(paths.DataDir, "proxy.json")
	external := []byte(`{"version":2,"active":"p2","proxies":{"p2":{"name":"p2","host":"2.2.2.2","port":9090,"protocol":"https"}}}`)
	if err := os.WriteFile(dbPath, external, 0600); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(2 * time.Second)
	_ = os.Chtimes(dbPath, future, future)

	act, err := store.Active()
	if err != nil || act == nil || act.Name != "p2" || act.Port != 9090 {
		t.Fatalf("expected cache invalidation on external write, got %+v (err=%v)", act, err)
	}
}
