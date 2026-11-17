package proxy

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const DefaultGeoURL = "https://api.country.is"

var blockedCountries = map[string]bool{
	"CN": true,
	"HK": true,
	"MO": true,
	"RU": true,
	"BY": true,
	"IR": true,
	"KP": true,
	"CU": true,
	"SY": true,
}

func ProbeGeo(proxyURL string) (string, bool, error) {
	return ProbeGeoWithURL(proxyURL, DefaultGeoURL)
}

func ProbeGeoWithURL(proxyURL, targetURL string) (string, bool, error) {
	client := &http.Client{
		Timeout: 4 * time.Second,
	}

	if proxyURL != "" {
		pURL, err := url.Parse(proxyURL)
		if err != nil {
			return "", false, fmt.Errorf("invalid proxy url: %w", err)
		}
		client.Transport = &http.Transport{
			Proxy: http.ProxyURL(pURL),
		}
	}

	resp, err := client.Get(targetURL)
	if err != nil {
		return "", false, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", false, err
	}

	var res struct {
		Country     string `json:"country"`
		CountryCode string `json:"country_code"`
	}
	if err := json.Unmarshal(body, &res); err != nil {
		return "", false, fmt.Errorf("unmarshal geo response: %w", err)
	}

	cc := strings.ToUpper(res.Country)
	if cc == "" {
		cc = strings.ToUpper(res.CountryCode)
	}

	blocked := blockedCountries[cc]
	return cc, blocked, nil
}
