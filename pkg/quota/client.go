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
	RevokeURL         string
	AICodeURL         string
	OAuthPair         *OAuthPair
	FindAgy           func(*config.Paths) (string, error)
	OnTokenRefreshed  func(tok *profile.Token)
}

type flight struct {
	done    chan struct{}
	cancel  context.CancelFunc
	waiters int
	info    *QuotaInfo
	err     error
}

type Client struct {
	paths            *config.Paths
	baseURL          string
	oauthURL         string
	revokeURL        string
	aicodeURL        string
	oauthPair        *OAuthPair
	findAgy          func(*config.Paths) (string, error)
	onTokenRefreshed func(tok *profile.Token)
	httpClient       *http.Client
	timeout          time.Duration
	slots            chan struct{}
	cacheMu          sync.Mutex
	cache            map[string]QuotaInfo
	cacheInfo        os.FileInfo
	flightMu         sync.Mutex
	started          bool
	flights          map[[32]byte]*flight
	tokens           map[[32]byte]profile.Token
	oauthMu          sync.Mutex
	discoveredPair   *OAuthPair
	scannedPath      string
	scannedIDs       []string
	scannedSecrets   []string
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
	if opts.RevokeURL == "" {
		opts.RevokeURL = "https://oauth2.googleapis.com/revoke"
	}
	if opts.AICodeURL == "" {
		opts.AICodeURL = DefaultAICodeURL
	}
	if opts.OAuthPair != nil {
		pair := *opts.OAuthPair
		opts.OAuthPair = &pair
	}
	return &Client{paths: paths, baseURL: DefaultBaseURL, oauthURL: opts.OAuthURL, revokeURL: opts.RevokeURL, aicodeURL: opts.AICodeURL,
		oauthPair: opts.OAuthPair, findAgy: opts.FindAgy, onTokenRefreshed: opts.OnTokenRefreshed,
		httpClient: opts.HTTPClient, timeout: opts.Timeout, slots: make(chan struct{}, opts.MaxConcurrentHTTP),
		flights: make(map[[32]byte]*flight), tokens: make(map[[32]byte]profile.Token)}
}

// SetBaseURL replaces the CloudCode API endpoint before the client is first used
// and disables the default AICode gRPC endpoint for isolated mock-server tests.
func (c *Client) SetBaseURL(url string) {
	c.baseURL = url
	c.aicodeURL = ""
}

// SetAICodeURL sets the AICode gRPC base URL (empty string disables AICode).
func (c *Client) SetAICodeURL(url string) { c.aicodeURL = url }

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
		go c.runFetch(opCtx, key, f, token, force)
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

func (c *Client) runFetch(ctx context.Context, key [32]byte, f *flight, tok profile.Token, force bool) {
	defer f.cancel()
	info, err := c.fetchNetwork(ctx, &tok, force)
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

type endpointResult struct {
	data map[string]interface{}
	err  error
}

func (c *Client) fetchEndpoints(ctx context.Context, accessToken, project string) []endpointResult {
	loadBody := map[string]interface{}{"metadata": map[string]interface{}{"ideType": "ANTIGRAVITY", "pluginType": "GEMINI"}}
	quotaBody := map[string]interface{}{}
	if project != "" {
		quotaBody["project"] = project
	}
	calls := []struct {
		path string
		body interface{}
	}{
		{"/v1internal:loadCodeAssist", loadBody},
		{"/v1internal:retrieveUserQuotaSummary", quotaBody},
		{"/v1internal:retrieveUserQuota", quotaBody},
	}
	results := make([]endpointResult, len(calls))
	var wg sync.WaitGroup
	for i, call := range calls {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i].data, results[i].err = c.postJSON(ctx, accessToken, call.path, call.body)
		}()
	}
	wg.Wait()
	return results
}

func hasUnauthorizedError(results []endpointResult) bool {
	var statusErr httpStatusError
	for _, r := range results {
		if errors.As(r.err, &statusErr) && statusErr.status == http.StatusUnauthorized {
			return true
		}
	}
	return false
}

func isForbiddenError(err error) bool {
	var statusErr httpStatusError
	return errors.As(err, &statusErr) && statusErr.status == http.StatusForbidden
}

