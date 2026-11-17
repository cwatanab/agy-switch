package profile

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Token struct {
	AccessToken  string                 `json:"access_token"`
	RefreshToken string                 `json:"refresh_token"`
	ExpiresAt    time.Time              `json:"expires_at"`
	Email        string                 `json:"email"`
	Raw          map[string]interface{} `json:"-"`
}

func decodeJWTEmail(jwt string) string {
	parts := strings.Split(jwt, ".")
	if len(parts) < 2 {
		return ""
	}
	payloadSegment := parts[1]
	decoded, err := base64.RawURLEncoding.DecodeString(payloadSegment)
	if err != nil {
		if rem := len(payloadSegment) % 4; rem != 0 {
			payloadSegment += strings.Repeat("=", 4-rem)
		}
		decoded, err = base64.URLEncoding.DecodeString(payloadSegment)
		if err != nil {
			return ""
		}
	}
	var claims struct {
		Email string `json:"email"`
	}
	if err := json.Unmarshal(decoded, &claims); err == nil && claims.Email != "" {
		return claims.Email
	}
	return ""
}

func FetchGoogleEmail(accessToken string) string {
	email, _ := LookupEmail(context.Background(), &http.Client{Timeout: 5 * time.Second}, accessToken)
	return email
}

// LookupEmail resolves an access token's identity using the provided HTTP client.
func LookupEmail(ctx context.Context, client *http.Client, accessToken string) (string, error) {
	if accessToken == "" {
		return "", nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://oauth2.googleapis.com/tokeninfo?access_token="+url.QueryEscape(accessToken), nil)
	if err != nil {
		return "", fmt.Errorf("create account identity request")
	}
	resp, err := client.Do(req)
	if err != nil {
		// URL errors can contain the access token in their query string.
		return "", fmt.Errorf("account identity request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("account identity returned HTTP %d", resp.StatusCode)
	}
	var res struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err == nil && res.Email != "" {
		return res.Email, nil
	}
	return "", fmt.Errorf("account identity response has no email")
}

func ParseTokenJSON(data []byte) (*Token, error) {
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("unmarshal token json: %w", err)
	}

	tok := &Token{Raw: raw}

	// Check if this is agy's standard nested structure: {"token": {"access_token": ...}}
	tokenObj := raw
	if inner, ok := raw["token"].(map[string]interface{}); ok {
		tokenObj = inner
	}

	if at, ok := tokenObj["access_token"].(string); ok {
		tok.AccessToken = at
	}
	if rt, ok := tokenObj["refresh_token"].(string); ok {
		tok.RefreshToken = rt
	}

	// 1. Direct email field (check outer and inner)
	for _, m := range []map[string]interface{}{raw, tokenObj} {
		for _, k := range []string{"id_token_email", "email", "user_email"} {
			if em, ok := m[k].(string); ok && em != "" {
				tok.Email = em
				break
			}
		}
		if tok.Email != "" {
			break
		}
	}

	// 2. JWT in id_token
	if tok.Email == "" {
		for _, m := range []map[string]interface{}{raw, tokenObj} {
			if idt, ok := m["id_token"].(string); ok && idt != "" {
				tok.Email = decodeJWTEmail(idt)
				if tok.Email != "" {
					break
				}
			}
		}
	}

	// 3. JWT in access_token
	if tok.Email == "" && tok.AccessToken != "" {
		tok.Email = decodeJWTEmail(tok.AccessToken)
	}

	// Expiry parsing
	for _, m := range []map[string]interface{}{tokenObj, raw} {
		if exp, ok := m["expiry"].(string); ok && exp != "" {
			if t, err := time.Parse(time.RFC3339Nano, exp); err == nil {
				tok.ExpiresAt = t
				break
			} else if t, err := time.Parse(time.RFC3339, exp); err == nil {
				tok.ExpiresAt = t
				break
			}
		} else if expNum, ok := m["expires_at"].(float64); ok {
			tok.ExpiresAt = time.Unix(int64(expNum), 0)
			break
		} else if expIn, ok := m["expires_in"].(float64); ok {
			tok.ExpiresAt = time.Now().Add(time.Duration(expIn) * time.Second)
			break
		}
	}

	return tok, nil
}
