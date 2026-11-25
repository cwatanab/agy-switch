package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	xproxy "golang.org/x/net/proxy"
)

const (
	DefaultIPAPIURL   = "http://ip-api.com/json/?fields=status,country,countryCode,city,isp,org,proxy,hosting,mobile,query"
	DefaultIPWhoisURL = "https://ipwho.is/"
	ProbeTTL          = 10 * time.Minute
)

var hostingASNs = map[int]bool{
	15169: true, 16509: true, 8075: true, 13335: true,
	14061: true, 45102: true, 132203: true, 31898: true,
	24940: true, 16276: true, 20473: true, 63949: true,
}

// ProbeOptions overrides endpoints and timeouts for testing.
type ProbeOptions struct {
	TargetURL  string
	IPAPIURL   string
	IPWhoisURL string
	Timeout    time.Duration
	SkipTCP    bool
}

// TransportForProxy builds an *http.Transport routing through cfg.
func TransportForProxy(cfg *Config) (*http.Transport, error) {
	if cfg == nil {
		return &http.Transport{}, nil
	}
	return TransportForProxyURL(cfg.RawURL(false))
}

// TransportForProxyURL builds an *http.Transport for http, https, socks5, or socks5h proxy URLs.
func TransportForProxyURL(rawURL string) (*http.Transport, error) {
	if rawURL == "" {
		return &http.Transport{ForceAttemptHTTP2: true}, nil
	}
	if !strings.Contains(rawURL, "://") {
		rawURL = "http://" + rawURL
	}
	pURL, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid proxy url: %w", err)
	}
	scheme := strings.ToLower(pURL.Scheme)
	if scheme == "socks5" || scheme == "socks5h" {
		var auth *xproxy.Auth
		if pURL.User != nil {
			pw, _ := pURL.User.Password()
			auth = &xproxy.Auth{User: pURL.User.Username(), Password: pw}
		}
		dialer, err := xproxy.SOCKS5("tcp", pURL.Host, auth, xproxy.Direct)
		if err != nil {
			return nil, fmt.Errorf("build socks5 dialer: %w", err)
		}
		if ctxDialer, ok := dialer.(xproxy.ContextDialer); ok {
			return &http.Transport{DialContext: ctxDialer.DialContext, ForceAttemptHTTP2: true}, nil
		}
		return &http.Transport{
			DialContext: func(_ context.Context, network, addr string) (net.Conn, error) {
				return dialer.Dial(network, addr)
			},
			ForceAttemptHTTP2: true,
		}, nil
	}
	return &http.Transport{Proxy: http.ProxyURL(pURL), ForceAttemptHTTP2: true}, nil
}

// NewManagedHTTPClient returns an HTTP client whose transport dynamically honors
// pre-existing environment proxies or the active unexpired managed proxy.
func NewManagedHTTPClient(store *Store, timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: &managedTransport{store: store},
	}
}

type managedTransport struct {
	store     *Store
	mu        sync.Mutex
	lastURL   string
	transport *http.Transport
}

func (m *managedTransport) effectiveProxyURL() string {
	if env := EnvProxyURL(); env != "" {
		return env
	}
	if m.store == nil {
		return ""
	}
	active, err := m.store.Active()
	if err != nil || active == nil || active.State(time.Now()) != "on" {
		return ""
	}
	return active.RawURL(false)
}

func (m *managedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	proxyURL := m.effectiveProxyURL()
	m.mu.Lock()
	if m.transport == nil || m.lastURL != proxyURL {
		tr, err := TransportForProxyURL(proxyURL)
		if err != nil {
			m.mu.Unlock()
			return nil, err
		}
		if m.transport != nil {
			m.transport.CloseIdleConnections()
		}
		m.transport = tr
		m.lastURL = proxyURL
	}
	tr := m.transport
	m.mu.Unlock()
	return tr.RoundTrip(req)
}

// ProbeProxy checks TCP reachability, tunneling to Google AI, and exit geo/IP classification.
func ProbeProxy(ctx context.Context, cfg Config) *ProbeResult {
	return ProbeProxyWithOptions(ctx, cfg, ProbeOptions{})
}

