package proxy

import (
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
)

const BridgeUser = "agy-switch"

type ProbeResult struct {
	At     int64  `json:"at"`
	OK     bool   `json:"ok"`
	Err    string `json:"err,omitempty"`
	ExitIP string `json:"exit_ip,omitempty"`
	CC     string `json:"cc,omitempty"`
	City   string `json:"city,omitempty"`
	ISP    string `json:"isp,omitempty"`
	Kind   string `json:"kind,omitempty"`
}

type Config struct {
	Name      string       `json:"name"`
	Host      string       `json:"host,omitempty"`
	Server    string       `json:"server,omitempty"`
	Port      int          `json:"port"`
	Protocol  string       `json:"protocol"` // https, socks5h, http
	Username  string       `json:"username,omitempty"`
	Password  string       `json:"password,omitempty"`
	Country   string       `json:"country,omitempty"`
	Active    bool         `json:"active"`
	ExpiresAt int64        `json:"expires_at,omitempty"`
	Fallback  bool         `json:"fallback,omitempty"`
	Check     *ProbeResult `json:"check,omitempty"`
}

func (c *Config) GetServer() string {
	if c.Host != "" {
		return c.Host
	}
	return c.Server
}

func (c *Config) IsSOCKS() bool {
	p := strings.ToLower(c.Protocol)
	return p == "socks5" || p == "socks5h"
}

// State returns "on", "expired_fallback", or "expired_block".
func (c *Config) State(now time.Time) string {
	if c.ExpiresAt > 0 && now.Unix() >= c.ExpiresAt {
		if c.Fallback {
			return "expired_fallback"
		}
		return "expired_block"
	}
	return "on"
}

// URL returns the proxy URL normalized for Go's http.ProxyURL (using socks5 for socks5h).
func (c *Config) URL() string {
	return c.buildURL(false, false)
}

// RawURL returns the proxy URL preserving socks5h and optionally masking credentials.
func (c *Config) RawURL(mask bool) string {
	return c.buildURL(true, mask)
}

func (c *Config) buildURL(preserveSocks5h bool, mask bool) string {
	scheme := strings.ToLower(c.Protocol)
	if scheme == "" {
		scheme = "http"
	}
	if !preserveSocks5h && scheme == "socks5h" {
		scheme = "socks5"
	}
	host := fmt.Sprintf("%s:%d", c.GetServer(), c.Port)
	if mask && c.Username != "" {
		u := c.Username
		maskedUser := "****"
		if len(u) > 2 {
			maskedUser = u[:2] + "****"
		}
		return fmt.Sprintf("%s://%s:****@%s", scheme, maskedUser, host)
	}
	var userinfo *url.Userinfo
	if c.Username != "" {
		if c.Password != "" {
			userinfo = url.UserPassword(c.Username, c.Password)
		} else {
			userinfo = url.User(c.Username)
		}
	}
	u := url.URL{
		Scheme: scheme,
		User:   userinfo,
		Host:   host,
	}
	return u.String()
}

// EnvProxyURL returns a user-exported HTTPS_PROXY / https_proxy URL, ignoring
// agy-switch's own local SOCKS bridge URL.
func EnvProxyURL() string {
	raw := os.Getenv("HTTPS_PROXY")
	if raw == "" {
		raw = os.Getenv("https_proxy")
	}
	if raw == "" {
		return ""
	}
	if u, err := url.Parse(raw); err == nil && u.User != nil && u.User.Username() == BridgeUser {
		return ""
	}
	return raw
}

// FromEnv parses an existing HTTPS_PROXY-style URL from the environment.
func FromEnv() *Config {
	raw := EnvProxyURL()
	if raw == "" {
		return nil
	}
	candidate := raw
	if !strings.Contains(candidate, "://") {
		candidate = "http://" + candidate
	}
	u, err := url.Parse(candidate)
	if err != nil || u.Hostname() == "" || u.Port() == "" {
		return nil
	}
	var port int
	if _, err := fmt.Sscanf(u.Port(), "%d", &port); err != nil || port < 1 || port > 65535 {
		return nil
	}
	proto := strings.ToLower(u.Scheme)
	if proto == "" {
		proto = "http"
	}
	var user, pass string
	if u.User != nil {
		user = u.User.Username()
		pass, _ = u.User.Password()
	}
	return &Config{
		Host:     u.Hostname(),
		Server:   u.Hostname(),
		Port:     port,
		Protocol: proto,
		Username: user,
		Password: pass,
	}
}
