package quota

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/ZhangNy301/agy-switch/pkg/config"
	"github.com/ZhangNy301/agy-switch/pkg/filelock"
	"github.com/ZhangNy301/agy-switch/pkg/keyring"
	"github.com/ZhangNy301/agy-switch/pkg/profile"
)

const (
	CacheTTL           = 15 * time.Minute
	DefaultTimeout     = 15 * time.Second
	DefaultConcurrency = 4
	DefaultRevokeURL   = "https://oauth2.googleapis.com/revoke"
)

// Options configures a quota client's dependencies and execution limits.
type Options struct {
	HTTPClient        *http.Client
	Timeout           time.Duration
	MaxConcurrentHTTP int
	RevokeURL         string
	TokenStore        keyring.TokenStore
	UseKeyringSwap    *bool
	FindAgy           func(*config.Paths) (string, error)
	ProxyEnv          func() ([]string, error)
	RunUsage          func(ctx context.Context, tok *profile.Token) ([]byte, *profile.Token, error)
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
	revokeURL        string
	tokenStore       keyring.TokenStore
	useKeyringSwap   bool
	keyringSlot      chan struct{}
	findAgy          func(*config.Paths) (string, error)
	proxyEnv         func() ([]string, error)
	runUsage         func(ctx context.Context, tok *profile.Token) ([]byte, *profile.Token, error)
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
	if opts.RevokeURL == "" {
		opts.RevokeURL = DefaultRevokeURL
	}
	useSwap := runtime.GOOS == "windows" || runtime.GOOS == "darwin"
	if opts.UseKeyringSwap != nil {
		useSwap = *opts.UseKeyringSwap
	}
	return &Client{
		paths:            paths,
		revokeURL:        opts.RevokeURL,
		tokenStore:       opts.TokenStore,
		useKeyringSwap:   useSwap && opts.TokenStore != nil,
		keyringSlot:      make(chan struct{}, 1),
		findAgy:          opts.FindAgy,
		proxyEnv:         opts.ProxyEnv,
		runUsage:         opts.RunUsage,
		onTokenRefreshed: opts.OnTokenRefreshed,
		httpClient:       opts.HTTPClient,
		timeout:          opts.Timeout,
		slots:            make(chan struct{}, opts.MaxConcurrentHTTP),
		flights:          make(map[[32]byte]*flight),
		tokens:           make(map[[32]byte]profile.Token),
	}
}

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
	waitTimeout := timeout
	if c.useKeyringSwap && c.runUsage == nil && waitTimeout < 90*time.Second {
		waitTimeout = 90 * time.Second
	}
	f := c.flights[key]
	if f == nil {
		c.started = true
		opCtx, cancel := context.WithTimeout(context.Background(), waitTimeout)
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
	ctx, cancel := context.WithTimeout(ctx, waitTimeout)
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
	info, err := c.fetchViaCLI(ctx, &tok)
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

func (c *Client) fetchViaCLI(ctx context.Context, tok *profile.Token) (*QuotaInfo, error) {
	if err := ctx.Err(); err != nil {
		return c.cachedFailure(tok.Email, err)
	}

	var raw []byte
	var refreshedTok *profile.Token
	var err error
	if c.runUsage != nil {
		raw, refreshedTok, err = c.runUsage(ctx, tok)
	} else {
		raw, refreshedTok, err = c.runAgyUsage(ctx, tok)
	}
	if err != nil {
		return c.cachedFailure(tok.Email, err)
	}

	parsed, err := parseCLIUsage(raw)
	if err != nil {
		return c.cachedFailure(tok.Email, err)
	}

	tier := "?"
	if tok.Email != "" {
		if cached, ok := c.Cached(tok.Email); ok && cached.Tier != "" && cached.Tier != "?" {
			tier = cached.Tier
		}
	}
	info := &QuotaInfo{
		Tier:      tier,
		Email:     tok.Email,
		UpdatedAt: time.Now(),
	}
	applyCLIUsage(info, parsed)

	if refreshedTok != nil {
		*tok = *refreshedTok
		if tok.Email != "" {
			info.Email = tok.Email
		}
		tokCopy := *tok
		info.RefreshedToken = &tokCopy
		if c.onTokenRefreshed != nil {
			c.onTokenRefreshed(&tokCopy)
		}
	}

	hasAnyQuota := info.HasGemini || info.HasClaude || info.HasGeminiWeekly || info.HasClaudeWeekly
	if !hasAnyQuota {
		return c.cachedFailure(tok.Email, errors.New("agy /usage returned no quota data"))
	}

	if tok.Email != "" {
		if err := c.saveCached(ctx, tok.Email, info); err != nil {
			return info, err
		}
	}
	return info, nil
}

func (c *Client) runAgyUsage(ctx context.Context, tok *profile.Token) ([]byte, *profile.Token, error) {
	if c.findAgy == nil {
		return nil, nil, fmt.Errorf("agy binary finder not configured")
	}
	agyBin, err := c.findAgy(c.paths)
	if err != nil {
		return nil, nil, fmt.Errorf("locate agy binary: %w", err)
	}

	tokBytes, err := marshalTokenForCLI(tok)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal token: %w", err)
	}

	usingKeyringSwap := c.useKeyringSwap && c.tokenStore != nil
	if usingKeyringSwap {
		select {
		case c.keyringSlot <- struct{}{}:
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		}
		defer func() { <-c.keyringSlot }()

		if c.paths != nil && c.paths.DataDir != "" {
			releaseLock, lErr := filelock.Acquire(ctx, filepath.Join(c.paths.DataDir, "accounts.lock"))
			if lErr != nil {
				return nil, nil, lErr
			}
			defer func() { _ = releaseLock() }()
		}

		origLive, origErr := c.tokenStore.ReadLive()
		if origErr != nil && !os.IsNotExist(origErr) {
			return nil, nil, fmt.Errorf("read live credential before quota check: %w", origErr)
		}
		origExists := origErr == nil && len(origLive) > 0
		if !origExists || !sameTokenGrant(origLive, tok) {
			if wErr := c.tokenStore.WriteLive(tokBytes); wErr != nil {
				return nil, nil, fmt.Errorf("stage account credential for quota check: %w", wErr)
			}
			defer func() {
				if origExists {
					_ = c.tokenStore.WriteLive(origLive)
				} else {
					_ = c.tokenStore.DeleteLive()
				}
			}()
		}
	} else {
		select {
		case c.slots <- struct{}{}:
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		}
		defer func() { <-c.slots }()
	}

	tmpHome, err := os.MkdirTemp("", "agy-switch-usage-*")
	if err != nil {
		return nil, nil, fmt.Errorf("create isolated home: %w", err)
	}
	defer os.RemoveAll(tmpHome)

	tokDir := filepath.Join(tmpHome, ".gemini", "antigravity-cli")
	if err := os.MkdirAll(tokDir, 0700); err != nil {
		return nil, nil, fmt.Errorf("create isolated config dir: %w", err)
	}
	tokPath := filepath.Join(tokDir, "antigravity-oauth-token")
	if err := os.WriteFile(tokPath, tokBytes, 0600); err != nil {
		return nil, nil, fmt.Errorf("write isolated token: %w", err)
	}

	overrides := []string{
		"HOME=" + tmpHome,
		"USERPROFILE=" + tmpHome,
		"XDG_CONFIG_HOME=" + filepath.Join(tmpHome, ".config"),
		"XDG_DATA_HOME=" + filepath.Join(tmpHome, ".local", "share"),
		"AGY_SWITCH_NO_REASSERT=1",
	}
	if !usingKeyringSwap {
		overrides = append(overrides, "DBUS_SESSION_BUS_ADDRESS=", "GNOME_KEYRING_CONTROL=")
	}
	if c.proxyEnv != nil {
		if pEnv, pErr := c.proxyEnv(); pErr == nil && len(pEnv) > 0 {
			overrides = append(overrides, pEnv...)
		}
	}

	cmdCtx, cmdCancel := context.WithTimeout(ctx, c.timeout)
	defer cmdCancel()

	stdout, stderr, err := execUsageCommand(cmdCtx, agyBin, []string{"--output-format", "json", "-p", "/usage"}, mergeEnv(os.Environ(), overrides))
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, nil, ctxErr
		}
		if cmdErr := cmdCtx.Err(); cmdErr != nil {
			return nil, nil, cmdErr
		}
		errMsg := strings.Join(strings.Fields(string(stderr)), " ")
		if len(errMsg) > 240 {
			errMsg = errMsg[:237] + "..."
		}
		if errMsg != "" {
			return nil, nil, fmt.Errorf("agy /usage failed: %w (%s)", err, errMsg)
		}
		return nil, nil, fmt.Errorf("agy /usage failed: %w", err)
	}

	var refreshedTok *profile.Token
	if usingKeyringSwap {
		if afterLive, rErr := c.tokenStore.ReadLive(); rErr == nil && !bytes.Equal(afterLive, tokBytes) {
			if parsedTok, pErr := profile.ParseTokenJSON(afterLive); pErr == nil && (parsedTok.AccessToken != "" || parsedTok.RefreshToken != "") {
				if parsedTok.Email == "" {
					parsedTok.Email = tok.Email
				}
				refreshedTok = parsedTok
			}
		}
	}
	if refreshedTok == nil {
		if afterBytes, rErr := os.ReadFile(tokPath); rErr == nil && !bytes.Equal(afterBytes, tokBytes) {
			if parsedTok, pErr := profile.ParseTokenJSON(afterBytes); pErr == nil && (parsedTok.AccessToken != "" || parsedTok.RefreshToken != "") {
				if parsedTok.Email == "" {
					parsedTok.Email = tok.Email
				}
				refreshedTok = parsedTok
			}
		}
	}

	return stdout, refreshedTok, nil
}

