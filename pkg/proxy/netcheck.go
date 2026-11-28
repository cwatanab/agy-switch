package proxy

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ZhangNy301/agy-switch/pkg/config"
	"github.com/ZhangNy301/agy-switch/pkg/util"
)

const (
	NetStateOK      = "ok"
	NetStateBlocked = "blocked"

	netOKTTL  = 6 * time.Hour
	netBadTTL = 10 * time.Minute
)

type NetCheckOptions struct {
	TargetURL  string
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

// CheckNetwork checks Google reachability, caching results in .netcheck.
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
						if fields[1] == "1" {
							return NetStateOK, ""
						}
						return NetStateBlocked, ""
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
	if req, err := http.NewRequestWithContext(ctx, http.MethodHead, targetURL, nil); err == nil {
		if resp, err := client.Do(req); err == nil {
			_ = resp.Body.Close()
			reachable = true
		}
	}

	stateCode := 1
	state := NetStateOK
	if !reachable {
		stateCode = 0
		state = NetStateBlocked
	}

	if cacheFile != "" {
		content := fmt.Sprintf("%d %d - %s", now.Unix(), stateCode, tag)
		_ = util.WriteFileAtomic(cacheFile, []byte(content), 0600)
	}

	return state, ""
}
