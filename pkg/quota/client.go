package quota

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/ZhangNy301/agy-switch/pkg/config"
	"github.com/ZhangNy301/agy-switch/pkg/profile"
)

const (
	DefaultBaseURL     = "https://daily-cloudcode-pa.googleapis.com"
	CacheTTL           = 15 * time.Minute
	DefaultTimeout     = 15 * time.Second
	DefaultConcurrency = 4
)

// Options configures a quota client's dependencies and request limits.
// Limits apply to the entire fetch, including OAuth refresh and retry waits.
type Options struct {
	HTTPClient        *http.Client
	Timeout           time.Duration
	MaxConcurrentHTTP int
	OAuthURL          string
	OAuthPair         *OAuthPair
}

type flight struct {
	done    chan struct{}
	cancel  context.CancelFunc
	waiters int
	info    *QuotaInfo
	err     error
}

type Client struct {
	paths      *config.Paths
	baseURL    string
	oauthURL   string
	oauthPair  *OAuthPair
	httpClient *http.Client
	timeout    time.Duration
	slots      chan struct{}
	cacheMu    sync.Mutex
	cache      map[string]QuotaInfo
	cacheInfo  os.FileInfo
	flightMu   sync.Mutex
	started    bool
	flights    map[[32]byte]*flight
	tokens     map[[32]byte]profile.Token
}

func NewClient(paths *config.Paths) *Client { return NewClientWithOptions(paths, Options{}) }

// NewClientWithOptions creates a client with bounded requests and concurrency.
func NewClientWithOptions(paths *config.Paths, opts Options) *Client {
	if opts.HTTPClient == nil {
		opts.HTTPClient = &http.Client{Timeout: 10 * time.Second}
	}
	if opts.Timeout <= 0 {
		opts.Timeout = DefaultTimeout
	}
	if opts.MaxConcurrentHTTP <= 0 {
		opts.MaxConcurrentHTTP = DefaultConcurrency
	}
	if opts.OAuthURL == "" {
		opts.OAuthURL = "https://oauth2.googleapis.com/token"
	}
	if opts.OAuthPair != nil {
		pair := *opts.OAuthPair
		opts.OAuthPair = &pair
	}
	return &Client{paths: paths, baseURL: DefaultBaseURL, oauthURL: opts.OAuthURL,
		oauthPair:  opts.OAuthPair,
		httpClient: opts.HTTPClient, timeout: opts.Timeout, slots: make(chan struct{}, opts.MaxConcurrentHTTP),
		flights: make(map[[32]byte]*flight), tokens: make(map[[32]byte]profile.Token)}
}

// SetBaseURL replaces the API endpoint before the client is first used.
func (c *Client) SetBaseURL(url string) { c.baseURL = url }

// SetLimits configures limits before the first fetch starts.
func (c *Client) SetLimits(timeout time.Duration, concurrency int) error {
	if timeout <= 0 || concurrency < 1 || concurrency > 64 {
		return fmt.Errorf("quota timeout must be positive and concurrency must be between 1 and 64")
	}
	c.flightMu.Lock()
	defer c.flightMu.Unlock()
	if c.started {
		return fmt.Errorf("configure quota limits before the first fetch")
	}
	c.timeout, c.slots = timeout, make(chan struct{}, concurrency)
	return nil
}

func (c *Client) Fetch(tok *profile.Token, force bool) (*QuotaInfo, error) {
	return c.FetchContext(context.Background(), tok, force)
}

// FetchContext coalesces concurrent fetches for the same grant. It never mutates
// the caller's token. The last departing waiter cancels an abandoned fetch.
func (c *Client) FetchContext(ctx context.Context, tok *profile.Token, force bool) (*QuotaInfo, error) {
	if tok == nil {
		return nil, fmt.Errorf("missing account token")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !force && tok.Email != "" {
		if cached, ok := c.loadCached(tok.Email); ok {
			return cached, nil
		}
	}
	keyToken := tok.RefreshToken
	if keyToken == "" {
		keyToken = tok.AccessToken
	}
	key := sha256.Sum256([]byte(tok.Email + "\x00" + keyToken))
	c.flightMu.Lock()
	timeout := c.timeout
	f := c.flights[key]
	if f == nil {
		c.started = true
		opCtx, cancel := context.WithTimeout(context.Background(), timeout)
		f = &flight{done: make(chan struct{}), cancel: cancel}
		c.flights[key] = f
		token := *tok
		if fresh, ok := c.tokens[key]; ok && time.Until(fresh.ExpiresAt) > 2*time.Minute {
			token = fresh
		}
		go c.runFetch(opCtx, key, f, token)
	}
	f.waiters++
	c.flightMu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	defer func() {
		c.flightMu.Lock()
		defer c.flightMu.Unlock()
		f.waiters--
		if f.waiters == 0 {
			f.cancel()
			if c.flights[key] == f {
				delete(c.flights, key)
			}
		}
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-f.done:
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if f.info == nil {
			return nil, f.err
		}
		info := *f.info
		return &info, f.err
	}
}

func (c *Client) runFetch(ctx context.Context, key [32]byte, f *flight, tok profile.Token) {
	defer f.cancel()
	info, err := c.fetchNetwork(ctx, &tok)
	c.flightMu.Lock()
	if tok.AccessToken != "" && time.Until(tok.ExpiresAt) > 2*time.Minute {
		c.tokens[key] = tok
	}
	f.info, f.err = info, err
	if c.flights[key] == f {
		delete(c.flights, key)
	}
	close(f.done)
	c.flightMu.Unlock()
}

func (c *Client) fetchNetwork(ctx context.Context, tok *profile.Token) (*QuotaInfo, error) {
	if tok.AccessToken == "" || (!tok.ExpiresAt.IsZero() && time.Until(tok.ExpiresAt) < 2*time.Minute) {
		if err := c.refreshAccessToken(ctx, tok); err != nil {
			return c.cachedFailure(tok.Email, err)
		}
	}
	loadBody := map[string]interface{}{"metadata": map[string]interface{}{"ideType": "ANTIGRAVITY", "pluginType": "GEMINI"}}
	calls := []struct {
		path string
		body interface{}
	}{
		{"/v1internal:loadCodeAssist", loadBody},
		{"/v1internal:retrieveUserQuotaSummary", nil},
		{"/v1internal:retrieveUserQuota", map[string]interface{}{}},
	}
	type result struct {
		data map[string]interface{}
		err  error
	}
	results := make([]result, len(calls))
	var wg sync.WaitGroup
	for i, call := range calls {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i].data, results[i].err = c.postJSON(ctx, tok.AccessToken, call.path, call.body)
		}()
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return c.cachedFailure(tok.Email, err)
	}
	info := &QuotaInfo{Tier: "?", UpdatedAt: time.Now()}
	applyTier(info, results[0].data)
	applySummary(info, results[1].data)
	applyModels(info, results[2].data)
	if info.Tier == "?" && !info.HasGemini && !info.HasClaude {
		return c.cachedFailure(tok.Email, errors.Join(errors.New("all quota calls failed"), results[0].err, results[1].err, results[2].err))
	}
	if tok.Email != "" {
		if err := c.saveCached(ctx, tok.Email, info); err != nil {
			return info, err
		}
	}
	return info, nil
}

func (c *Client) cachedFailure(email string, err error) (*QuotaInfo, error) {
	if info, ok := c.Cached(email); ok {
		return info, err
	}
	return nil, err
}