func ProbeProxyWithOptions(ctx context.Context, cfg Config, opts ProbeOptions) *ProbeResult {
	res := &ProbeResult{At: time.Now().Unix(), OK: false}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 6 * time.Second
	}
	targetURL := opts.TargetURL
	if targetURL == "" {
		if env := os.Getenv("AGY_SWITCH_ENDPOINT"); env != "" {
			targetURL = env
		} else {
			targetURL = TargetEndpoint
		}
	}
	ipapiURL := opts.IPAPIURL
	if ipapiURL == "" {
		if env := os.Getenv("AGY_SWITCH_IPAPI_URL"); env != "" {
			ipapiURL = env
		} else {
			ipapiURL = DefaultIPAPIURL
		}
	}
	ipwhoisURL := opts.IPWhoisURL
	if ipwhoisURL == "" {
		if env := os.Getenv("AGY_SWITCH_IPWHOIS_URL"); env != "" {
			ipwhoisURL = env
		} else {
			ipwhoisURL = DefaultIPWhoisURL
		}
	}

	hostPort := fmt.Sprintf("%s:%d", cfg.GetServer(), cfg.Port)
	if !opts.SkipTCP {
		dialer := net.Dialer{Timeout: 3 * time.Second}
		conn, err := dialer.DialContext(ctx, "tcp", hostPort)
		if err != nil {
			res.Err = fmt.Sprintf("tcp %s unreachable", hostPort)
			return res
		}
		_ = conn.Close()
	}

	tr, err := TransportForProxy(&cfg)
	if err != nil {
		res.Err = err.Error()
		return res
	}
	defer tr.CloseIdleConnections()

	client := &http.Client{Transport: tr, Timeout: timeout}
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, targetURL, nil)
	if err != nil {
		res.Err = err.Error()
		return res
	}
	resp, err := client.Do(req)
	if err != nil || resp.StatusCode == http.StatusProxyAuthRequired || resp.StatusCode >= 500 {
		if resp != nil {
			_ = resp.Body.Close()
		}
		res.Err = "cannot tunnel to googleapis — dead proxy, wrong protocol, or blocked by the local network"
		return res
	}
	_ = resp.Body.Close()
	res.OK = true

	for i, geoURL := range []string{ipapiURL, ipwhoisURL} {
		gReq, err := http.NewRequestWithContext(ctx, http.MethodGet, geoURL, nil)
		if err != nil {
			continue
		}
		gResp, err := client.Do(gReq)
		if err != nil {
			continue
		}
		body, rErr := io.ReadAll(io.LimitReader(gResp.Body, 512*1024))
		_ = gResp.Body.Close()
		if rErr != nil {
			continue
		}
		var parsed *ProbeResult
		if i == 0 {
			parsed = parseIPAPI(body)
		} else {
			parsed = parseIPWhois(body)
		}
		if parsed != nil {
			res.ExitIP = parsed.ExitIP
			res.CC = parsed.CC
			res.City = parsed.City
			res.ISP = parsed.ISP
			res.Kind = parsed.Kind
			break
		}
	}

	return res
}

func parseIPAPI(body []byte) *ProbeResult {
	var j struct {
		Status      string `json:"status"`
		CountryCode string `json:"countryCode"`
		City        string `json:"city"`
		ISP         string `json:"isp"`
		Org         string `json:"org"`
		Proxy       bool   `json:"proxy"`
		Hosting     bool   `json:"hosting"`
		Mobile      bool   `json:"mobile"`
		Query       string `json:"query"`
	}
	if err := json.Unmarshal(body, &j); err != nil || j.Status != "success" {
		return nil
	}
	kind := "residential/ISP"
	switch {
	case j.Hosting:
		kind = "hosting"
	case j.Mobile:
		kind = "mobile"
	case j.Proxy:
		kind = "proxy/VPN"
	}
	isp := j.ISP
	if isp == "" {
		isp = j.Org
	}
	return &ProbeResult{
		ExitIP: j.Query,
		CC:     strings.ToUpper(j.CountryCode),
		City:   j.City,
		ISP:    isp,
		Kind:   kind,
	}
}

func parseIPWhois(body []byte) *ProbeResult {
	var j struct {
		Success     bool   `json:"success"`
		IP          string `json:"ip"`
		CountryCode string `json:"country_code"`
		City        string `json:"city"`
		Connection  struct {
			ASN int    `json:"asn"`
			ISP string `json:"isp"`
			Org string `json:"org"`
		} `json:"connection"`
	}
	if err := json.Unmarshal(body, &j); err != nil || !j.Success {
		return nil
	}
	kind := ""
	if hostingASNs[j.Connection.ASN] {
		kind = "hosting"
	}
	isp := j.Connection.ISP
	if isp == "" {
		isp = j.Connection.Org
	}
	return &ProbeResult{
		ExitIP: j.IP,
		CC:     strings.ToUpper(j.CountryCode),
		City:   j.City,
		ISP:    isp,
		Kind:   kind,
	}
}
