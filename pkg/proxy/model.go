package proxy

import (
	"fmt"
	"net/url"
)

type Config struct {
	Name      string `json:"name"`
	Host      string `json:"host,omitempty"`
	Server    string `json:"server,omitempty"`
	Port      int    `json:"port"`
	Protocol  string `json:"protocol"` // https, socks5h, http
	Username  string `json:"username,omitempty"`
	Password  string `json:"password,omitempty"`
	Country   string `json:"country,omitempty"`
	Active    bool   `json:"active"`
	ExpiresAt int64  `json:"expires_at,omitempty"`
	Fallback  bool   `json:"fallback,omitempty"`
}

func (c *Config) GetServer() string {
	if c.Host != "" {
		return c.Host
	}
	return c.Server
}

func (c *Config) URL() string {
	scheme := c.Protocol
	if scheme == "socks5h" {
		scheme = "socks5"
	}
	var userinfo *url.Userinfo
	if c.Username != "" {
		if c.Password != "" {
			userinfo = url.UserPassword(c.Username, c.Password)
		} else {
			userinfo = url.User(c.Username)
		}
	}
	host := fmt.Sprintf("%s:%d", c.GetServer(), c.Port)
	u := url.URL{
		Scheme: scheme,
		User:   userinfo,
		Host:   host,
	}
	return u.String()
}
