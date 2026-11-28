package proxy

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	xproxy "golang.org/x/net/proxy"
)

const TargetEndpoint = "https://accounts.google.com"

func testProxy(parent context.Context, proto, server string, port int, user, pass string, timeout time.Duration) bool {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	hostPort := fmt.Sprintf("%s:%d", server, port)
	var transport *http.Transport

	if proto == "socks5h" || proto == "socks5" {
		var auth *xproxy.Auth
		if user != "" {
			auth = &xproxy.Auth{User: user, Password: pass}
		}
		dialer, err := xproxy.SOCKS5("tcp", hostPort, auth, xproxy.Direct)
		if err != nil {
			return false
		}
		if contextDialer, ok := dialer.(xproxy.ContextDialer); ok {
			transport = &http.Transport{
				DialContext: contextDialer.DialContext,
			}
		} else {
			transport = &http.Transport{
				DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
					return dialer.Dial(network, addr)
				},
			}
		}
	} else {
		scheme := proto
		proxyURLStr := fmt.Sprintf("%s://%s", scheme, hostPort)
		if user != "" {
			if pass != "" {
				proxyURLStr = fmt.Sprintf("%s://%s:%s@%s", scheme, url.QueryEscape(user), url.QueryEscape(pass), hostPort)
			} else {
				proxyURLStr = fmt.Sprintf("%s://%s@%s", scheme, url.QueryEscape(user), hostPort)
			}
		}
		pURL, err := url.Parse(proxyURLStr)
		if err != nil {
			return false
		}
		transport = &http.Transport{
			Proxy: http.ProxyURL(pURL),
		}
	}

	defer transport.CloseIdleConnections()

	client := &http.Client{
		Transport: transport,
		Timeout:   timeout,
	}

	req, err := http.NewRequestWithContext(ctx, "HEAD", TargetEndpoint, nil)
	if err != nil {
		return false
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	_ = resp.Body.Close()

	// 407 = Proxy Authentication Required -> not working
	if resp.StatusCode == http.StatusProxyAuthRequired {
		return false
	}

	return resp.StatusCode >= 200 && resp.StatusCode < 500
}

func RaceProtocol(server string, port int, user, pass string) (string, error) {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", server, port), 2*time.Second)
	if err != nil {
		return "", fmt.Errorf("tcp %s:%d unreachable: %w", server, port, err)
	}
	_ = conn.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Priority order: https, socks5h, http
	candidates := []string{"https", "socks5h", "http"}
	type result struct {
		proto string
		index int
	}
	resChan := make(chan result, len(candidates))
	var wg sync.WaitGroup

	for i, cand := range candidates {
		wg.Add(1)
		go func(idx int, proto string) {
			defer wg.Done()
			if testProxy(ctx, proto, server, port, user, pass, 3*time.Second) {
				resChan <- result{proto: proto, index: idx}
			}
		}(i, cand)
	}

	go func() {
		wg.Wait()
		close(resChan)
	}()

	var bestResult *result
	for res := range resChan {
		if bestResult == nil || res.index < bestResult.index {
			r := res
			bestResult = &r
			if res.index == 0 {
				// Highest priority already won
				break
			}
		}
	}

	if bestResult != nil {
		return bestResult.proto, nil
	}

	return "", fmt.Errorf("all protocols failed to tunnel to Google")
}
