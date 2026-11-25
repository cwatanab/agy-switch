package quota

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/ZhangNy301/agy-switch/pkg/config"
	"github.com/ZhangNy301/agy-switch/pkg/profile"
	"github.com/ZhangNy301/agy-switch/pkg/util"
)

type OAuthPair struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
}

var (
	idRegex     = regexp.MustCompile(`\d{6,}-[a-z0-9_-]+\.apps\.googleusercontent\.com`)
	secretRegex = regexp.MustCompile(`GOCSPX-[A-Za-z0-9_\-]{28}`)
)

// RefreshAccessToken refreshes an installed-app token with the default deadline.
func RefreshAccessToken(tok *profile.Token) error {
	paths, err := config.ResolvePaths()
	if err != nil {
		return err
	}
	client := NewClient(paths)
	ctx, cancel := context.WithTimeout(context.Background(), DefaultTimeout)
	defer cancel()
	return client.refreshAccessToken(ctx, tok)
}

func (c *Client) refreshAccessToken(ctx context.Context, tok *profile.Token) error {
	if tok == nil || tok.RefreshToken == "" {
		return fmt.Errorf("no refresh token")
	}
	if c.oauthPair != nil {
		pair, err := validOAuthPair(c.oauthPair)
		if err != nil {
			return err
		}
		return c.refreshWithPair(ctx, tok, pair)
	}
	id, secret := os.Getenv("AGY_SWITCH_OAUTH_CLIENT_ID"), os.Getenv("AGY_SWITCH_OAUTH_CLIENT_SECRET")
	if id != "" || secret != "" {
		pair, err := validOAuthPair(&OAuthPair{ClientID: id, ClientSecret: secret})
		if err != nil {
			return err
		}
		return c.refreshWithPair(ctx, tok, pair)
	}
	var failedPair *OAuthPair
	pair, err := LoadCachedOAuthPair(c.paths)
	if err == nil {
		refreshErr := c.refreshWithPair(ctx, tok, pair)
		if refreshErr == nil || !isOAuthClientError(refreshErr) || c.findAgy == nil {
			return refreshErr
		}
		failedPair = pair
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read OAuth client configuration: %w", err)
	}
	if c.findAgy == nil {
		return fmt.Errorf("OAuth client not configured: set AGY_SWITCH_OAUTH_CLIENT_ID and AGY_SWITCH_OAUTH_CLIENT_SECRET or create oauth_client.json in the data directory")
	}
	return c.discoverAndRefresh(ctx, tok, failedPair)
}

func (c *Client) refreshWithPair(ctx context.Context, tok *profile.Token, pair *OAuthPair) error {
	form := url.Values{
		"client_id": {pair.ClientID}, "client_secret": {pair.ClientSecret},
		"refresh_token": {tok.RefreshToken}, "grant_type": {"refresh_token"},
	}
	res, err := c.requestJSON(ctx, c.oauthURL, []byte(form.Encode()), http.Header{"Content-Type": {"application/x-www-form-urlencoded"}})
	if err != nil {
		return fmt.Errorf("refresh access token: %w", err)
	}
	access, ok := res["access_token"].(string)
	if !ok || access == "" {
		return fmt.Errorf("OAuth response has no access token")
	}
	expires, _ := res["expires_in"].(float64)
	if expires <= 0 {
		expires = 3600
	}
	tok.AccessToken = access
	tok.ExpiresAt = time.Now().Add(time.Duration(expires) * time.Second)
	if rt, ok := res["refresh_token"].(string); ok && rt != "" {
		tok.RefreshToken = rt
	}
	if idt, ok := res["id_token"].(string); ok && idt != "" {
		tok.IDToken = idt
		if em := profile.DecodeJWTEmail(idt); em != "" {
			tok.Email = em
		}
	}
	return nil
}

// ForceRefreshToken refreshes tok immediately regardless of its current expiry.
func (c *Client) ForceRefreshToken(ctx context.Context, tok *profile.Token) error {
	return c.refreshAccessToken(ctx, tok)
}

// RevokeToken revokes the given refresh token at Google's OAuth revocation endpoint.
func (c *Client) RevokeToken(ctx context.Context, refreshToken string) error {
	if refreshToken == "" {
		return fmt.Errorf("no refresh token to revoke")
	}
	form := url.Values{"token": {refreshToken}}
	headers := http.Header{"Content-Type": {"application/x-www-form-urlencoded"}}
	_, status, _, err := c.attempt(ctx, c.revokeURL, []byte(form.Encode()), headers)
	if err != nil {
		return fmt.Errorf("revoke OAuth token: %w", err)
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("revoke OAuth token: %w", httpStatusError{status: status})
	}
	return nil
}

func isOAuthClientError(err error) bool {
	var statusErr httpStatusError
	return errors.As(err, &statusErr) && (statusErr.status == http.StatusBadRequest || statusErr.status == http.StatusUnauthorized)
}

func sameOAuthPair(a, b *OAuthPair) bool {
	return a != nil && b != nil && a.ClientID == b.ClientID && a.ClientSecret == b.ClientSecret
}

