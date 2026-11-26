package quota

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"runtime"
	"strconv"
	"time"
)

const defaultUserAgentVersion = "1.5.18"

func quotaUserAgent() string {
	return fmt.Sprintf("antigravity/%s (os_type=%s; arch=%s)", defaultUserAgentVersion, runtime.GOOS, runtime.GOARCH)
}

func (c *Client) postJSON(ctx context.Context, token, path string, body interface{}) (map[string]interface{}, error) {
	var data []byte
	if body != nil {
		var err error
		data, err = json.Marshal(body)
		if err != nil {
			return nil, err
		}
	}
	ua := quotaUserAgent()
	headers := http.Header{"Authorization": {"Bearer " + token}, "Content-Type": {"application/json"}, "User-Agent": {ua}}
	return c.requestJSON(ctx, c.baseURL+path, data, headers)
}

type httpStatusError struct {
	status int
}

func (e httpStatusError) Error() string { return fmt.Sprintf("API returned HTTP %d", e.status) }

func (c *Client) requestJSON(ctx context.Context, target string, data []byte, headers http.Header) (map[string]interface{}, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		body, status, retryAfter, err := c.attempt(ctx, target, data, headers)
		if err == nil && status >= 200 && status < 300 {
			var result map[string]interface{}
			if err := json.Unmarshal(body, &result); err != nil {
				return nil, fmt.Errorf("decode API response: %w", err)
			}
			return result, nil
		}
		if err != nil {
			lastErr = err
		} else {
			lastErr = httpStatusError{status: status}
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		retry := status == 429 || status == 500 || status == 502 || status == 503 || status == 504
		if err != nil {
			var networkErr net.Error
			retry = errors.As(err, &networkErr) && (networkErr.Timeout() || networkErr.Temporary()) &&
				!errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded)
		}
		if !retry || attempt == 2 {
			break
		}
		delay := time.Duration(1<<attempt) * 100 * time.Millisecond
		if seconds, err := strconv.Atoi(retryAfter); err == nil && seconds > 0 {
			if deadline, ok := ctx.Deadline(); ok && time.Until(deadline)/time.Second <= time.Duration(seconds) {
				return nil, lastErr
			}
			if int64(seconds) > int64((time.Duration(1<<63-1))/time.Second) {
				return nil, lastErr
			}
			delay = time.Duration(seconds) * time.Second
		} else if until, err := http.ParseTime(retryAfter); err == nil && time.Until(until) > delay {
			delay = time.Until(until)
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	return nil, lastErr
}

func (c *Client) attempt(ctx context.Context, target string, data []byte, headers http.Header) ([]byte, int, string, error) {
	select {
	case c.slots <- struct{}{}:
	case <-ctx.Done():
		return nil, 0, "", ctx.Err()
	}
	defer func() { <-c.slots }()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(data))
	if err != nil {
		return nil, 0, "", err
	}
	req.Header = headers.Clone()
	resp, err := c.httpClient.Do(req)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return nil, 0, "", fmt.Errorf("API request failed: %w", err)
	}
	defer resp.Body.Close()
	const maxResponse = 4 * 1024 * 1024
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if len(body) > maxResponse {
		return nil, resp.StatusCode, "", fmt.Errorf("API response exceeds size limit")
	}
	return body, resp.StatusCode, resp.Header.Get("Retry-After"), err
}
