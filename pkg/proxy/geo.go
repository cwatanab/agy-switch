package proxy

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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

// IsBlockedCountry reports whether the country code is unsupported by Google AI.
func IsBlockedCountry(cc string) bool {
	return blockedCountries[strings.ToUpper(strings.TrimSpace(cc))]
}

func ProbeGeo(proxyURL string) (string, bool, error) {
	return ProbeGeoWithURL(proxyURL, DefaultGeoURL)
}

func ProbeGeoWithURL(proxyURL, targetURL string) (string, bool, error) {
	client := &http.Client{
		Timeout: 4 * time.Second,
	}

	if proxyURL != "" {
		tr, err := TransportForProxyURL(proxyURL)
		if err != nil {
			return "", false, err
		}
		defer tr.CloseIdleConnections()
		client.Transport = tr
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

	return cc, IsBlockedCountry(cc), nil
}