func (c *Client) discoverAndRefresh(ctx context.Context, tok *profile.Token, failedPair *OAuthPair) error {
	c.oauthMu.Lock()
	defer c.oauthMu.Unlock()

	if c.discoveredPair != nil && !sameOAuthPair(c.discoveredPair, failedPair) {
		if err := c.refreshWithPair(ctx, tok, c.discoveredPair); err == nil {
			return nil
		}
	}
	if cached, err := LoadCachedOAuthPair(c.paths); err == nil && !sameOAuthPair(cached, failedPair) && !sameOAuthPair(cached, c.discoveredPair) {
		if err := c.refreshWithPair(ctx, tok, cached); err == nil {
			pairCopy := *cached
			c.discoveredPair = &pairCopy
			return nil
		}
	}

	binaryPath, err := c.findAgy(c.paths)
	if err != nil {
		return fmt.Errorf("OAuth client not configured and agy binary not found: %w", err)
	}
	ids, secrets := c.scannedIDs, c.scannedSecrets
	if c.scannedPath != binaryPath || (len(ids) == 0 && len(secrets) == 0) {
		ids, secrets, err = scanOAuthStrings(ctx, binaryPath)
		if err != nil {
			return fmt.Errorf("OAuth client not configured; scan agy binary: %w", err)
		}
		c.scannedPath, c.scannedIDs, c.scannedSecrets = binaryPath, ids, secrets
	}

	const maxAttempts = 20
	attempts := 0
	var lastErr error
	for _, cid := range ids {
		for _, secret := range secrets {
			candidate := OAuthPair{ClientID: cid, ClientSecret: secret}
			if sameOAuthPair(&candidate, failedPair) {
				continue
			}
			if attempts >= maxAttempts {
				break
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			attempts++
			err := c.refreshWithPair(ctx, tok, &candidate)
			if err == nil {
				pairCopy := candidate
				c.discoveredPair = &pairCopy
				if c.paths != nil && c.paths.DataDir != "" {
					_ = SaveOAuthPair(c.paths, &pairCopy)
				}
				return nil
			}
			lastErr = err
			if ctx.Err() != nil || !isOAuthClientError(err) {
				return err
			}
		}
		if attempts >= maxAttempts {
			break
		}
	}
	if lastErr != nil {
		return fmt.Errorf("OAuth client not configured (auto-discovery failed): %w", lastErr)
	}
	return fmt.Errorf("OAuth client not configured (no OAuth client credentials found in %s)", binaryPath)
}

func (c *Client) oauthCredentials() (*OAuthPair, error) {
	if c.oauthPair != nil {
		return validOAuthPair(c.oauthPair)
	}
	id, secret := os.Getenv("AGY_SWITCH_OAUTH_CLIENT_ID"), os.Getenv("AGY_SWITCH_OAUTH_CLIENT_SECRET")
	if id != "" || secret != "" {
		return validOAuthPair(&OAuthPair{ClientID: id, ClientSecret: secret})
	}
	pair, err := LoadCachedOAuthPair(c.paths)
	if os.IsNotExist(err) {
		return nil, fmt.Errorf("OAuth client not configured: set AGY_SWITCH_OAUTH_CLIENT_ID and AGY_SWITCH_OAUTH_CLIENT_SECRET or create oauth_client.json in the data directory")
	}
	if err != nil {
		return nil, fmt.Errorf("read OAuth client configuration: %w", err)
	}
	return pair, nil
}

func validOAuthPair(pair *OAuthPair) (*OAuthPair, error) {
	if pair == nil || pair.ClientID == "" || pair.ClientSecret == "" {
		return nil, fmt.Errorf("OAuth client configuration requires both client_id and client_secret")
	}
	result := *pair
	return &result, nil
}

func OAuthCachePath(paths *config.Paths) string {
	return filepath.Join(paths.DataDir, "oauth_client.json")
}

func LoadCachedOAuthPair(paths *config.Paths) (*OAuthPair, error) {
	if paths == nil || paths.DataDir == "" {
		return nil, os.ErrNotExist
	}
	data, err := os.ReadFile(OAuthCachePath(paths))
	if err != nil {
		return nil, err
	}
	var pair OAuthPair
	if err := json.Unmarshal(data, &pair); err != nil {
		return nil, err
	}
	return validOAuthPair(&pair)
}

func SaveOAuthPair(paths *config.Paths, pair *OAuthPair) error {
	if paths == nil || paths.DataDir == "" {
		return fmt.Errorf("OAuth client configuration requires a data directory")
	}
	pair, err := validOAuthPair(pair)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(pair, "", "  ")
	if err != nil {
		return err
	}
	return util.WriteFileAtomic(OAuthCachePath(paths), data, 0600)
}

func ScanOAuthStrings(binaryPath string) ([]string, []string, error) {
	return scanOAuthStrings(context.Background(), binaryPath)
}

func scanOAuthStrings(ctx context.Context, binaryPath string) ([]string, []string, error) {
	f, err := os.Open(binaryPath)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()

	var ids []string
	var secrets []string
	seenID := make(map[string]bool)
	seenSecret := make(map[string]bool)

	const (
		chunkSize = 4 * 1024 * 1024
		overlap   = 256
	)
	buf := make([]byte, overlap+chunkSize)
	carry := 0
	idMarker := []byte(".apps.googleusercontent.com")
	secretMarker := []byte("GOCSPX-")

	for {
		if err := ctx.Err(); err != nil {
			return ids, secrets, err
		}
		n, err := f.Read(buf[carry:])
		if n > 0 {
			total := carry + n
			chunk := buf[:total]
			if bytes.Contains(chunk, idMarker) {
				for _, m := range idRegex.FindAll(chunk, -1) {
					s := string(m)
					if !seenID[s] {
						seenID[s] = true
						ids = append(ids, s)
					}
				}
			}
			if bytes.Contains(chunk, secretMarker) {
				for _, m := range secretRegex.FindAll(chunk, -1) {
					s := string(m)
					if !seenSecret[s] {
						seenSecret[s] = true
						secrets = append(secrets, s)
					}
				}
			}
			carry = min(total, overlap)
			copy(buf[:carry], chunk[total-carry:total])
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return ids, secrets, err
		}
	}

	return ids, secrets, nil
}
