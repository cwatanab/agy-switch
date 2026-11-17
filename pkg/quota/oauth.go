package quota

import (
	"context"
	"encoding/json"
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
	idRegex     = regexp.MustCompile(`\d{6,}-[a-z0-9_]+\.apps\.googleusercontent\.com`)
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
	pair, err := c.oauthCredentials()
	if err != nil {
		return err
	}
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
	return nil
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
	f, err := os.Open(binaryPath)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()

	var ids []string
	var secrets []string
	seenID := make(map[string]bool)
	seenSecret := make(map[string]bool)

	buf := make([]byte, 4*1024*1024)
	var tail []byte

	for {
		n, err := f.Read(buf)
		if n > 0 {
			chunk := append(tail, buf[:n]...)
			for _, m := range idRegex.FindAll(chunk, -1) {
				s := string(m)
				if !seenID[s] {
					seenID[s] = true
					ids = append(ids, s)
				}
			}
			for _, m := range secretRegex.FindAll(chunk, -1) {
				s := string(m)
				if !seenSecret[s] {
					seenSecret[s] = true
					secrets = append(secrets, s)
				}
			}
			if len(chunk) > 256 {
				tail = chunk[len(chunk)-256:]
			} else {
				tail = chunk
			}
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