func (c *Client) retryQuotaWithProject(ctx context.Context, accessToken, project string, results []endpointResult) {
	paths := []string{"", "/v1internal:retrieveUserQuotaSummary", "/v1internal:retrieveUserQuota"}
	body := map[string]interface{}{"project": project}
	var wg sync.WaitGroup
	for i := 1; i <= 2; i++ {
		if !isForbiddenError(results[i].err) {
			continue
		}
		idx, path := i, paths[i]
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[idx].data, results[idx].err = c.postJSON(ctx, accessToken, path, body)
		}()
	}
	wg.Wait()
}

func (c *Client) fetchNetwork(ctx context.Context, tok *profile.Token, force bool) (*QuotaInfo, error) {
	refreshed := false
	if tok.AccessToken == "" || (!tok.ExpiresAt.IsZero() && time.Until(tok.ExpiresAt) < 2*time.Minute) {
		if err := c.refreshAccessToken(ctx, tok); err != nil {
			return c.cachedFailure(tok.Email, err)
		}
		refreshed = true
	}
	cachedTier := ""
	cachedProject := ""
	if tok.Email != "" {
		if cached, ok := c.Cached(tok.Email); ok {
			cachedTier = cached.Tier
			cachedProject = cached.Project
		}
	}

	var loadRes endpointResult
	var aicodeSummary *aiCodeQuotaSummary
	var aicodeErr error
	var results []endpointResult
	needTier := force || cachedTier == "" || cachedTier == "?"

	if c.aicodeURL != "" {
		loadBody := map[string]interface{}{"metadata": map[string]interface{}{"ideType": "ANTIGRAVITY", "pluginType": "GEMINI"}}
		runPrimary := func() {
			var wg sync.WaitGroup
			if needTier {
				wg.Add(1)
				go func() {
					defer wg.Done()
					loadRes.data, loadRes.err = c.postJSON(ctx, tok.AccessToken, "/v1internal:loadCodeAssist", loadBody)
				}()
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				aicodeSummary, aicodeErr = c.fetchAICodeQuota(ctx, tok.AccessToken)
			}()
			wg.Wait()
		}
		runPrimary()
		if !refreshed && tok.RefreshToken != "" && (hasUnauthorizedError([]endpointResult{loadRes}) || isUnauthorizedError(aicodeErr)) {
			if err := c.refreshAccessToken(ctx, tok); err != nil {
				return c.cachedFailure(tok.Email, err)
			}
			refreshed = true
			runPrimary()
		}
	} else {
		results = c.fetchEndpoints(ctx, tok.AccessToken, cachedProject)
		if !refreshed && tok.RefreshToken != "" && hasUnauthorizedError(results) {
			if err := c.refreshAccessToken(ctx, tok); err != nil {
				return c.cachedFailure(tok.Email, err)
			}
			refreshed = true
			results = c.fetchEndpoints(ctx, tok.AccessToken, cachedProject)
		}
		loadRes = results[0]
	}

	project := cachedProject
	if loadRes.data != nil {
		if p, ok := loadRes.data["cloudaicompanionProject"].(string); ok && p != "" {
			project = p
		}
	}

	if err := ctx.Err(); err != nil {
		return c.cachedFailure(tok.Email, err)
	}

	tier := "?"
	if !needTier {
		tier = cachedTier
	}
	info := &QuotaInfo{Tier: tier, Project: project, Email: tok.Email, UpdatedAt: time.Now()}
	applyTier(info, loadRes.data)
	if aicodeErr == nil && aicodeSummary != nil {
		applyAICodeSummary(info, aicodeSummary)
	}

	hasAnyQuota := info.HasGemini || info.HasClaude || info.HasGeminiWeekly || info.HasClaudeWeekly
	if c.aicodeURL != "" && !hasAnyQuota {
		// Fallback to CloudCode quota endpoints if AICode failed or returned no groups.
		quotaProject := project
		quotaBody := map[string]interface{}{}
		if quotaProject != "" {
			quotaBody["project"] = quotaProject
		}
		results = make([]endpointResult, 3)
		results[0] = loadRes
		var wg sync.WaitGroup
		if !needTier && loadRes.data == nil && loadRes.err == nil {
			loadBody := map[string]interface{}{"metadata": map[string]interface{}{"ideType": "ANTIGRAVITY", "pluginType": "GEMINI"}}
			wg.Add(1)
			go func() {
				defer wg.Done()
				results[0].data, results[0].err = c.postJSON(ctx, tok.AccessToken, "/v1internal:loadCodeAssist", loadBody)
			}()
		}
		for i, path := range []string{"", "/v1internal:retrieveUserQuotaSummary", "/v1internal:retrieveUserQuota"} {
			if i == 0 {
				continue
			}
			idx, p := i, path
			wg.Add(1)
			go func() {
				defer wg.Done()
				results[idx].data, results[idx].err = c.postJSON(ctx, tok.AccessToken, p, quotaBody)
			}()
		}
		wg.Wait()
		if !refreshed && tok.RefreshToken != "" && hasUnauthorizedError(results) {
			if err := c.refreshAccessToken(ctx, tok); err == nil {
				refreshed = true
				for i, path := range []string{"", "/v1internal:retrieveUserQuotaSummary", "/v1internal:retrieveUserQuota"} {
					if i == 0 {
						continue
					}
					results[i].data, results[i].err = c.postJSON(ctx, tok.AccessToken, path, quotaBody)
				}
			}
		}
		if results[0].data != nil {
			applyTier(info, results[0].data)
			if p, ok := results[0].data["cloudaicompanionProject"].(string); ok && p != "" {
				project = p
				info.Project = p
			}
		}
		if project != "" && project != cachedProject &&
			(isForbiddenError(results[1].err) || isForbiddenError(results[2].err)) {
			c.retryQuotaWithProject(ctx, tok.AccessToken, project, results)
		}
		applySummary(info, results[1].data)
		applyModelsAt(info, results[2].data, time.Now())
		hasAnyQuota = info.HasGemini || info.HasClaude || info.HasGeminiWeekly || info.HasClaudeWeekly
	} else if c.aicodeURL == "" {
		if project != "" && project != cachedProject &&
			(isForbiddenError(results[1].err) || isForbiddenError(results[2].err)) {
			c.retryQuotaWithProject(ctx, tok.AccessToken, project, results)
		}
		applySummary(info, results[1].data)
		applyModelsAt(info, results[2].data, time.Now())
		hasAnyQuota = info.HasGemini || info.HasClaude || info.HasGeminiWeekly || info.HasClaudeWeekly
	}

	if refreshed {
		tokCopy := *tok
		info.RefreshedToken = &tokCopy
		info.Email = tok.Email
		if c.onTokenRefreshed != nil {
			c.onTokenRefreshed(&tokCopy)
		}
	}

	if info.Tier == "?" && !hasAnyQuota {
		var errs []error
		errs = append(errs, errors.New("all quota calls failed"), loadRes.err)
		if aicodeErr != nil {
			errs = append(errs, aicodeErr)
		}
		if len(results) >= 3 {
			errs = append(errs, results[1].err, results[2].err)
		}
		return c.cachedFailure(tok.Email, errors.Join(errs...))
	}
	if !hasAnyQuota {
		var qErrs []error
		if aicodeErr != nil {
			qErrs = append(qErrs, aicodeErr)
		}
		if len(results) >= 3 && (results[1].err != nil || results[2].err != nil) {
			qErrs = append(qErrs, results[1].err, results[2].err)
		}
		if len(qErrs) > 0 {
			return c.cachedFailure(tok.Email, errors.Join(append([]error{errors.New("quota endpoints failed")}, qErrs...)...))
		}
		if existing, ok := c.Cached(tok.Email); ok && (existing.HasGemini || existing.HasClaude || existing.HasGeminiWeekly || existing.HasClaudeWeekly) {
			return existing, errors.New("quota response contained no model usage data")
		}
	}
	if tok.Email != "" {
		if err := c.saveCached(ctx, tok.Email, info); err != nil {
			return info, err
		}
	}
	return info, nil
}

func isUnauthorizedError(err error) bool {
	var statusErr httpStatusError
	return errors.As(err, &statusErr) && statusErr.status == http.StatusUnauthorized
}

func (c *Client) cachedFailure(email string, err error) (*QuotaInfo, error) {
	if info, ok := c.Cached(email); ok {
		return info, err
	}
	return nil, err
}
