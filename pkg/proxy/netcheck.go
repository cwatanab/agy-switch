package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ZhangNy301/agy-switch/pkg/config"
	"github.com/ZhangNy301/agy-switch/pkg/util"
)

const (
	NetStateOK      = "ok"
	NetStateBlocked = "blocked"
	NetStateRegion  = "region"

	netOKTTL  = 6 * time.Hour
	netBadTTL = 10 * time.Minute
)

type NetCheckOptions struct {
	TargetURL  string
	GeoURL     string
	HTTPClient *http.Client
	Now        time.Time
}

func netCheckPath(paths *config.Paths) string {
	if paths == nil || paths.DataDir == "" {
		return ""
	}
	return filepath.Join(paths.DataDir, ".netcheck")
}

// NetTag identifies the effective outbound network path ("P:host:port", "E:host:port", or "D").
func NetTag(store *Store, now time.Time) string {
	if store != nil {
		if active, err := store.Active(); err == nil && active != nil && active.State(now) == "on" {
			return fmt.Sprintf("P:%s:%d", active.GetServer(), active.Port)
		}
	}
	if envCfg := FromEnv(); envCfg != nil {
		return fmt.Sprintf("E:%s:%d", envCfg.GetServer(), envCfg.Port)
	}
	return "D"
}

// CheckNetwork checks Google AI API reachability and exit country support, caching results in .netcheck.
func CheckNetwork(ctx context.Context, paths *config.Paths, store *Store, opts NetCheckOptions) (string, string) {
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	tag := NetTag(store, now)
	cacheFile := netCheckPath(paths)
	if cacheFile != "" {
		if data, err := os.ReadFile(cacheFile); err == nil {
			fields := strings.Fields(string(data))
			if len(fields) >= 4 && fields[3] == tag {
				if epoch, err := strconv.ParseInt(fields[0], 10, 64); err == nil {
					ttl := netBadTTL
					if fields[1] == "1" {
						ttl = netOKTTL
					}
					if now.Sub(time.Unix(epoch, 0)) < ttl {
						cc := fields[2]
						if cc == "-" {
							cc = ""
						}
						switch fields[1] {
						case "0":
							return NetStateBlocked, cc
						case "1":
							return NetStateOK, cc
						default:
							return NetStateRegion, cc
						}
					}
				}
			}
		}
	}

	targetURL := opts.TargetURL
	if targetURL == "" {
		if env := os.Getenv("AGY_SWITCH_ENDPOINT"); env != "" {
			targetURL = env
		} else {
			targetURL = TargetEndpoint
		}
	}
	geoURL := opts.GeoURL
	if geoURL == "" {
		geoURL = DefaultGeoURL
	}

	client := opts.HTTPClient
	var trToClose *http.Transport
	if client == nil {
		var proxyURL string
		if strings.HasPrefix(tag, "P:") && store != nil {
			if active, err := store.Active(); err == nil && active != nil {
				proxyURL = active.RawURL(false)
			}
		} else if strings.HasPrefix(tag, "E:") {
			proxyURL = EnvProxyURL()
		}
		tr, err := TransportForProxyURL(proxyURL)
		if err != nil {
			tr = &http.Transport{}
		}
		trToClose = tr
		client = &http.Client{Transport: tr, Timeout: 3 * time.Second}
	}
	if trToClose != nil {
		defer trToClose.CloseIdleConnections()
	}

	reachable := false
	cc := ""

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		req, err := http.NewRequestWithContext(ctx, http.MethodHead, targetURL, nil)
		if err != nil {
			return
		}
		if resp, err := client.Do(req); err == nil {
			_ = resp.Body.Close()
			reachable = true
		}
	}()
	go func() {
		defer wg.Done()
		gReq, err := http.NewRequestWithContext(ctx, http.MethodGet, geoURL, nil)
		if err != nil {
			return
		}
		gResp, err := client.Do(gReq)
		if err != nil {
			return
		}
		defer gResp.Body.Close()
		if body, rErr := io.ReadAll(io.LimitReader(gResp.Body, 64*1024)); rErr == nil {
			var res struct {
				Country     string `json:"country"`
				CountryCode string `json:"country_code"`
			}
			if json.Unmarshal(body, &res) == nil {
				cc = strings.ToUpper(res.Country)
				if cc == "" {
					cc = strings.ToUpper(res.CountryCode)
				}
			}
		}
	}()
	wg.Wait()

	stateCode := 1
	if !reachable {
		stateCode = 0
	} else if IsBlockedCountry(cc) {
		stateCode = 2
	}

	if cacheFile != "" {
		ccToken := cc
		if ccToken == "" {
			ccToken = "-"
		}
		content := fmt.Sprintf("%d %d %s %s", now.Unix(), stateCode, ccToken, tag)
		_ = util.WriteFileAtomic(cacheFile, []byte(content), 0600)
	}

	switch stateCode {
	case 0:
		return NetStateBlocked, cc
	case 1:
		return NetStateOK, cc
	default:
		return NetStateRegion, cc
	}
}