func sameTokenGrant(liveBytes []byte, tok *profile.Token) bool {
	if tok == nil || len(liveBytes) == 0 {
		return false
	}
	liveTok, err := profile.ParseTokenJSON(liveBytes)
	if err != nil {
		return false
	}
	if tok.RefreshToken != "" && liveTok.RefreshToken != "" {
		return tok.RefreshToken == liveTok.RefreshToken
	}
	if tok.AccessToken != "" && liveTok.AccessToken != "" {
		return tok.AccessToken == liveTok.AccessToken
	}
	return false
}

func marshalTokenForCLI(tok *profile.Token) ([]byte, error) {
	if len(tok.Raw) > 0 {
		return json.Marshal(tok.Raw)
	}
	inner := map[string]interface{}{
		"access_token":  tok.AccessToken,
		"refresh_token": tok.RefreshToken,
	}
	if !tok.ExpiresAt.IsZero() {
		inner["expiry"] = tok.ExpiresAt.UTC().Format(time.RFC3339)
	}
	obj := map[string]interface{}{
		"auth_method": "consumer",
		"token":       inner,
	}
	if tok.IDToken != "" {
		obj["id_token"] = tok.IDToken
	}
	if tok.Email != "" {
		obj["id_token_email"] = tok.Email
	}
	return json.Marshal(obj)
}

func mergeEnv(base, overrides []string) []string {
	idx := make(map[string]int, len(base)+len(overrides))
	out := make([]string, 0, len(base)+len(overrides))
	keyOf := func(kv string) string {
		k, _, _ := strings.Cut(kv, "=")
		if runtime.GOOS == "windows" {
			return strings.ToUpper(k)
		}
		return k
	}
	for _, list := range [][]string{base, overrides} {
		for _, kv := range list {
			k := keyOf(kv)
			if pos, ok := idx[k]; ok {
				out[pos] = kv
			} else {
				idx[k] = len(out)
				out = append(out, kv)
			}
		}
	}
	return out
}

// RevokeToken revokes the given refresh token at Google's public OAuth 2.0 revocation endpoint.
func (c *Client) RevokeToken(ctx context.Context, refreshToken string) error {
	if refreshToken == "" {
		return fmt.Errorf("no refresh token to revoke")
	}
	select {
	case c.slots <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-c.slots }()

	form := url.Values{"token": {refreshToken}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.revokeURL, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("revoke OAuth token: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return fmt.Errorf("revoke OAuth token: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("revoke OAuth token: API returned HTTP %d", resp.StatusCode)
	}
	return nil
}

func (c *Client) cachedFailure(email string, err error) (*QuotaInfo, error) {
	if info, ok := c.Cached(email); ok {
		return info, err
	}
	return nil, err
}
